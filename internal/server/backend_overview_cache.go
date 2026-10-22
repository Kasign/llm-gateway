// server（backend_overview_cache）：/api/admin/backend 探测视图，数据来自 admin_state。
package server

import (
	"context"
	"strings"

	"github.com/whl/llm-gateway/internal/llama"
)

type backendOverviewSnapshot struct {
	health          string
	probes          map[string]interface{}
	reachable       map[string]bool
	activeByBackend map[string]string
}

func upstreamProbeBlockOK(block map[string]interface{}) bool {
	if block == nil {
		return false
	}
	if v, ok := block["reachable"].(bool); ok {
		return v
	}
	base, _ := block["base_url"].(string)
	if strings.TrimSpace(base) == "" {
		return false
	}
	checks := probeChecksFromBlock(block)
	if len(checks) == 0 {
		return false
	}
	for _, m := range checks {
		if errMsg, _ := m["error"].(string); strings.TrimSpace(errMsg) != "" {
			return false
		}
		st := probeHTTPStatus(m["http_status"])
		if st < 200 || st >= 300 {
			return false
		}
	}
	return true
}

func probeChecksFromBlock(block map[string]interface{}) []map[string]interface{} {
	switch v := block["checks"].(type) {
	case []map[string]interface{}:
		return v
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(v))
		for _, c := range v {
			if m, ok := c.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func probeHTTPStatus(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	default:
		return 0
	}
}

func (s *Server) backendOverviewCached(ctx context.Context, wantProbes bool) backendOverviewSnapshot {
	return s.backendOverviewFromAdminState(ctx, wantProbes)
}

func activeModelIDFromSnapshot(snap *llama.RuntimeSnapshot, backend string) string {
	if snap == nil {
		return ""
	}
	if n := strings.TrimSpace(snap.ActiveModel); n != "" {
		return n
	}
	if len(snap.Loaded) > 0 {
		return strings.TrimSpace(snap.Loaded[0].ID)
	}
	for _, item := range snap.Catalog {
		if item.Loaded {
			return strings.TrimSpace(item.ID)
		}
	}
	if backend == "llamacpp" && len(snap.Catalog) == 1 {
		return strings.TrimSpace(snap.Catalog[0].ID)
	}
	return ""
}
