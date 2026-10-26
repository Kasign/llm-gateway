// ratelimit 用 Redis 做按 API Key 的每分钟请求计数，并暂存模型下载进度。
package ratelimit

import (
	"context"
	"fmt"
	"strings"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter 封装 Redis 客户端，供 server 在鉴权后决定是否放行请求。
type Limiter struct{ r *redis.Client }

// New 连接 Redis；addr 形如 host:6379。
func New(addr, password string, db int) *Limiter {
	return &Limiter{r: redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})}
}
func (l *Limiter) Ping(ctx context.Context) error { _, err := l.r.Ping(ctx).Result(); return err }
func (l *Limiter) Close() error                   { return l.r.Close() }

// allowRateLimitScript 原子递增固定窗口计数，并为历史上可能遗留的无 TTL key 修复过期时间。
// Lua 避免 INCR 与 EXPIRE 之间的竞态及忽略 EXPIRE 错误导致的永久 key。
var allowRateLimitScript = redis.NewScript(`
local current = redis.call("INCR", KEYS[1])
if redis.call("TTL", KEYS[1]) < 0 then
  redis.call("EXPIRE", KEYS[1], ARGV[1])
end
return current
`)

// reserveProviderTokenBudgetScript 原子预占每日 Token 预算，避免并发请求同时越额。
var reserveProviderTokenBudgetScript = redis.NewScript(`
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
local reserved = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
if reserved <= 0 or current + reserved > limit then
  return "0|" .. current
end
local next = redis.call("INCRBY", KEYS[1], reserved)
if redis.call("TTL", KEYS[1]) < 0 then
  redis.call("EXPIRE", KEYS[1], ARGV[3])
end
return "1|" .. next
`)

// settleProviderTokenBudgetScript 将保守预占校正为供应商报告的真实 Token。
var settleProviderTokenBudgetScript = redis.NewScript(`
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
local delta = tonumber(ARGV[1])
local next = current + delta
if next < 0 then next = 0 end
redis.call("SET", KEYS[1], next, "EX", ARGV[2])
return next
`)

// rateLimitBucketKey 保持既有 UTC 固定分钟窗口的 key 格式。
func rateLimitBucketKey(key string, now time.Time) string {
	bucket := now.UTC().Format("200601021504")
	return fmt.Sprintf("llmgw:rl:%s:%s", key, bucket)
}

// Allow 在当前 UTC 分钟桶内递增计数；超过 rpm 返回 false，rpm<=0 表示不限流。
func (l *Limiter) Allow(ctx context.Context, key string, rpm int) (bool, int64, error) {
	return l.allowAt(ctx, key, rpm, time.Now())
}

// AllowProvider 对同一 Provider 的所有调用方与 Agent 共享每分钟预算；rpm<=0 表示禁用。
// 使用独立 Redis key 前缀，不能与按调用方 API Key 统计的 RPM 互相影响。
func (l *Limiter) AllowProvider(ctx context.Context, providerID string, rpm int) (bool, int64, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return false, 0, fmt.Errorf("provider ID is required")
	}
	return l.Allow(ctx, "provider:"+providerID, rpm)
}

func (l *Limiter) allowAt(ctx context.Context, key string, rpm int, now time.Time) (bool, int64, error) {
	if rpm <= 0 {
		return true, 0, nil
	}
	k := rateLimitBucketKey(key, now)
	n, err := allowRateLimitScript.Run(ctx, l.r, []string{k}, int64(70)).Int64()
	if err != nil {
		return false, 0, err
	}
	return n <= int64(rpm), n, nil
}
func (l *Limiter) SetDownload(ctx context.Context, id string, vals map[string]interface{}) error {
	k := "llmgw:download:" + id
	for f, v := range vals {
		if err := l.r.HSet(ctx, k, f, v).Err(); err != nil {
			return err
		}
	}
	return l.r.Expire(ctx, k, 24*time.Hour).Err()
}

func (l *Limiter) GetDownload(ctx context.Context, id string) (map[string]string, error) {
	return l.r.HGetAll(ctx, "llmgw:download:"+id).Result()
}

func (l *Limiter) ClearDownload(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	return l.r.Del(ctx, "llmgw:download:"+id).Err()
}

// DeleteDownloadFields removes hash fields (e.g. stale "error" after retry).
func (l *Limiter) DeleteDownloadFields(ctx context.Context, id string, fields ...string) error {
	id = strings.TrimSpace(id)
	if id == "" || len(fields) == 0 {
		return nil
	}
	return l.r.HDel(ctx, "llmgw:download:"+id, fields...).Err()
}

const ollamaPullJobsKey = "llmgw:ollama_pull_jobs"

func (l *Limiter) TrackOllamaPullJob(ctx context.Context, jobID string) error {
	return l.r.SAdd(ctx, ollamaPullJobsKey, jobID).Err()
}

func (l *Limiter) UntrackOllamaPullJob(ctx context.Context, jobID string) error {
	return l.r.SRem(ctx, ollamaPullJobsKey, jobID).Err()
}

func (l *Limiter) ListOllamaPullJobs(ctx context.Context) ([]string, error) {
	return l.r.SMembers(ctx, ollamaPullJobsKey).Result()
}

func sessionKey(token string) string { return "llmgw:session:" + token }

// SetSession 保存控制台登录会话；value 为 username|revision。
func (l *Limiter) SetSession(ctx context.Context, token, username string, revision uint64, ttl time.Duration) error {
	username = strings.TrimSpace(username)
	if token == "" || username == "" {
		return fmt.Errorf("invalid session")
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	val := fmt.Sprintf("%s|%d", username, revision)
	return l.r.Set(ctx, sessionKey(token), val, ttl).Err()
}

// GetSession 读取会话对应的用户名与凭据 revision。
func (l *Limiter) GetSession(ctx context.Context, token string) (username string, revision uint64, ok bool, err error) {
	if token == "" {
		return "", 0, false, nil
	}
	v, err := l.r.Get(ctx, sessionKey(token)).Result()
	if err == redis.Nil {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", 0, false, nil
	}
	if i := strings.LastIndex(v, "|"); i >= 0 {
		user := v[:i]
		var rev uint64
		_, _ = fmt.Sscanf(v[i+1:], "%d", &rev)
		if user == "" {
			return "", 0, false, nil
		}
		return user, rev, true, nil
	}
	return v, 0, true, nil
}

// DeleteSession 登出时作废 token。
func (l *Limiter) DeleteSession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return l.r.Del(ctx, sessionKey(token)).Err()
}


// ProviderRPMUsage 查询当前 UTC 分钟窗口内各 Provider 已计数的请求数。
// 只读取 AllowProvider 使用的 key，不递增计数；所有 ID 通过一次 MGET 查询。
func (l *Limiter) ProviderRPMUsage(ctx context.Context, providerIDs []string, now time.Time) (map[string]int64, error) {
	usage := make(map[string]int64, len(providerIDs))
	if len(providerIDs) == 0 {
		return usage, nil
	}
	if l == nil || l.r == nil {
		return nil, fmt.Errorf("Redis rate limiter is not configured")
	}
	ids := make([]string, 0, len(providerIDs))
	keys := make([]string, 0, len(providerIDs))
	for _, id := range providerIDs {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		ids = append(ids, id)
		keys = append(keys, rateLimitBucketKey("provider:"+id, now))
	}
	if len(keys) == 0 {
		return usage, nil
	}
	values, err := l.r.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value == nil {
			usage[ids[i]] = 0
			continue
		}
		count, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse Provider RPM counter")
		}
		usage[ids[i]] = count
	}
	return usage, nil
}


// 每个 UTC 日桶保留 48 小时，使跨日结束的在途请求仍可结算。
const providerTokenBudgetTTLSeconds = 172800

func normalizeTokenBudgetProviderID(raw string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if id == "" || len(id) > 64 || strings.ContainsAny(id, ":{} /") || strings.Contains(id, "\\") || strings.ContainsAny(id, "\r\n\t") {
		return "", fmt.Errorf("invalid Provider ID for token budget")
	}
	return id, nil
}

func providerTokenBudgetBucket(now time.Time) string {
	return now.UTC().Format("20060102")
}

func providerTokenBudgetKey(providerID, bucket string) string {
	return fmt.Sprintf("llmgw:provider_tokens:%s:%s", providerID, bucket)
}

// ReserveProviderTokens 使用 UTC 自然日固定窗口原子预占；超限不修改当前计数。
func (l *Limiter) ReserveProviderTokens(ctx context.Context, providerID string, dailyLimit, reserveTokens int64, now time.Time) (bucket string, used int64, allowed bool, err error) {
	id, err := normalizeTokenBudgetProviderID(providerID)
	if err != nil {
		return "", 0, false, err
	}
	if l == nil || l.r == nil {
		return "", 0, false, fmt.Errorf("Redis token budget is not configured")
	}
	if dailyLimit <= 0 || reserveTokens <= 0 {
		return "", 0, false, fmt.Errorf("invalid Provider token budget reservation")
	}
	bucket = providerTokenBudgetBucket(now)
	raw, err := reserveProviderTokenBudgetScript.Run(
		ctx, l.r, []string{providerTokenBudgetKey(id, bucket)},
		reserveTokens, dailyLimit, providerTokenBudgetTTLSeconds,
	).Text()
	if err != nil {
		return "", 0, false, err
	}
	parts := strings.Split(raw, "|")
	if len(parts) != 2 {
		return "", 0, false, fmt.Errorf("invalid Redis token budget result")
	}
	allowed = parts[0] == "1"
	used, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || used < 0 {
		return "", 0, false, fmt.Errorf("invalid Redis token budget counter")
	}
	return bucket, used, allowed, nil
}

// SettleProviderTokens 将预占值替换为可信用量；传入旧日期桶，可在 UTC 跨日后完成结算。
func (l *Limiter) SettleProviderTokens(ctx context.Context, providerID, bucket string, reservedTokens, actualTokens int64) error {
	id, err := normalizeTokenBudgetProviderID(providerID)
	if err != nil {
		return err
	}
	if l == nil || l.r == nil {
		return fmt.Errorf("Redis token budget is not configured")
	}
	if _, err := time.Parse("20060102", bucket); err != nil {
		return fmt.Errorf("invalid Provider token budget day")
	}
	if reservedTokens <= 0 || actualTokens < 0 {
		return fmt.Errorf("invalid Provider token budget settlement")
	}
	delta := actualTokens - reservedTokens
	return settleProviderTokenBudgetScript.Run(
		ctx, l.r, []string{providerTokenBudgetKey(id, bucket)},
		delta, providerTokenBudgetTTLSeconds,
	).Err()
}

// ProviderTokenBudgetUsage 只读查询当前 UTC 日桶已用与预占 Token，不递增计数。
func (l *Limiter) ProviderTokenBudgetUsage(ctx context.Context, providerIDs []string, now time.Time) (map[string]int64, error) {
	usage := make(map[string]int64, len(providerIDs))
	if len(providerIDs) == 0 {
		return usage, nil
	}
	if l == nil || l.r == nil {
		return nil, fmt.Errorf("Redis token budget is not configured")
	}
	ids := make([]string, 0, len(providerIDs))
	keys := make([]string, 0, len(providerIDs))
	bucket := providerTokenBudgetBucket(now)
	for _, rawID := range providerIDs {
		id, err := normalizeTokenBudgetProviderID(rawID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
		keys = append(keys, providerTokenBudgetKey(id, bucket))
	}
	values, err := l.r.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value == nil {
			usage[ids[i]] = 0
			continue
		}
		count, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		if err != nil || count < 0 {
			return nil, fmt.Errorf("parse Provider token budget counter")
		}
		usage[ids[i]] = count
	}
	return usage, nil
}
