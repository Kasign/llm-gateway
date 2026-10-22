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
	wantBackend = strings.ToLower(strings.TrimSpace(wantBackend))
	if remote, ok := s.remoteProviderConfig(wantBackend); ok {
		if strings.TrimRight(strings.TrimSpace(baseURL), "/") != strings.TrimRight(remote.BaseURL, "/") {
			return fmt.Errorf("remote Provider %q URL must come from deploy/config.yaml", wantBackend)
		}
		if s.providers == nil {
			return fmt.Errorf("remote Provider registry is not initialized")
		}
		if _, err := s.providers.Resolve(wantBackend); err != nil {
			return fmt.Errorf("remote Provider %q is unavailable: %w", wantBackend, err)
		}
		return nil
	}
	if !isLocalInferenceBackend(wantBackend) {
		return fmt.Errorf("Provider %q is not configured", wantBackend)
	}
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
	out := map[string]string{
		"llamacpp": s.baseURLForBackend("llamacpp"),
		"ollama":   s.baseURLForBackend("ollama"),
	}
	if s != nil && s.cfg != nil {
		for _, item := range s.cfg.Providers.OpenAICompatible {
			if item.Enabled {
				out[item.ID] = item.BaseURL
			}
		}
	}
	return out
}

func (s *Server) resolveUpstream(u settings.Upstream) settings.Upstream {
	u = settings.Normalize(u)
	if isLocalInferenceBackend(u.Backend) {
		u.BaseURL = s.baseURLForBackend(u.Backend)
		u.ModelsDir = s.fileUpstream.ModelsDir
		if u.ModelsDir == "" {
			u.ModelsDir = s.cfg.Storage.ModelsDir
		}
		return u
	}
	u.ModelsDir = ""
	if remote, ok := s.remoteProviderConfig(u.Backend); ok {
		u.BaseURL = remote.BaseURL
		if u.ModelName == "" {
			u.ModelName = remote.DefaultModel
		}
	} else {
		u.BaseURL = ""
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
		applog.Printf("apply upstream skipped: Provider URL missing for backend=%q", u.Backend)
		return
	}
	localBackend := isLocalInferenceBackend(u.Backend)
	var client *llama.Client
	if localBackend {
		client = s.newUpstreamClient(u.BaseURL, u.Backend)
	}

	s.upstreamMu.Lock()
	s.upstream = u
	// 远程 Provider 不伪装成 llama.Client，保留本地控制客户端供管理接口使用。
	if localBackend {
		s.llama = client
	}
	s.upstreamMu.Unlock()
	if resetRouter {
		s.mu.Lock()
		s.llamaRouter = nil
		s.mu.Unlock()
	}
	if localBackend {
		s.cfg.Llama.Backend = u.Backend
		s.cfg.Llama.BaseURL = u.BaseURL
		s.cfg.Llama.ModelName = u.ModelName
		s.cfg.Llama.ModelsDir = u.ModelsDir
		if s.cfg.Llama.ModelsDir == "" {
			s.cfg.Llama.ModelsDir = s.cfg.Storage.ModelsDir
		}
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
	health := overview.health
	if _, remote := s.remoteProviderConfig(u.Backend); remote {
		health = "remote Provider configured; connectivity is checked on inference"
	}
	probes := overview.probes
	if probes == nil {
		probes = map[string]interface{}{}
	}
	for _, item := range s.cfg.Providers.OpenAICompatible {
		if item.Enabled {
			probes[item.ID] = map[string]interface{}{
				"base_url": item.BaseURL, "configured_remote": true, "default_model": item.DefaultModel,
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"active":                      u,
		"defaults":                    s.fileUpstream,
		"configured_urls":             s.configuredBackendURLs(),
		"available_backends":          s.availableBackends(),
		"url_warnings":                s.upstreamURLWarnings(),
		"health":                      health,
		"url_from_env_only":           true,
		"upstream_probes":             probes,
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
	if !s.isConfiguredProvider(u.Backend) {
		jsonError(w, 400, "invalid_upstream", "Provider ID is not configured")
		return
	}
	resolved := s.resolveUpstream(u)
	if resolved.BaseURL == "" {
		jsonError(w, 400, "upstream_url_missing", "Provider URL is missing from deploy/config.yaml or deploy/.env")
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
	active := s.effectiveUpstream()
	health := "configured"
	if isLocalInferenceBackend(active.Backend) {
		health = errString(s.llamaClient().Health(r.Context()))
	} else {
		health = "remote Provider configured; connectivity is checked on inference"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true, "active": active, "configured_urls": s.configuredBackendURLs(),
		"available_backends": s.availableBackends(), "health": health,
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
	backend := s.effectiveUpstream().Backend
	if !isLocalInferenceBackend(backend) {
		jsonError(w, 400, "unsupported_operation", "remote Provider has no local model unload operation")
		return
	}
	if err := s.InferenceUnloadAll(r.Context(), backend); err != nil {
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
	active := s.effectiveUpstream()
	if _, remote := s.remoteProviderConfig(active.Backend); remote {
		data := []map[string]interface{}{}
		if name := strings.TrimSpace(active.ModelName); name != "" {
			data = append(data, map[string]interface{}{"id": name, "object": "model", "owned_by": active.Backend})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": data})
		return
	}
	client := s.llamaClient()
	body, err := client.List(r.Context())
	if err != nil {
		jsonError(w, 502, "upstream_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func (s *Server) unloadModelUpstream(ctx context.Context, m *model.Model) error {
	if m == nil {
		return fmt.Errorf("model required")
	}
	_, err := s.InferenceUnload(ctx, "llamacpp", "", m.ModelKey, nil)
	return err
}
