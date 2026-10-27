// server（runtime）：管理台用的运行时预览、卸载/选中模型等 HTTP 接口（需管理员密钥）。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/llama"
	"github.com/whl/llm-gateway/internal/model"
)

func (s *Server) runtimePreviewAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	backend := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("backend")))
	if backend != "llamacpp" && backend != "ollama" {
		jsonError(w, 400, "invalid_backend", "backend must be llamacpp or ollama")
		return
	}
	if s.baseURLForBackend(backend) == "" {
		jsonError(w, 400, "upstream_url_missing", "configure LLAMA_CPP_BASE_URL / OLLAMA_BASE_URL in deploy/.env")
		return
	}
	list, err := s.InferenceList(r.Context(), backend, true)
	if err != nil {
		code, tag := 502, "runtime_error"
		if strings.Contains(err.Error(), "实际是") {
			tag = "upstream_mismatch"
		}
		jsonError(w, code, tag, err.Error())
		return
	}
	snap := list.Snapshot
	if snap == nil {
		jsonError(w, 502, "runtime_error", "empty upstream snapshot")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	out := map[string]interface{}{
		"backend":        backend,
		"base_url":       list.BaseURL,
		"active_model":   snap.ActiveModel,
		"catalog":        snap.Catalog,
		"loaded":         snap.Loaded,
		"catalog_source": list.CatalogSource,
	}
	if list.UpstreamWarning != "" {
		out["upstream_warning"] = list.UpstreamWarning
	}
	json.NewEncoder(w).Encode(out)
}

// runtimeRefreshAPI 显式刷新 llama.cpp Router 注册目录；普通页面轮询不触发重扫。
func (s *Server) runtimeRefreshAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) { return }
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if _, err := s.InferenceRefreshRegistry(r.Context(), "llamacpp"); err != nil {
		jsonError(w, http.StatusBadGateway, "runtime_refresh_failed", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "backend": "llamacpp"})
}

func (s *Server) runtimeAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	snap, err := s.buildAdminRuntimeOverview(r.Context())
	if err != nil {
		jsonError(w, 502, "runtime_error", err.Error())
		return
	}
	json.NewEncoder(w).Encode(snap)
}

func (s *Server) buildAdminRuntimeOverview(ctx context.Context) (map[string]interface{}, error) {
	u := s.effectiveUpstream()
	switch {
	case u.Backend == "ollama":
		return s.buildOllamaRuntimeOverview(ctx)
	case u.Backend == "llamacpp":
		return s.buildLlamacppRuntimeSnapshot(ctx)
	case s.isConfiguredProvider(u.Backend):
		// 远程 Provider 的运行时由供应商管理；不能回退展示本地 llama.cpp/Ollama 状态。
		return map[string]interface{}{
			"backend":                u.Backend,
			"base_url":               u.BaseURL,
			"active_model":           u.ModelName,
			"remote_provider":        true,
			"local_model_management": false,
			"loaded":                 []interface{}{},
			"catalog":                []interface{}{},
			"unload_note":            "远程 Provider 不支持本地模型加载/卸载；模型生命周期由远程服务管理",
		}, nil
	default:
		return nil, fmt.Errorf("unknown active Provider %q", u.Backend)
	}
}

// localRuntimeBackend 保护只能操作本地模型的旧运行时接口，拒绝把远程 Provider ID 当成本地后端。
func localRuntimeBackend(id string) (string, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if !isLocalInferenceBackend(id) {
		return "", fmt.Errorf("当前后端 %q 不支持本地模型加载/卸载操作", id)
	}
	return id, nil
}



func (s *Server) runtimeUnloadAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	setInferenceLegacyDeprecation(w)
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	backend, err := localRuntimeBackend(s.effectiveUpstream().Backend)
	if err != nil {
		jsonError(w, 400, "unsupported_operation", err.Error())
		return
	}
	var in struct {
		ModelID  string `json:"model_id"`
		ModelKey string `json:"model_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonError(w, 400, "invalid_body", err.Error())
		return
	}
	result, err := s.InferenceUnload(r.Context(), backend, in.ModelID, in.ModelKey, nil)
	if err != nil {
		jsonError(w, 502, "unload_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result.toJSON())
}

func (s *Server) runtimeSelectAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	setInferenceLegacyDeprecation(w)
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	backend, err := localRuntimeBackend(s.effectiveUpstream().Backend)
	if err != nil {
		jsonError(w, 400, "unsupported_operation", err.Error())
		return
	}
	var in struct {
		ModelID  string `json:"model_id"`
		ModelKey string `json:"model_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonError(w, 400, "invalid_body", err.Error())
		return
	}
	res, err := s.InferenceLoad(r.Context(), backend, in.ModelID, in.ModelKey, nil)
	if err != nil {
		if strings.Contains(err.Error(), "model_id or model_key") {
			jsonError(w, 400, "model_required", err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "cannot resolve") {
			jsonError(w, 502, "model_resolve_failed", err.Error())
			return
		}
		jsonError(w, 502, "load_failed", err.Error())
		return
	}
	snap, _ := s.buildAdminRuntimeOverview(r.Context())
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":      true,
		"active_model": res.LoadedAs,
		"runtime":      snap,
	})
	applog.Infof("runtime select: model_id=%q backend=%s", res.LoadedAs, backend)
}

func llamaLoadedModelForHint(loaded []llama.CatalogItem, configured string) string {
	configured = strings.TrimSpace(configured)
	for _, item := range loaded {
		if configured != "" && llama.ModelIDMatch(item.ID, configured) { return item.ID }
	}
	if len(loaded) == 1 { return strings.TrimSpace(loaded[0].ID) }
	return ""
}

func (s *Server) buildLlamacppRuntimeSnapshot(ctx context.Context) (map[string]interface{}, error) {
	list, err := s.InferenceList(ctx, "llamacpp", false)
	if err != nil { return nil, err }
	snap := list.Snapshot
	if snap == nil { return nil, fmt.Errorf("empty llamacpp upstream snapshot") }
	baseURL := list.BaseURL
	client, _, err := s.llamacppAdminClient()
	if err != nil { return nil, err }

	ms := list.GatewayModels
	configuredModel := strings.TrimSpace(s.activeModelHintForBackend("llamacpp"))
	catalogWarning := s.mergeLlamacppCatalog(snap, ms, list.CatalogSource == "upstream")
	actualLoadedModel := llamaLoadedModelForHint(snap.Loaded, configuredModel)

	var gatewayModelKey string
	if actualLoadedModel != "" {
		for _, m := range ms {
			if s.modelMatchesUpstreamID(&m, actualLoadedModel) { gatewayModelKey = m.ModelKey; break }
		}
	}
	if gatewayModelKey == "" && configuredModel != "" {
		if gw := s.gatewayModelForUpstreamID(ctx, configuredModel); gw != nil { gatewayModelKey = gw.ModelKey }
	}

	var activeItem *llama.CatalogItem
	for i := range snap.Loaded {
		if actualLoadedModel != "" && llama.ModelIDMatch(snap.Loaded[i].ID, actualLoadedModel) {
			activeItem = &snap.Loaded[i]
			break
		}
	}
	var diskEstimate int64
	if activeItem != nil { diskEstimate = activeItem.DiskBytes }
	hotUnload, _ := client.HasRouter(ctx)
	out := map[string]interface{}{
		"backend": "llamacpp", "base_url": baseURL,
		// active_model 只表示实际上游已加载模型；Gateway 默认路由目标单独返回。
		"active_model": actualLoadedModel, "gateway_model": configuredModel, "gateway_model_key": gatewayModelKey,
		"loaded": snap.Loaded, "catalog": snap.Catalog, "catalog_source": list.CatalogSource,
		"loaded_vram_bytes": snap.LoadedVRAM, "loaded_ram_bytes": snap.LoadedRAM,
		"disk_bytes_est": diskEstimate, "hot_unload_supported": hotUnload,
	}
	if list.UpstreamWarning != "" { out["upstream_warning"] = list.UpstreamWarning }
	if catalogWarning != "" { out["catalog_warning"] = catalogWarning }
	if !hotUnload {
		out["unload_note"] = "当前 llama.cpp 为单模型模式：本页仍显示共享磁盘中的 GGUF，但不能在进程内切换其它模型；需要 Router 模式和 --models-dir 才能动态加载"
	} else {
		out["unload_note"] = "卸载仅释放推理内存/显存，不删除磁盘上的 GGUF"
	}
	return out, nil
}

func (s *Server) buildOllamaRuntimeOverview(ctx context.Context) (map[string]interface{}, error) {
	list, err := s.InferenceList(ctx, "ollama", false)
	if err != nil {
		return nil, err
	}
	snap := list.Snapshot
	if snap == nil {
		return nil, fmt.Errorf("empty ollama upstream snapshot")
	}
	activeModel := strings.TrimSpace(snap.ActiveModel)
	if activeModel == "" {
		activeModel = activeModelIDFromSnapshot(snap, "ollama")
	}
	if activeModel == "" {
		activeModel = s.activeModelHintForBackend("ollama")
	}

	var activeItem *llama.CatalogItem
	for i := range snap.Loaded {
		if snap.Loaded[i].ID == activeModel || snap.Loaded[i].Active {
			activeItem = &snap.Loaded[i]
			break
		}
	}
	if activeItem == nil && len(snap.Loaded) > 0 {
		activeItem = &snap.Loaded[0]
	}

	var diskEstimate int64
	if activeItem != nil {
		diskEstimate = activeItem.DiskBytes
		if cat := catalogItemMatchingID(snap.Catalog, activeItem.ID); cat != nil {
			if diskEstimate == 0 {
				diskEstimate = cat.DiskBytes
			}
			if activeItem.Quantization == "" {
				activeItem.Quantization = cat.Quantization
			}
			if activeItem.ParameterSize == "" {
				activeItem.ParameterSize = cat.ParameterSize
			}
		}
	}

	return map[string]interface{}{
		"backend":              "ollama",
		"base_url":             list.BaseURL,
		"active_model":         activeModel,
		"loaded":               snap.Loaded,
		"catalog":              snap.Catalog,
		"loaded_vram_bytes":    snap.LoadedVRAM,
		"loaded_ram_bytes":     snap.LoadedRAM,
		"disk_bytes_est":       diskEstimate,
		"hot_unload_supported": true,
		"unload_note":          "在 Ollama 模型表中「卸载」仅释放内存；「删除」才会从磁盘移除",
	}, nil
}

func catalogItemMatchingID(catalog []llama.CatalogItem, id string) *llama.CatalogItem {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	for i := range catalog {
		c := &catalog[i]
		if c.ID == id || strings.HasPrefix(c.ID, id+":") || strings.HasPrefix(id, c.ID+":") {
			return c
		}
	}
	return nil
}

func (s *Server) buildRuntimeSnapshot(ctx context.Context) (map[string]interface{}, error) {
	u := s.effectiveUpstream()
	list, err := s.InferenceList(ctx, u.Backend, false)
	if err != nil {
		return nil, err
	}
	snap := list.Snapshot
	if snap == nil {
		return nil, fmt.Errorf("empty upstream snapshot")
	}
	activeModel := strings.TrimSpace(snap.ActiveModel)
	if activeModel == "" {
		activeModel = activeModelIDFromSnapshot(snap, u.Backend)
	}
	if activeModel == "" {
		activeModel = s.activeModelHintForBackend(u.Backend)
	}
	return map[string]interface{}{
		"backend":      snap.Backend,
		"active_model": activeModel,
		"loaded":       snap.Loaded,
		"catalog":      snap.Catalog,
		"upstream":     u,
	}, nil
}

func (s *Server) catalogFromGatewayModels(ctx context.Context) []llama.CatalogItem {
	ms, err := s.models.List(ctx)
	if err != nil {
		return nil
	}
	return s.catalogFromGatewayModelsWith(ms)
}

func (s *Server) catalogFromGatewayModelsWith(ms []model.Model) []llama.CatalogItem {
	if len(ms) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []llama.CatalogItem
	for _, m := range ms {
		id := strings.TrimSpace(strings.TrimSuffix(m.LlamaModelName, ".gguf"))
		if id == "" {
			id = strings.TrimSuffix(m.Filename, ".gguf")
		}
		if id == "" {
			id = m.ModelKey
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, llama.CatalogItem{
			ID:         id,
			DiskBytes:  model.CachedDiskBytes(&m),
			MemoryNote: "Gateway 已安装模型（上游 HTTP 列表失败时的备选）",
		})
	}
	return out
}

func (s *Server) enrichCatalogDiskFromGateway(snap *llama.RuntimeSnapshot, ms []model.Model) {
	if snap == nil {
		return
	}
	// Ollama 磁盘大小只认本机 /api/tags；勿用 Gateway GGUF 缓存误填「已安装」体积。
	if snap.Backend == "ollama" {
		return
	}
	if ms == nil {
		var err error
		ms, err = s.models.List(context.Background())
		if err != nil {
			return
		}
	}
	apply := func(items *[]llama.CatalogItem) {
		for i := range *items {
			if (*items)[i].DiskBytes > 0 {
				continue
			}
			id := (*items)[i].ID
			for _, m := range ms {
				size := model.CachedDiskBytes(&m)
				if size <= 0 {
					continue
				}
				for _, key := range model.MatchKeys(&m) {
					if llama.ModelIDMatch(id, key) {
						(*items)[i].DiskBytes = size
						break
					}
				}
				if (*items)[i].DiskBytes > 0 {
					break
				}
			}
		}
	}
	apply(&snap.Catalog)
	apply(&snap.Loaded)
}


func (s *Server) syncGatewayModelStatus(ctx context.Context, activeUpstreamID string) {
	s.syncGatewayFromLlamacpp(ctx)
}

func (s *Server) syncGatewayFromLlamacpp(ctx context.Context) {
	client, _, err := s.llamacppAdminClient()
	if err != nil {
		return
	}
	s.syncGatewayFromClient(ctx, client, "llamacpp")
}

func (s *Server) syncGatewayFromUpstream(ctx context.Context) {
	client := s.llamaClient()
	active := strings.TrimSpace(s.effectiveUpstream().ModelName)
	snap, err := client.RuntimeSnapshot(ctx, active)
	if err != nil {
		return
	}
	u := s.effectiveUpstream()
	s.applyInMemorySync(ctx, snap, u.Backend)
}

func (s *Server) syncGatewayFromClient(ctx context.Context, client *llama.Client, backend string) {
	active := ""
	if s.effectiveUpstream().Backend == backend {
		active = strings.TrimSpace(s.effectiveUpstream().ModelName)
	}
	snap, err := client.RuntimeSnapshot(ctx, active)
	if err != nil {
		return
	}
	s.applyInMemorySync(ctx, snap, backend)
}

func (s *Server) applyInMemorySync(ctx context.Context, snap *llama.RuntimeSnapshot, backend string) {
	inMemory := map[string]struct{}{}
	for _, item := range snap.Catalog {
		if item.Loaded {
			inMemory[item.ID] = struct{}{}
		}
	}
	for _, item := range snap.Loaded {
		inMemory[item.ID] = struct{}{}
	}
	ms, _ := s.models.List(ctx)
	for _, m := range ms {
		var matchedID string
		for id := range inMemory {
			if s.modelMatchesUpstreamID(&m, id) {
				matchedID = id
				break
			}
		}
		if matchedID != "" {
			_ = s.models.MarkLoaded(ctx, m.ModelKey)
			s.persistUpstreamName(ctx, &m, backend, matchedID)
		} else if m.Status == "loaded" {
			_ = s.models.SetStatus(ctx, m.ModelKey, "unloaded")
		}
	}
}
