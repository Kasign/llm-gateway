package server

import (
	"context"
	"time"
)

// listManagedProviderViewsWithRPMUsage 在原有安全 Provider 视图上附加当前分钟用量。
// Redis 只作观测用途：查询失败不应阻断 Provider 管理，也不会额外消耗 RPM 额度。
func (s *Server) listManagedProviderViewsWithRPMUsage(ctx context.Context) []map[string]interface{} {
	views := s.listManagedProviderViews()
	if len(views) == 0 {
		return views
	}

	limits := make(map[string]int, len(views))
	ids := make([]string, 0, len(views))
	for _, view := range views {
		id, _ := view["id"].(string)
		rpm, _ := view["requests_per_minute"].(int)
		view["requests_per_minute_used"] = 0
		view["requests_per_minute_remaining"] = 0
		view["requests_per_minute_reset_seconds"] = 0
		view["requests_per_minute_usage_available"] = rpm <= 0
		if rpm <= 0 || id == "" {
			continue
		}
		limits[id] = rpm
		ids = append(ids, id)
	}

	if len(ids) == 0 {
		return views
	}
	now := time.Now().UTC()
	nextWindow := now.Truncate(time.Minute).Add(time.Minute)
	resetSeconds := int((nextWindow.Sub(now) + time.Second - 1) / time.Second)
	for _, view := range views {
		if rpm, _ := view["requests_per_minute"].(int); rpm > 0 {
			view["requests_per_minute_reset_seconds"] = resetSeconds
		}
	}
	if s == nil || s.limiter == nil {
		return views
	}

	// 管理台列表不应等待 Redis 默认网络超时；预算执行路径仍使用真实请求上下文。
	usageCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	counts, err := s.limiter.ProviderRPMUsage(usageCtx, ids, now)
	if err != nil {
		return views
	}
	for _, view := range views {
		id, _ := view["id"].(string)
		rpm, limited := limits[id]
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
	return views
}
