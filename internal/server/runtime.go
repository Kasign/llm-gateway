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
	if u.Backend == "ollama" {
		return s.buildOllamaRuntimeOverview(ctx)
	}
	return s.buildLlamacppRuntimeSnapshot(ctx)
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
	var in struct {
		ModelID  string `json:"model_id"`
		ModelKey string `json:"model_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonError(w, 400, "invalid_body", err.Error())
		return
	}
	result, err := s.InferenceUnload(r.Context(), "llamacpp", in.ModelID, in.ModelKey, nil)
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
	var in struct {
		ModelID  string `json:"model_id"`
		ModelKey string `json:"model_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonError(w, 400, "invalid_body", err.Error())
		return
	}
	res, err := s.InferenceLoad(r.Context(), "llamacpp", in.ModelID, in.ModelKey, nil)
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
	snap, _ := s.buildLlamacppRuntimeSnapshot(r.Context())
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":      true,
		"active_model": res.LoadedAs,
		"runtime":      snap,
	})
	applog.Infof("runtime select: model_id=%q backend=llamacpp", res.LoadedAs)
}

func (s *Server) buildLlamacppRuntimeSnapshot(ctx context.Context) (map[string]interface{}, error) {
	list, err := s.InferenceList(ctx, "llamacpp", false)
	if err != nil {
		return nil, err
	}
	snap := list.Snapshot
	if snap == nil {
		return nil, fmt.Errorf("empty llamacpp upstream snapshot")
	}
	baseURL := list.BaseURL
	client, _, err := s.llamacppAdminClient()
	if err != nil {
		return nil, err
	}

	var gatewayModelKey string
	ms := list.GatewayModels
	for _, m := range ms {
		if strings.EqualFold(m.Status, "loaded") {
			gatewayModelKey = m.ModelKey
			break
		}
	}
	if gatewayModelKey == "" {
		for _, m := range ms {
			resolved := s.resolveLlamaModel(ctx, &m)
			if resolved != "" && (resolved == snap.ActiveModel || strings.TrimSuffix(resolved, ".gguf") == strings.TrimSuffix(snap.ActiveModel, ".gguf")) {
				gatewayModelKey = m.ModelKey
				break
			}
		}
	}

	var activeItem *llama.CatalogItem
	for i := range snap.Loaded {
		if snap.Loaded[i].ID == snap.ActiveModel || snap.Loaded[i].Active {
			activeItem = &snap.Loaded[i]
			break
		}
	}
	if activeItem == nil && len(snap.Loaded) > 0 {
		activeItem = &snap.Loaded[0]
	}

	var diskEstimate int64
	if activeItem != nil && activeItem.DiskBytes > 0 {
		diskEstimate = activeItem.DiskBytes
	} else {
		for _, m := range ms {
			if m.ModelKey == gatewayModelKey {
				diskEstimate = model.CachedDiskBytes(&m)
			}
			if diskEstimate > 0 {
				break
			}
		}
	}

	hotUnload, _ := client.HasRouter(ctx)
	activeModel := strings.TrimSpace(snap.ActiveModel)
	if activeModel == "" {
		activeModel = activeModelIDFromSnapshot(snap, "llamacpp")
	}
	if activeModel == "" {
		activeModel = s.activeModelHintForBackend("llamacpp")
	}
	if gatewayModelKey == "" && activeModel != "" {
		if gw := s.gatewayModelForUpstreamID(ctx, activeModel); gw != nil {
			gatewayModelKey = gw.ModelKey
		}
	}
	out := map[string]interface{}{
		"backend":              "llamacpp",
		"base_url":             baseURL,
		"active_model":         activeModel,
		"gateway_model_key":    gatewayModelKey,
		"loaded":               snap.Loaded,
		"catalog":              snap.Catalog,
		"loaded_vram_bytes":    snap.LoadedVRAM,
		"loaded_ram_bytes":     snap.LoadedRAM,
		"disk_bytes_est":       diskEstimate,
		"hot_unload_supported": hotUnload,
	}
	if !hotUnload {
		out["unload_note"] = "当前 llama.cpp 可能为单模型进程：Gateway「卸载」不会删磁盘，但可能无法热释放权重，需 router 模式或重启推理容器"
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
