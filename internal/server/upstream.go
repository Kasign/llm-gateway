// server（upstream）：读写「当前生效的上游」、校验 URL 与真实服务类型是否一致。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/llama"
	"github.com/whl/llm-gateway/internal/model"
	"github.com/whl/llm-gateway/internal/settings"
)

func (s *Server) initUpstreamFromDB(ctx context.Context) {
	u, err := s.settings.GetUpstream(ctx)
	if err != nil {
		applog.Printf("load runtime upstream failed: %v", err)
		return
	}
	if u == nil {
		return
	}
	s.applyUpstream(*u, false)
	applog.Infof("runtime upstream loaded from database: backend=%s url=%s", u.Backend, s.effectiveUpstream().BaseURL)
}

func (s *Server) baseURLForBackend(backend string) string {
	return s.cfg.BaseURLForBackend(backend)
}

func (s *Server) newUpstreamClient(baseURL, backend string) *llama.Client {
	return llama.New(baseURL, time.Duration(s.cfg.Llama.RequestTimeout)*time.Second, backend, s.cfg.Llama.UpstreamAPIKey)
}

// probeBackendReachable 与切换上游时的校验一致：能连上且 ServiceKind 与配置后端一致。
func (s *Server) probeBackendReachable(ctx context.Context, backend string) bool {
	backend = strings.ToLower(strings.TrimSpace(backend))
	base := strings.TrimSpace(s.baseURLForBackend(backend))
	if base == "" || (backend != "llamacpp" && backend != "ollama") {
		return false
	}
	if backend == "ollama" {
		llamaURL := strings.TrimSpace(s.baseURLForBackend("llamacpp"))
		if llamaURL != "" && llamaURL == base {
			return false
		}
	}
	client := s.newUpstreamClient(base, backend)
	if err := client.Reachable(ctx); err != nil {
		return false
	}
	kind := strings.TrimSpace(client.ServiceKind(ctx))
	if kind == "" {
		return false
	}
	return kind == backend
}

func (s *Server) validateUpstreamBackend(ctx context.Context, wantBackend, baseURL string) error {
	if baseURL == "" {
		if wantBackend == "ollama" {
			return fmt.Errorf("未配置 OLLAMA_BASE_URL（Ollama 不能与 llama.cpp 共用地址，请填写独立地址，如 http://ollama:11434）")
		}
		return fmt.Errorf("未配置 LLAMA_CPP_BASE_URL")
	}
	if wantBackend == "ollama" {
		llamaURL := strings.TrimSpace(s.baseURLForBackend("llamacpp"))
		if llamaURL != "" && llamaURL == strings.TrimSpace(baseURL) {
			return fmt.Errorf("Ollama 不能与 llama.cpp 共用同一地址 (%s)；请为 OLLAMA_BASE_URL 配置独立 Ollama 服务", baseURL)
		}
	}
	client := s.newUpstreamClient(baseURL, wantBackend)
	if err := client.Reachable(ctx); err != nil {
		return err
	}
	kind := client.ServiceKind(ctx)
	if kind != "" && kind != wantBackend {
		if wantBackend == "ollama" {
			return fmt.Errorf("地址 %s 实际是 llama.cpp 服务；请设置 OLLAMA_BASE_URL 为 Ollama 容器地址（如 http://ollama:11434）", baseURL)
		}
		return fmt.Errorf("地址 %s 实际是 Ollama 服务；请检查 LLAMA_CPP_BASE_URL", baseURL)
	}
	return nil
}

func (s *Server) upstreamURLWarnings() []string {
	urls := s.configuredBackendURLs()
	var w []string
	if urls["ollama"] == "" {
		w = append(w, "未配置 OLLAMA_BASE_URL，切换到 Ollama 将不可用")
	}
	if urls["llamacpp"] == "" {
		w = append(w, "未配置 LLAMA_CPP_BASE_URL")
	}
	if urls["ollama"] != "" && urls["llamacpp"] != "" && urls["ollama"] == urls["llamacpp"] {
		w = append(w, "Ollama 与 llama.cpp 使用了相同地址，请分别配置")
	}
	if llama.UpstreamURLIsOllamaLibrary(urls["ollama"]) {
		w = append(w, "OLLAMA_BASE_URL 不能为 ollama.com 公网库；本机用 http://127.0.0.1:11434 等，浏览/下载用 download.ollama_library_url")
	}
	return w
}

func (s *Server) configuredBackendURLs() map[string]string {
	return map[string]string{
		"llamacpp": s.baseURLForBackend("llamacpp"),
		"ollama":   s.baseURLForBackend("ollama"),
	}
}

func (s *Server) resolveUpstream(u settings.Upstream) settings.Upstream {
	u = settings.Normalize(u)
	u.BaseURL = s.baseURLForBackend(u.Backend)
	u.ModelsDir = s.fileUpstream.ModelsDir
	if u.ModelsDir == "" {
		u.ModelsDir = s.cfg.Storage.ModelsDir
	}
	return u
}

func (s *Server) effectiveUpstream() settings.Upstream {
	s.upstreamMu.RLock()
	defer s.upstreamMu.RUnlock()
	return s.upstream
}

func (s *Server) llamaClient() *llama.Client {
	s.upstreamMu.RLock()
	defer s.upstreamMu.RUnlock()
	return s.llama
}

func (s *Server) applyUpstream(u settings.Upstream, resetRouter bool) {
	u = s.resolveUpstream(u)
	if u.BaseURL == "" {
		applog.Printf("apply upstream skipped: no base_url for backend=%q (check LLAMA_CPP_BASE_URL / OLLAMA_BASE_URL in .env)", u.Backend)
		return
	}
	client := s.newUpstreamClient(u.BaseURL, u.Backend)

	s.upstreamMu.Lock()
	s.upstream = u
	s.llama = client
	s.upstreamMu.Unlock()
	if resetRouter {
		s.mu.Lock()
		s.llamaRouter = nil
		s.mu.Unlock()
	}

	s.cfg.Llama.Backend = u.Backend
	s.cfg.Llama.BaseURL = u.BaseURL
	s.cfg.Llama.ModelName = u.ModelName
	s.cfg.Llama.ModelsDir = u.ModelsDir
	if s.cfg.Llama.ModelsDir == "" {
		s.cfg.Llama.ModelsDir = s.cfg.Storage.ModelsDir
	}
}

func (s *Server) backendAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getBackendAPI(w, r)
	case http.MethodPut:
		s.putBackendAPI(w, r)
	default:
		jsonError(w, 405, "method_not_allowed", "GET or PUT required")
	}
}

func (s *Server) getBackendAPI(w http.ResponseWriter, r *http.Request) {
	wantProbes := r.URL.Query().Get("refresh") == "1" || r.URL.Query().Get("probe") == "1"
	if wantProbes {
		s.invalidateAdminRuntimeCaches()
	}
	u := s.effectiveUpstream()
	overview := s.backendOverviewCached(r.Context(), wantProbes)
	proxyActive := overview.activeByBackend[u.Backend]
	if proxyActive == "" {
		proxyActive = s.activeModelHintForBackend(u.Backend)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"active":                      u,
		"defaults":                    s.fileUpstream,
		"configured_urls":             s.configuredBackendURLs(),
		"url_warnings":                s.upstreamURLWarnings(),
		"health":                      overview.health,
		"url_from_env_only":           true,
		"upstream_probes":             overview.probes,
		"inference_active_model_id":   proxyActive,
		"inference_active_by_backend": overview.activeByBackend,
	})
}

func (s *Server) upstreamProbes(ctx context.Context) map[string]interface{} {
	out := map[string]interface{}{}
	for _, backend := range []string{"llamacpp", "ollama"} {
		base := s.baseURLForBackend(backend)
		if base == "" {
			out[backend] = map[string]interface{}{"base_url": "", "checks": []interface{}{}}
			continue
		}
		client := s.newUpstreamClient(base, backend)
		paths := []string{"/health", "/models"}
		if backend == "ollama" {
			paths = []string{"/api/tags"}
		}
		checks := make([]map[string]interface{}, 0, len(paths))
		for _, p := range paths {
			st, sample, err := client.Probe(ctx, p)
			item := map[string]interface{}{"path": p, "http_status": st, "body_sample": sample}
			if err != nil {
				item["error"] = err.Error()
			}
			checks = append(checks, item)
		}
		block := map[string]interface{}{"base_url": base, "checks": checks}
		if err := client.Reachable(ctx); err != nil {
			block["reachable"] = false
			block["error"] = err.Error()
		} else {
			block["reachable"] = true
		}
		out[backend] = block
	}
	return out
}

func (s *Server) putBackendAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Backend   string `json:"backend"`
		ModelID   string `json:"model_id"`
		ModelName string `json:"model_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonError(w, 400, "invalid_body", err.Error())
		return
	}
	u := settings.Normalize(settings.Upstream{Backend: in.Backend, ModelName: strings.TrimSpace(in.ModelName)})
	if u.Backend != "llamacpp" && u.Backend != "ollama" {
		jsonError(w, 400, "invalid_upstream", "backend must be llamacpp or ollama")
		return
	}
	u = s.resolveUpstream(u)
	if u.BaseURL == "" {
		jsonError(w, 400, "upstream_url_missing", "configure LLAMA_CPP_BASE_URL and OLLAMA_BASE_URL in deploy/.env")
		return
	}
	modelID := strings.TrimSpace(in.ModelID)
	if modelID == "" {
		modelID = strings.TrimSpace(in.ModelName)
	}
	if err := s.SwitchProxyUpstream(r.Context(), u, modelID); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "实际是"):
			jsonError(w, 502, "upstream_mismatch", msg)
		case strings.Contains(msg, "未配置") || strings.Contains(msg, "Reachable") || strings.Contains(msg, "connection"):
			jsonError(w, 502, "upstream_unreachable", msg)
		default:
			jsonError(w, 502, "load_failed", msg)
		}
		return
	}
	u = s.effectiveUpstream()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":         true,
		"active":          s.effectiveUpstream(),
		"configured_urls": s.configuredBackendURLs(),
		"health":          errString(s.llamaClient().Health(r.Context())),
	})
}

func (s *Server) backendUnloadAllAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	if err := s.InferenceUnloadAll(r.Context(), "llamacpp"); err != nil {
		jsonError(w, 502, "unload_failed", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (s *Server) upstreamModelsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	client := s.llamaClient()
	b, err := client.List(r.Context())
	if err != nil {
		jsonError(w, 502, "upstream_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (s *Server) unloadModelUpstream(ctx context.Context, m *model.Model) error {
	if m == nil {
		return fmt.Errorf("model required")
	}
	_, err := s.InferenceUnload(ctx, "llamacpp", "", m.ModelKey, nil)
	return err
}
