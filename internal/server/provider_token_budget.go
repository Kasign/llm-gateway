package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/core"
)

const defaultProviderBudgetOutputTokens int64 = 8192

type providerTokenBudgetReservation struct {
	ProviderID     string
	Bucket         string
	ReservedTokens int64
}

var providerTokenBudgetTokenFields = []string{"max_output_tokens", "max_completion_tokens", "max_tokens"}

// providerBudgetHasOutputLimit 仅为确实生成内容的兼容端点强制输出上限；其他端点仍计入请求预算。
func providerBudgetHasOutputLimit(path string) bool {
	path = strings.TrimRight(strings.ToLower(path), "/")
	for _, suffix := range []string{"/chat/completions", "/completions", "/responses", "/messages"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

// requestOutputTokenLimit 读取请求声明的最大输出 Token 数；多个兼容字段同时存在时采用最小值。
func requestOutputTokenLimit(body []byte) (map[string]json.RawMessage, int64, error) {
	var object map[string]json.RawMessage
	if len(body) == 0 || json.Unmarshal(body, &object) != nil || object == nil {
		return nil, 0, errors.New("request body must be a JSON object when Provider token budget is enabled")
	}
	var limit int64
	for _, field := range providerTokenBudgetTokenFields {
		raw, ok := object[field]
		if !ok || strings.TrimSpace(string(raw)) == "null" {
			continue
		}
		var value int64
		if err := json.Unmarshal(raw, &value); err != nil || value <= 0 {
			return nil, 0, fmt.Errorf("%s must be a positive integer when Provider token budget is enabled", field)
		}
		if limit == 0 || value < limit {
			limit = value
		}
	}
	if limit == 0 {
		limit = defaultProviderBudgetOutputTokens
	}
	return object, limit, nil
}

// writeRequestOutputTokenLimit 确保调用方显式限制输出；未指定时按协议添加合适字段。
func writeRequestOutputTokenLimit(object map[string]json.RawMessage, path string, limit int64) ([]byte, error) {
	if object == nil || limit <= 0 {
		return nil, errors.New("request output token limit is invalid")
	}
	raw, err := json.Marshal(limit)
	if err != nil {
		return nil, err
	}
	found := false
	for _, field := range providerTokenBudgetTokenFields {
		if _, ok := object[field]; ok {
			object[field] = raw
			found = true
		}
	}
	if !found {
		field := "max_tokens"
		if strings.Contains(strings.ToLower(path), "/responses") {
			field = "max_output_tokens"
		}
		object[field] = raw
	}
	return json.Marshal(object)
}

// mutateProviderTokenBudget 对启用预算的远程 Provider 限制输出上限；最终准入由后续 Redis 原子预占决定。
func (s *Server) mutateProviderTokenBudget(ctx context.Context, st core.State) (core.State, error) {
	if strings.HasSuffix(strings.TrimRight(strings.ToLower(st.Path), "/"), "/count_tokens") || !providerBudgetHasOutputLimit(st.Path) {
		return st, nil
	}
	providerID := s.providerIDForState(st)
	cfg, ok := s.remoteProviderConfig(providerID)
	if !ok || cfg.TokenBudgetPerDay <= 0 {
		return st, nil
	}
	object, requestedLimit, err := requestOutputTokenLimit(st.Body)
	if err != nil {
		return st, &core.Error{Status: 400, Code: "provider_token_budget_invalid_request", Message: "Provider token budget requires a valid JSON request and positive output token limits"}
	}
	candidate, err := writeRequestOutputTokenLimit(object, st.Path, requestedLimit)
	if err != nil {
		return st, &core.Error{Status: 400, Code: "provider_token_budget_invalid_request", Message: "Provider token budget cannot normalize the request output token limit"}
	}
	if s.limiter != nil {
		readCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		usage, readErr := s.limiter.ProviderTokenBudgetUsage(readCtx, []string{providerID}, time.Now().UTC())
		cancel()
		if readErr == nil {
			// 预估仅用于尽可能利用剩余额度；真正的并发准入由后面的 Lua 原子预占决定。
			available := cfg.TokenBudgetPerDay - usage[providerID] - int64(len(candidate))
			if available > 0 && requestedLimit > available {
				if capped, capErr := writeRequestOutputTokenLimit(object, st.Path, available); capErr == nil {
					candidate = capped
				}
			}
		}
	}
	st.Body = candidate
	return st, nil
}

// providerTokenBudgetGuard 在请求级 ACL 与 RPM 检查之后执行，所有 Provider 副本共享 Redis 日预算。
func (s *Server) providerTokenBudgetGuard() core.Guard {
	return func(ctx context.Context, st core.State) error {
		if strings.HasSuffix(strings.TrimRight(strings.ToLower(st.Path), "/"), "/count_tokens") {
			return nil
		}
		providerID := s.providerIDForState(st)
		cfg, ok := s.remoteProviderConfig(providerID)
		if !ok || cfg.TokenBudgetPerDay <= 0 {
			return nil
		}
		if s.limiter == nil {
			return &core.Error{Status: 503, Code: "provider_token_budget_unavailable", Message: "Provider token budget is temporarily unavailable"}
		}
		// 请求体字节数只是输入用量估算，不是供应商 tokenizer；真实 usage 会在 Finish 阶段结算。
		reserved := int64(len(st.Body))
		if providerBudgetHasOutputLimit(st.Path) {
			_, outputLimit, err := requestOutputTokenLimit(st.Body)
			if err != nil || outputLimit <= 0 {
				return &core.Error{Status: 400, Code: "provider_token_budget_invalid_request", Message: "Provider token budget requires a positive output token limit"}
			}
			reserved += outputLimit
		}
		if reserved <= 0 || reserved > math.MaxInt64/2 {
			return &core.Error{Status: 413, Code: "provider_token_budget_request_too_large", Message: "Request exceeds the Provider daily token budget"}
		}
		bucket, used, allowed, err := s.limiter.ReserveProviderTokens(ctx, providerID, cfg.TokenBudgetPerDay, reserved, time.Now().UTC())
		if err != nil {
			applog.Printf("[%s] Provider token budget reservation failed: provider=%s", st.ID, providerID)
			return &core.Error{Status: 503, Code: "provider_token_budget_unavailable", Message: "Provider token budget is temporarily unavailable"}
		}
		if !allowed {
			return &core.Error{Status: 429, Code: "provider_token_budget_exceeded", Message: fmt.Sprintf("Provider daily token budget is exhausted (used or reserved: %d)", used)}
		}
		s.storeProviderTokenBudgetReservation(st.ID, providerTokenBudgetReservation{
			ProviderID: providerID, Bucket: bucket, ReservedTokens: reserved,
		})
		return nil
	}
}

func (s *Server) storeProviderTokenBudgetReservation(requestID string, reservation providerTokenBudgetReservation) {
	if s == nil || strings.TrimSpace(requestID) == "" || reservation.ReservedTokens <= 0 {
		return
	}
	s.tokenBudgetMu.Lock()
	if s.tokenBudgetReservations == nil {
		s.tokenBudgetReservations = make(map[string]providerTokenBudgetReservation)
	}
	s.tokenBudgetReservations[requestID] = reservation
	s.tokenBudgetMu.Unlock()
}

func (s *Server) takeProviderTokenBudgetReservation(requestID string) (providerTokenBudgetReservation, bool) {
	if s == nil {
		return providerTokenBudgetReservation{}, false
	}
	s.tokenBudgetMu.Lock()
	reservation, ok := s.tokenBudgetReservations[requestID]
	delete(s.tokenBudgetReservations, requestID)
	s.tokenBudgetMu.Unlock()
	return reservation, ok
}

// finishProviderTokenBudget 只按真实供应商 usage 结算；无可信 usage 的已出网上游请求保守保留预占。
func (s *Server) finishProviderTokenBudget(requestID string, upstreamStarted bool, source string, input, output, total *int64) {
	reservation, ok := s.takeProviderTokenBudgetReservation(requestID)
	if !ok {
		return
	}
	actual := int64(0)
	settle := !upstreamStarted
	if upstreamStarted && source == "upstream_usage" {
		switch {
		case total != nil && *total >= 0:
			actual = *total
			settle = true
		case input != nil && output != nil && *input >= 0 && *output >= 0 && *input <= math.MaxInt64-*output:
			actual = *input + *output
			settle = true
		}
	}
	if !settle {
		// 上游可能已经计费；未知 usage 不释放预占，直到 Redis UTC 日桶过期。
		applog.Printf("[%s] Provider token usage unavailable; daily reservation retained: provider=%s reserved=%d", requestID, reservation.ProviderID, reservation.ReservedTokens)
		return
	}
	if s.limiter == nil {
		return
	}
	if err := s.limiter.SettleProviderTokens(context.Background(), reservation.ProviderID, reservation.Bucket, reservation.ReservedTokens, actual); err != nil {
		applog.Printf("[%s] Provider token budget settlement failed: provider=%s", requestID, reservation.ProviderID)
	}
}
