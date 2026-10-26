package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/store"
)

func requestStatsRange(raw string) (string, time.Duration, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "24h":
		return "24h", 24 * time.Hour, true
	case "7d":
		return "7d", 7 * 24 * time.Hour, true
	case "30d":
		return "30d", 30 * 24 * time.Hour, true
	default:
		return "", 0, false
	}
}

// requestsStatsAPI 返回时间窗口内的聚合调用量、延迟分布和按来源分类的 Token 汇总。
func (s *Server) requestsStatsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	rangeName, window, ok := requestStatsRange(r.URL.Query().Get("range"))
	if !ok {
		jsonError(w, http.StatusBadRequest, "invalid_range", "range must be 24h, 7d, or 30d")
		return
	}
	to := time.Now()
	from := to.Add(-window)
	providerID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	stats, err := s.logs.AggregateRequestStats(r.Context(), from, to, providerID)
	if err != nil {
		writeRequestStatsDatabaseError(w, err)
		return
	}
	stats.Range = rangeName
	stats.ProviderID = providerID
	if stats.Tokens.Note == "" {
		stats.Tokens.Note = store.RequestStatsTokens{}.Note
	}
	writeJSON(w, stats)
}

// writeRequestStatsDatabaseError 保留服务端诊断信息，但不把数据库结构/驱动错误直接返回给 API 调用方。
func writeRequestStatsDatabaseError(w http.ResponseWriter, err error) {
	applog.Printf("request statistics query failed: %v", err)
	jsonError(w, http.StatusInternalServerError, "database_error", "查询请求统计失败")
}
