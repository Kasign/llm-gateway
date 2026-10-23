// server（inference_ops）：对 llama.cpp / Ollama 的统一加载、卸载、列表与 Pull/Delete 逻辑。
// Load / Unload / List / UnloadAll / RefreshRegistry；当前上游 model id 见 ActiveModelID。
// Gateway 本地模型见 gateway_model_ops.go；代理上游切换见 upstream_ops.go。
// 管理写操作首选 POST /api/admin/model-ops；/runtime/select、/ollama/load 等为兼容路由。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/llama"
	"github.com/whl/llm-gateway/internal/model"
)

const inferenceUnloadStillLoadedWarning = "上游仍报告有模型在内存（含 sleeping）；Gateway 容器内存不会因此下降，请在推理主机/容器上查看占用"

// InferenceOpLogger receives human-readable steps (加载测试等). Nil 则不打日志。
type InferenceOpLogger func(level, msg string)

// InferenceLoadResult 加载成功后告诉前端实际在后端注册的名字。
// InferenceLoadResult is returned after a successful load into an inference backend.
type InferenceLoadResult struct {
	Backend  string `json:"backend"`
	LoadedAs string `json:"loaded_as"`
}

// InferenceUnloadResult 卸载后仍留在内存的模型列表与显存占用（仅释放 RAM/VRAM，不删磁盘）。
// InferenceUnloadResult is returned after unload (memory only, disk unchanged).
type InferenceUnloadResult struct {
	RemainingLoaded []map[string]interface{} `json:"remaining_loaded"`
	LoadedRAMBytes  int64                    `json:"loaded_ram_bytes"`
	LoadedVRAMBytes int64                    `json:"loaded_vram_bytes"`
	Warning         string                   `json:"warning,omitempty"`
}

func (r InferenceUnloadResult) toJSON() map[string]interface{} {
	out := map[string]interface{}{
		"success":           true,
		"remaining_loaded":  r.RemainingLoaded,
		"loaded_ram_bytes":  r.LoadedRAMBytes,
		"loaded_vram_bytes": r.LoadedVRAMBytes,
	}
	if r.Warning != "" {
		out["warning"] = r.Warning
	}
	return out
}

func normalizeInferenceBackend(backend string) (string, error) {
	b := strings.ToLower(strings.TrimSpace(backend))
	switch b {
	case "llamacpp", "ollama":
		return b, nil
	default:
		return "", fmt.Errorf("backend must be llamacpp or ollama")
	}
}

func (s *Server) gatewayModelByKey(ctx context.Context, modelKey string) (*model.Model, error) {
	modelKey = strings.TrimSpace(modelKey)
	if modelKey == "" {
		return nil, nil
	}
	return s.models.GetByKey(ctx, modelKey)
}

func (s *Server) resolveInferenceModelID(
	ctx context.Context,
	backend string,
	client *llama.Client,
	modelID string,
	gw *model.Model,
) (string, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID != "" {
		return modelID, nil
	}
	if gw == nil {
		return "", fmt.Errorf("model_id or model_key required")
	}
	name, err := s.resolveUpstreamModelNameForBackend(ctx, client, backend, s.activeModelHintForBackend(backend), gw)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("cannot resolve upstream model for %q", gw.ModelKey)
	}
	return name, nil
}

func (s *Server) inferenceLoadSucceeded(ctx context.Context, backend string, gw *model.Model, loadedAs string, log InferenceOpLogger) InferenceLoadResult {
	loadedAs = strings.TrimSpace(loadedAs)
	if loadedAs == "" {
		loadedAs = "?"
	}
	if log != nil {
		log("ok", "loaded_as="+loadedAs)
	}
	if gw != nil {
		s.persistUpstreamName(ctx, gw, backend, loadedAs)
	} else {
		s.persistUpstreamNameForID(ctx, backend, loadedAs)
	}
	if !shouldSuppressActiveModelAdoption(ctx) {
		s.maybeUpdateActiveUpstreamModel(ctx, backend, loadedAs)
	}
	if backend == "llamacpp" {
		s.syncGatewayFromLlamacpp(ctx)
	}
	s.invalidateAdminRuntimeCaches()
	return InferenceLoadResult{Backend: backend, LoadedAs: loadedAs}
}

func (s *Server) skipLoadIfUpstreamAlreadyLoaded(ctx context.Context, client *llama.Client, backend, modelID string, gw *model.Model, log InferenceOpLogger) (InferenceLoadResult, bool) {
	if ok, as := client.ModelLoadedInMemoryRefresh(ctx, modelID); ok {
		if log != nil {
			log("info", "上游快照：已在内存，跳过 POST load")
		}
		res := s.inferenceLoadSucceeded(ctx, backend, gw, as, log)
		applog.Infof("InferenceLoad skip (already in memory): backend=%s loaded_as=%q", backend, res.LoadedAs)
		return res, true
	}
	return InferenceLoadResult{}, false
}

// InferenceLoad 把模型加载进上游内存；chat 代理在 ensureProxyModelReady 里也会调用（auto_load 场景）。
// 管理员的 model-ops 接口同样走这里。不会删除磁盘上的模型文件。
func (s *Server) InferenceLoad(ctx context.Context, backend, modelID, modelKey string, log InferenceOpLogger) (InferenceLoadResult, error) {
	var zero InferenceLoadResult
	applog.Infof("InferenceLoad: backend=%s model_id=%q model_key=%q", backend, modelID, modelKey)
	b, err := normalizeInferenceBackend(backend)
	if err != nil {
		return zero, err
	}
	gw, err := s.gatewayModelByKey(ctx, modelKey)
	if err != nil {
		return zero, err
	}
	client, baseURL, err := s.adminClientForBackend(b)
	if err != nil {
		return zero, err
	}
	modelID, err = s.resolveInferenceModelID(ctx, b, client, modelID, gw)
	if err != nil {
		return zero, err
	}

	switch b {
	case "ollama":
		if res, skip := s.skipLoadIfUpstreamAlreadyLoaded(ctx, client, b, modelID, gw, log); skip {
			return res, nil
		}
		if log != nil {
			log("info", "POST /api/generate keep_alive=24h model="+modelID)
		}
		body, err := client.Load(ctx, modelID, s.ollamaKeepAlive(ctx))
		logUpstreamJSON(log, "Ollama load", body, err)
		if err != nil {
			return zero, err
		}
		if log != nil {
			log("ok", "Ollama 加载请求已发送")
		}
		applog.Infof("InferenceLoad ok: backend=ollama loaded_as=%q", modelID)
		return s.inferenceLoadSucceeded(ctx, b, gw, modelID, log), nil
	default:
		if res, skip := s.skipLoadIfUpstreamAlreadyLoaded(ctx, client, b, modelID, gw, log); skip {
			return res, nil
		}
		if gw != nil {
			for _, c := range s.llamaLoadCandidatesWithClient(ctx, client, gw) {
				if log != nil {
					log("info", "load 候选: "+c)
				}
			}
		} else {
			for _, c := range s.upstreamLoadCandidatesWithClient(ctx, client, modelID) {
				if log != nil {
					log("info", "load 候选: "+c)
				}
			}
		}
		loadedAs, err := s.loadLlamaCppUpstreamWithClient(ctx, client, gw, modelID, baseURL, false, log)
		if err != nil {
			return zero, err
		}
		if loadedAs == "" {
			loadedAs = modelID
		}
		applog.Infof("InferenceLoad ok: backend=llamacpp loaded_as=%q", loadedAs)
		return s.inferenceLoadSucceeded(ctx, b, gw, loadedAs, log), nil
	}
}

// InferenceUnload releases inference memory. Disk files are not removed.
func (s *Server) InferenceUnload(ctx context.Context, backend, modelID, modelKey string, log InferenceOpLogger) (InferenceUnloadResult, error) {
	var zero InferenceUnloadResult
	applog.Infof("InferenceUnload: backend=%s model_id=%q model_key=%q", backend, modelID, modelKey)
	b, err := normalizeInferenceBackend(backend)
	if err != nil {
		return zero, err
	}
	gw, err := s.gatewayModelByKey(ctx, modelKey)
	if err != nil {
		return zero, err
	}
	client, _, err := s.adminClientForBackend(b)
	if err != nil {
		return zero, err
	}

	switch b {
	case "ollama":
		modelID, err = s.resolveInferenceModelID(ctx, b, client, modelID, gw)
		if err != nil {
			return zero, err
		}
		if log != nil {
			log("info", "POST /api/generate keep_alive=0 model="+modelID)
		}
		body, err := client.Unload(ctx, modelID)
		logUpstreamJSON(log, "Ollama unload", body, err)
		if err != nil && !llama.UpstreamModelAbsent(err) {
			return zero, err
		}
		if log != nil {
			log("ok", "Ollama 卸载完成（或本未加载）")
		}
		s.invalidateAdminRuntimeCaches()
		applog.Infof("InferenceUnload ok: backend=ollama model=%q", modelID)
		return zero, nil
	default:
		if gw == nil && strings.TrimSpace(modelID) != "" {
			gw = s.gatewayModelForUpstreamID(ctx, modelID)
		}
		modelID = strings.TrimSpace(modelID)
		if gw != nil {
			if log != nil {
				log("info", "按 Gateway 记录卸载（多候选名，不删磁盘）model_key="+gw.ModelKey)
			}
			if err := s.unloadLlamacppGatewayModel(ctx, client, gw); err != nil {
				return zero, err
			}
		} else if modelID != "" {
			if log != nil {
				log("info", "POST /models/unload model="+modelID)
			}
			body, err := client.UnloadFromMemory(ctx, modelID)
			logUpstreamJSON(log, "llama.cpp unload", body, err)
			if err != nil {
				return zero, err
			}
		} else {
			return zero, fmt.Errorf("model_id or model_key required")
		}
		s.clearUpstreamModelNameIfMatch(ctx, modelID, gw)
		s.syncGatewayFromLlamacpp(ctx)
		s.invalidateAdminRuntimeCaches()
		if log != nil {
			log("ok", "llama.cpp 已从内存卸载（或本未加载）")
		}
		out, err := s.llamacppUnloadSnapshot(ctx, client)
		if err == nil {
			applog.Infof("InferenceUnload ok: backend=llamacpp remaining_in_memory=%d", len(out.RemainingLoaded))
		}
		return out, err
	}
}

func (s *Server) unloadLlamacppGatewayModel(ctx context.Context, client *llama.Client, gw *model.Model) error {
	if gw == nil {
		return fmt.Errorf("gateway model required")
	}
	try := func(name string) (ok bool, err error) {
		name = strings.TrimSpace(name)
		if name == "" {
			return false, nil
		}
		if _, err := client.UnloadFromMemory(ctx, name); err != nil {
			if llama.UpstreamModelAbsent(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	var names []string
	names = append(names, s.llamaLoadCandidatesWithClient(ctx, client, gw)...)
	if u := strings.TrimSpace(s.effectiveUpstream().ModelName); u != "" && s.modelMatchesUpstreamID(gw, u) {
		names = append(names, u)
	}
	if name, err := s.resolveUpstreamModelNameForBackend(ctx, client, "llamacpp", s.activeModelHintForBackend("llamacpp"), gw); err == nil && name != "" {
		names = append(names, name)
	}
	seen := map[string]struct{}{}
	var lastHard error
	for _, name := range names {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		ok, err := try(name)
		if err != nil {
			lastHard = err
			continue
		}
		if ok {
			return nil
		}
	}
	if lastHard != nil {
		return lastHard
	}
	return client.UnloadAll(ctx)
}

func (s *Server) clearUpstreamModelNameIfMatch(ctx context.Context, modelID string, gw *model.Model) {
	u := s.effectiveUpstream()
	if u.Backend != "llamacpp" {
		return
	}
	if u.ModelName == "" {
		return
	}
	if gw != nil && s.modelMatchesUpstreamID(gw, u.ModelName) {
		u.ModelName = ""
		_ = s.settings.SaveUpstream(ctx, u)
		s.applyUpstream(u, false)
		return
	}
	if modelID != "" && llama.ModelIDMatch(u.ModelName, modelID) {
		u.ModelName = ""
		_ = s.settings.SaveUpstream(ctx, u)
		s.applyUpstream(u, false)
	}
}

// InferenceListResult is upstream catalog + load state (admin client).
type InferenceListResult struct {
	Snapshot        *llama.RuntimeSnapshot
	BaseURL         string
	CatalogSource   string // upstream | gateway
	UpstreamWarning string
	GatewayModels   []model.Model
}

// activeModelHintForBackend is the persisted proxy model name (settings), used only to mark "active" in catalog UI — not load state.
func (s *Server) activeModelHintForBackend(backend string) string {
	u := s.effectiveUpstream()
	if strings.EqualFold(u.Backend, backend) {
		return strings.TrimSpace(u.ModelName)
	}
	return ""
}

// resolveInferenceActiveModelID reads upstream snapshot only (GET /models, Ollama /api/ps).
func (s *Server) resolveInferenceActiveModelID(ctx context.Context, backend string) string {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend == "" {
		backend = s.effectiveUpstream().Backend
	}
	if backend != "llamacpp" && backend != "ollama" {
		return ""
	}
	if strings.TrimSpace(s.baseURLForBackend(backend)) == "" {
		return ""
	}
	list, err := s.InferenceList(ctx, backend, false)
	if err != nil || list.Snapshot == nil {
		return ""
	}
	return activeModelIDFromSnapshot(list.Snapshot, backend)
}

// ActiveModelID is the single resolver for proxy/benchmark/admin: snapshot (in-memory) wins over persisted ModelName.
func (s *Server) ActiveModelID(ctx context.Context, backend string) string {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend == "" {
		backend = s.effectiveUpstream().Backend
	}
	if strings.TrimSpace(s.baseURLForBackend(backend)) != "" {
		list, err := s.InferenceList(ctx, backend, false)
		if err == nil && list.Snapshot != nil {
			if snapID := activeModelIDFromSnapshot(list.Snapshot, backend); snapID != "" {
				return snapID
			}
		}
	}
	return s.activeModelHintForBackend(backend)
}

// ActiveModelIDForProxy returns ActiveModelID for the backend that receives /v1 traffic.
func (s *Server) ActiveModelIDForProxy(ctx context.Context) string {
	u := s.effectiveUpstream()
	return s.ActiveModelID(ctx, u.Backend)
}

// InferenceList returns runtime catalog for backend (llamacpp admin / ollama admin).
func (s *Server) InferenceList(ctx context.Context, backend string, verifyServiceKind bool) (InferenceListResult, error) {
	var zero InferenceListResult
	b, err := normalizeInferenceBackend(backend)
	if err != nil {
		return zero, err
	}
	client, baseURL, err := s.adminClientForBackend(b)
	if err != nil {
		return zero, err
	}
	if b == "ollama" {
		verifyServiceKind = true
	}
	if verifyServiceKind {
		if kind := client.ServiceKind(ctx); kind != "" && kind != b {
			if b == "ollama" {
				return zero, fmt.Errorf("地址 %s 实际是 llama.cpp；请设置 OLLAMA_BASE_URL 为 Ollama 地址", baseURL)
			}
			return zero, fmt.Errorf("地址 %s 实际是 Ollama；请检查 LLAMA_CPP_BASE_URL", baseURL)
		}
	}
	active := s.activeModelHintForBackend(b)
	var ms []model.Model
	if b != "ollama" {
		ms, _ = s.models.List(ctx)
	}
	snap, err := s.upstreamRuntimeSnapshot(ctx, b, client, active)
	out := InferenceListResult{BaseURL: baseURL, CatalogSource: "upstream"}
	if err != nil {
		if b == "llamacpp" {
			fallback := s.catalogFromGatewayModelsWith(ms)
			if len(fallback) > 0 {
				snap = &llama.RuntimeSnapshot{Backend: "llamacpp", Catalog: fallback}
				out.CatalogSource = "gateway"
				out.UpstreamWarning = err.Error()
			} else {
				return zero, err
			}
		} else {
			return zero, err
		}
	}
	if snap == nil {
		return zero, fmt.Errorf("upstream %s returned empty snapshot", b)
	}
	s.enrichCatalogDiskFromGateway(snap, ms)
	if snap.ActiveModel == "" {
		snap.ActiveModel = active
	}
	out.Snapshot = snap
	out.GatewayModels = ms
	return out, nil
}

// InferenceUnloadAll releases all models in memory on the given backend.
func (s *Server) InferenceUnloadAll(ctx context.Context, backend string) error {
	b, err := normalizeInferenceBackend(backend)
	if err != nil {
		return err
	}
	client, _, err := s.adminClientForBackend(b)
	if err != nil {
		return err
	}
	if err := client.UnloadAll(ctx); err != nil {
		return err
	}
	if b == "llamacpp" {
		ms, _ := s.models.List(ctx)
		for _, m := range ms {
			if m.Status == "loaded" {
				_ = s.models.SetStatus(ctx, m.ModelKey, "unloaded")
			}
		}
		s.syncGatewayFromLlamacpp(ctx)
	}
	s.invalidateAdminRuntimeCaches()
	return nil
}

// InferenceRefreshRegistry rescans upstream model registry (e.g. GET /models?reload=1).
func (s *Server) InferenceRefreshRegistry(ctx context.Context, backend string) (json.RawMessage, error) {
	b, err := normalizeInferenceBackend(backend)
	if err != nil {
		return nil, err
	}
	client, _, err := s.adminClientForBackend(b)
	if err != nil {
		return nil, err
	}
	return client.Refresh(ctx)
}

// OllamaPullModel pulls a model tag into Ollama (disk), blocking until complete.
func (s *Server) OllamaPullModel(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name required")
	}
	client, _, err := s.adminClientForBackend("ollama")
	if err != nil {
		return err
	}
	return client.OllamaPull(ctx, name, nil)
}

// OllamaDeleteModel removes a model from Ollama storage (not the same as unload).
func (s *Server) OllamaDeleteModel(ctx context.Context, name string, log InferenceOpLogger) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name required")
	}
	client, _, err := s.adminClientForBackend("ollama")
	if err != nil {
		return err
	}
	_, _ = s.InferenceUnload(ctx, "ollama", name, "", log)
	body, err := client.OllamaDeleteModel(ctx, name)
	logUpstreamJSON(log, "Ollama delete", body, err)
	if err != nil {
		return err
	}
	s.invalidateAdminRuntimeCaches()
	return nil
}

func (s *Server) llamacppUnloadSnapshot(ctx context.Context, client *llama.Client) (InferenceUnloadResult, error) {
	remaining, ram, vram := inferenceStillLoaded(ctx, client)
	out := InferenceUnloadResult{
		RemainingLoaded: remaining,
		LoadedRAMBytes:  ram,
		LoadedVRAMBytes: vram,
	}
	if len(remaining) > 0 {
		out.Warning = inferenceUnloadStillLoadedWarning
	}
	return out, nil
}
