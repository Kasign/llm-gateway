package server

import (
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
)

func (s *Server) bootstrapAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	ctx := r.Context()
	snap := s.adminStateEnsure(ctx, false)
	reach := reachabilityFromAdminSnap(snap)
	inf := s.inferenceSummaryWithReach(ctx, snap, reach)
	memSnap, diskSnap := s.systemHostStatsCached()
	u := snap.upstream
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"version": 1,
		"upstream": map[string]interface{}{
			"backend":    u.Backend,
			"model_name": u.ModelName,
			"base_url":   u.BaseURL,
		},
		"configured_urls":   s.configuredBackendURLs(),
		"reachability":      reach,
		"inference_summary": inf,
		"dependencies": map[string]interface{}{
			"upstream": upstreamDependencyFromReach(reach, u.Backend),
			"redis":    snap.redisErr,
		},
		"gateway_counts": map[string]interface{}{
			"gguf_total":   snap.gatewayGguf,
			"downloading":  snap.gatewayDL,
		},
		"host": s.hostMetricsJSON(memSnap, diskSnap),
	})
}

func (s *Server) bootstrapLiteAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	snap := s.adminStateEnsure(ctx, false)
	reach := s.probeBackendsReachability(ctx)
	inf := s.inferenceSummaryWithReach(ctx, snap, reach)
	memSnap, diskSnap := s.systemHostStatsCached()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"version":           1,
		"reachability":      reach,
		"inference_summary": inf,
		"dependencies": map[string]interface{}{
			"upstream": upstreamDependencyFromReach(reach, snap.upstream.Backend),
			"redis":    snap.redisErr,
		},
		"host": s.hostMetricsJSON(memSnap, diskSnap),
	})
}

func upstreamDependencyFromReach(reach map[string]serviceReachability, backend string) string {
	be := strings.ToLower(strings.TrimSpace(backend))
	if be == "" {
		return "未配置"
	}
	if r, ok := reach[be]; ok && r.OK {
		return "ok"
	}
	return "不可达"
}

func (s *Server) hostMetricsJSON(mem containerMemory, disk diskUsage) map[string]interface{} {
	memMap := map[string]interface{}{
		"total":     mem.Limit,
		"used":      mem.Used,
		"available": mem.Limit - mem.Used,
		"source":    mem.Source,
	}
	if mem.Limit < mem.Used {
		memMap["available"] = uint64(0)
	}
	return map[string]interface{}{
		"cpu":                  runtime.NumCPU(),
		"cpu_usage_percent":    s.cpuUsagePercent(),
		"memory":               memMap,
		"memory_usage_percent": mem.UsagePct,
		"disk": map[string]interface{}{
			"total":     disk.Total,
			"used":      disk.Used,
			"available": disk.Available,
		},
	}
}

