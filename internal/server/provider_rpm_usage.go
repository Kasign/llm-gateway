package server

import (
	"context"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/provider"
)

// listManagedProviderViewsWithRPMUsage 在原有安全 Provider 视图上附加 RPM 与并发用量快照。
// Redis/并发状态只作观测用途：查询失败不应阻断 Provider 管理，也不会消耗配额。
func (s *Server) listManagedProviderViewsWithRPMUsage(ctx context.Context) []map[string]interface{} {
	views := s.listManagedProviderViews()
	if len(views) == 0 {
		return views
	}
	attachProviderConcurrencyUsage(s, views)
	rpmLimits := make(map[string]int, len(views))
	tokenLimits := make(map[string]int64, len(views))
	rpmIDs := make([]string, 0, len(views))
	tokenIDs := make([]string, 0, len(views))
	for _, view := range views {
		id, _ := view["id"].(string)
		rpm, _ := view["requests_per_minute"].(int)
		tokenBudget, _ := view["token_budget_per_day"].(int64)
		view["requests_per_minute_used"] = int64(0)
		view["requests_per_minute_remaining"] = int64(0)
		view["requests_per_minute_reset_seconds"] = 0
		view["requests_per_minute_usage_available"] = rpm <= 0
		view["token_budget_per_day_used"] = int64(0)
		view["token_budget_per_day_remaining"] = int64(0)
		view["token_budget_per_day_reset_seconds"] = 0
		view["token_budget_per_day_usage_available"] = tokenBudget <= 0
		if id == "" {
			continue
		}
		if rpm > 0 {
			rpmLimits[id] = rpm
			rpmIDs = append(rpmIDs, id)
		}
		if tokenBudget > 0 {
			tokenLimits[id] = tokenBudget
			tokenIDs = append(tokenIDs, id)
		}
	}
	now := time.Now().UTC()
	nextMinute := now.Truncate(time.Minute).Add(time.Minute)
	minuteReset := int((nextMinute.Sub(now) + time.Second - 1) / time.Second)
	nextDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	dayReset := int((nextDay.Sub(now) + time.Second - 1) / time.Second)
	for _, view := range views {
		if rpm, _ := view["requests_per_minute"].(int); rpm > 0 {
			view["requests_per_minute_reset_seconds"] = minuteReset
		}
		if budget, _ := view["token_budget_per_day"].(int64); budget > 0 {
			view["token_budget_per_day_reset_seconds"] = dayReset
		}
	}
	if s == nil || s.limiter == nil {
		return views
	}
	// 管理台只读快照限时查询 Redis，失败不影响 Provider 管理或请求准入。
	usageCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	if len(rpmIDs) > 0 {
		if counts, err := s.limiter.ProviderRPMUsage(usageCtx, rpmIDs, now); err == nil {
			for _, view := range views {
				id, _ := view["id"].(string)
				rpm, limited := rpmLimits[id]
				if !limited {
					continue
				}
				used := counts[id]
				remaining := int64(rpm) - used
				if remaining < 0 {
					remaining = 0
				}
				view["requests_per_minute_used"] = used
				view["requests_per_minute_remaining"] = remaining
				view["requests_per_minute_usage_available"] = true
			}
		}
	}
	if len(tokenIDs) > 0 {
		if counts, err := s.limiter.ProviderTokenBudgetUsage(usageCtx, tokenIDs, now); err == nil {
			for _, view := range views {
				id, _ := view["id"].(string)
				limit, limited := tokenLimits[id]
				if !limited {
					continue
				}
				used := counts[id]
				remaining := limit - used
				if remaining < 0 {
					remaining = 0
				}
				view["token_budget_per_day_used"] = used
				view["token_budget_per_day_remaining"] = remaining
				view["token_budget_per_day_usage_available"] = true
			}
		}
	}
	return views
}
type providerConcurrencyReporter interface {
	ConcurrencyLimiter() *provider.ConcurrencyLimiter
}

// providerConcurrencyLimiter 优先读取 Web Provider 保留的共享 limiter，
// 即使 Provider 被停用/移出 Registry，已在途请求的槽位仍可被观测。
func (s *Server) providerConcurrencyLimiter(id string) *provider.ConcurrencyLimiter {
	id = strings.ToLower(strings.TrimSpace(id))
	if s == nil || id == "" || isLocalInferenceBackend(id) {
		return nil
	}
	s.providerConfigMu.RLock()
	row := s.webProviders[id]
	limiter := row.ConcurrencyLimiter
	s.providerConfigMu.RUnlock()
	if limiter != nil {
		return limiter
	}
	if s.providers == nil {
		return nil
	}
	instance, err := s.providers.Resolve(id)
	if err != nil {
		return nil
	}
	reporter, ok := instance.(providerConcurrencyReporter)
	if !ok {
		return nil
	}
	return reporter.ConcurrencyLimiter()
}

// attachProviderConcurrencyUsage 给管理视图附加单个 Gateway 进程内的并发快照。
// 这是只读观测值；不用于准入判断，也不代表多副本部署的全局并发总量。
func attachProviderConcurrencyUsage(s *Server, views []map[string]interface{}) {
	for _, view := range views {
		view["concurrent_requests_used"] = 0
		view["concurrent_requests_limit"] = 0
		view["concurrent_requests_remaining"] = 0
		view["concurrent_requests_rejected"] = int64(0)
		view["queued_requests"] = 0
		view["queue_limit"] = 0
		view["queue_timeouts"] = int64(0)
		view["concurrent_requests_usage_available"] = false
		id, _ := view["id"].(string)
		limiter := s.providerConcurrencyLimiter(id)
		if limiter == nil {
			continue
		}
		used, limit := limiter.Active(), limiter.Limit()
		if limit <= 0 {
			continue
		}
		remaining := limit - used
		if remaining < 0 {
			remaining = 0
		}
		view["concurrent_requests_used"] = used
		view["concurrent_requests_limit"] = limit
		view["concurrent_requests_remaining"] = remaining
		view["concurrent_requests_rejected"] = limiter.RejectedRequests()
		view["queued_requests"] = limiter.QueueDepth()
		view["queue_limit"] = limiter.QueueLimit()
		view["queue_timeouts"] = limiter.QueueTimeouts()
		view["concurrent_requests_usage_available"] = true
	}
}
