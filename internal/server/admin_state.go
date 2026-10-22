// server（admin_state）：单容器内统一管理台全局状态（上游可达、推理摘要、依赖等），各 HTTP 接口只读投影。
package server

import (
	"context"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/settings"
)

const adminStateTTL = 10 * time.Second

type serviceReachability struct {
	OK        bool      `json:"ok"`
	CheckedAt time.Time `json:"checked_at"`
	BaseURL   string    `json:"base_url,omitempty"`
}

type serviceInferenceSummary struct {
	Reachable   bool   `json:"reachable"`
	LoadedCount int    `json:"loaded_count"`
	BaseURL     string `json:"base_url,omitempty"`
	ActiveModel string `json:"active_model,omitempty"`
}

type adminStateSnap struct {
	at              time.Time
	upstream        settings.Upstream
	reachability    map[string]serviceReachability
	inference       map[string]serviceInferenceSummary
	activeByBackend map[string]string
	probes          map[string]interface{}
	health          string
	gatewayGguf     int
	gatewayDL       int
	redisOK         bool
	redisErr        string
}

func (s *Server) invalidateAdminState() {
	s.adminStateMu.Lock()
	s.adminState = nil
	s.adminStateMu.Unlock()
}

func (s *Server) invalidateAdminRuntimeCaches() {
	s.invalidateAdminState()
	s.invalidateUpstreamRuntimeSnapshotCache()
}

func (s *Server) adminStateEnsure(ctx context.Context, wantProbes bool) *adminStateSnap {
	s.adminStateMu.Lock()
	if s.adminState != nil && time.Since(s.adminState.at) < adminStateTTL {
		out := s.adminState
		s.adminStateMu.Unlock()
		if wantProbes && out.probes == nil {
			return s.rebuildAdminState(ctx, true)
		}
		return out
	}
	s.adminStateMu.Unlock()
	return s.rebuildAdminState(ctx, wantProbes)
}

func (s *Server) adminStateForceRefresh(ctx context.Context, wantProbes bool) *adminStateSnap {
	return s.rebuildAdminState(ctx, wantProbes)
}

func (s *Server) rebuildAdminState(ctx context.Context, wantProbes bool) *adminStateSnap {
	now := time.Now()
	out := &adminStateSnap{
		at:              now,
		upstream:        s.effectiveUpstream(),
		reachability:    map[string]serviceReachability{},
		inference:       map[string]serviceInferenceSummary{},
		activeByBackend: map[string]string{},
	}
	for _, be := range []string{"llamacpp", "ollama"} {
		base := strings.TrimSpace(s.baseURLForBackend(be))
		ok := false
		if base != "" {
			ok = s.probeBackendReachable(ctx, be)
		}
		out.reachability[be] = serviceReachability{OK: ok, CheckedAt: now, BaseURL: base}
		sum := serviceInferenceSummary{Reachable: ok, BaseURL: base}
		if ok {
			if client, _, err := s.adminClientForBackend(be); err == nil {
				hint := s.activeModelHintForBackend(be)
				snap, err := s.upstreamRuntimeSnapshotInMemory(ctx, be, client, hint)
				if err == nil && snap != nil {
					sum.LoadedCount = len(inferenceInMemoryModels(snap))
					sum.ActiveModel = activeModelIDFromSnapshot(snap, be)
					if sum.ActiveModel == "" {
						sum.ActiveModel = hint
					}
					out.activeByBackend[be] = sum.ActiveModel
				} else if hint != "" {
					out.activeByBackend[be] = hint
				}
			}
		}
		out.inference[be] = sum
	}
	if wantProbes {
		out.probes = s.upstreamProbes(ctx)
		if client := s.llamaClient(); client != nil {
			out.health = errString(client.Health(ctx))
		}
	}
	if ms, err := s.models.List(ctx); err == nil {
		out.gatewayGguf = len(ms)
		for _, m := range ms {
			if m.Status == "downloading" {
				out.gatewayDL++
			}
		}
	}
	if err := s.limiter.Ping(ctx); err != nil {
		out.redisOK = false
		out.redisErr = errString(err)
	} else {
		out.redisOK = true
		out.redisErr = "ok"
	}
	s.adminStateMu.Lock()
	s.adminState = out
	s.adminStateMu.Unlock()
	return out
}

// probeBackendsReachability 实时探测各后端（不读 admin 缓存），供首页 lite 轮询。
func reachabilityFromAdminSnap(snap *adminStateSnap) map[string]serviceReachability {
	if snap == nil || snap.reachability == nil {
		return map[string]serviceReachability{}
	}
	out := make(map[string]serviceReachability, len(snap.reachability))
	for be, r := range snap.reachability {
		out[be] = r
	}
	return out
}

func (s *Server) probeBackendsReachability(ctx context.Context) map[string]serviceReachability {
	now := time.Now()
	out := map[string]serviceReachability{}
	for _, be := range []string{"llamacpp", "ollama"} {
		base := strings.TrimSpace(s.baseURLForBackend(be))
		ok := base != "" && s.probeBackendReachable(ctx, be)
		out[be] = serviceReachability{OK: ok, CheckedAt: now, BaseURL: base}
	}
	return out
}

// inferenceSummaryWithReach 合并实时可达性，并在可达时刷新「在内存」计数（避免 admin 缓存与真实状态脱节）。
func (s *Server) inferenceSummaryWithReach(ctx context.Context, snap *adminStateSnap, reach map[string]serviceReachability) map[string]serviceInferenceSummary {
	inf := mergeInferenceSummaryReachability(snap.inference, reach)
	for be, r := range reach {
		if !r.OK {
			continue
		}
		client, _, err := s.adminClientForBackend(be)
		if err != nil {
			continue
		}
		hint := s.activeModelHintForBackend(be)
		memSnap, err := s.upstreamRuntimeSnapshotInMemory(ctx, be, client, hint)
		sum := inf[be]
		if err != nil || memSnap == nil {
			sum.LoadedCount = 0
			sum.ActiveModel = ""
			inf[be] = sum
			continue
		}
		sum.LoadedCount = len(inferenceInMemoryModels(memSnap))
		if id := activeModelIDFromSnapshot(memSnap, be); id != "" {
			sum.ActiveModel = id
		} else if hint != "" {
			sum.ActiveModel = hint
		}
		inf[be] = sum
	}
	return inf
}

func mergeInferenceSummaryReachability(inf map[string]serviceInferenceSummary, reach map[string]serviceReachability) map[string]serviceInferenceSummary {
	out := make(map[string]serviceInferenceSummary, len(inf)+len(reach))
	for be, sum := range inf {
		if r, ok := reach[be]; ok {
			sum.Reachable = r.OK
			if r.BaseURL != "" {
				sum.BaseURL = r.BaseURL
			}
			if !r.OK {
				sum.LoadedCount = 0
				sum.ActiveModel = ""
			}
		}
		out[be] = sum
	}
	for be, r := range reach {
		if _, ok := out[be]; !ok {
			out[be] = serviceInferenceSummary{Reachable: r.OK, BaseURL: r.BaseURL}
		}
	}
	return out
}

func (s *Server) backendReachable(ctx context.Context, backend string) bool {
	be := strings.ToLower(strings.TrimSpace(backend))
	if be != "llamacpp" && be != "ollama" {
		return false
	}
	return s.probeBackendReachable(ctx, be)
}

func (s *Server) activeUpstreamDependencyStatus(ctx context.Context) string {
	u := s.effectiveUpstream()
	if u.BaseURL == "" {
		return "未配置"
	}
	if s.backendReachable(ctx, u.Backend) {
		return "ok"
	}
	return "不可达"
}

func (s *Server) backendOverviewFromAdminState(ctx context.Context, wantProbes bool) backendOverviewSnapshot {
	snap := s.adminStateEnsure(ctx, wantProbes)
	if wantProbes && snap.probes == nil {
		snap = s.adminStateForceRefresh(ctx, true)
	}
	reach := map[string]bool{}
	for be, r := range snap.reachability {
		reach[be] = r.OK
	}
	ov := backendOverviewSnapshot{
		health:          snap.health,
		probes:          snap.probes,
		reachable:       reach,
		activeByBackend: snap.activeByBackend,
	}
	if !wantProbes {
		ov.probes = nil
		ov.health = ""
	}
	return ov
}
