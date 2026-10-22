// server（llama_model）：把网关 Model 与 llama.cpp router 对齐（解析 upstream 名、加载 GGUF、刷新注册表）。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/llama"
	"github.com/whl/llm-gateway/internal/model"
)

func (s *Server) llamaModelsDir() string {
	if s.cfg.Llama.ModelsDir != "" {
		return s.cfg.Llama.ModelsDir
	}
	return s.cfg.Storage.ModelsDir
}

func (s *Server) gatewayModelMatchKeys(m *model.Model) []string {
	filename := m.Filename
	if filename == "" {
		filename = filepath.Base(m.LlamaModelName)
	}
	stem := strings.TrimSuffix(filename, ".gguf")
	if stem == "" {
		stem = strings.TrimSuffix(m.LlamaModelName, ".gguf")
	}
	seen := map[string]struct{}{}
	var keys []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		keys = append(keys, v)
	}
	add(m.ModelKey)
	add(m.LlamaModelName)
	add(filename)
	add(stem)
	if m.LocalPath != "" {
		add(m.LocalPath)
		add(filepath.Base(m.LocalPath))
	}
	return keys
}

func (s *Server) modelMatchesUpstreamID(m *model.Model, upstreamID string) bool {
	for _, k := range s.gatewayModelMatchKeys(m) {
		if llama.ModelIDMatch(k, upstreamID) {
			return true
		}
	}
	return false
}

func (s *Server) llamaLoadCandidates(ctx context.Context, m *model.Model) []string {
	return s.llamaLoadCandidatesWithClient(ctx, s.llamaClient(), m)
}

func (s *Server) llamaLoadCandidatesWithClient(ctx context.Context, client *llama.Client, m *model.Model) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}

	filename := m.Filename
	if filename == "" {
		filename = filepath.Base(m.LlamaModelName)
	}
	stem := strings.TrimSuffix(filename, ".gguf")
	if stem == "" {
		stem = strings.TrimSuffix(m.LlamaModelName, ".gguf")
	}
	keys := s.gatewayModelMatchKeys(m)
	llamaDir := s.llamaModelsDir()

	if ids, err := client.ListModelIDs(ctx); err != nil {
		applog.Debugf("llamaLoadCandidates: list models failed: %v", err)
	} else {
		applog.Debugf("llamaLoadCandidates: llama registry ids=%v", ids)
		for _, id := range ids {
			for _, k := range keys {
				if llama.ModelIDMatch(id, k) {
					add(id)
				}
			}
		}
	}

	add(stem)
	add(m.LlamaModelName)
	add(filename)
	add(filepath.Join(llamaDir, filename))
	if m.LocalPath != "" {
		add(m.LocalPath)
	}
	return out
}

func (s *Server) gatewayModelForUpstreamID(ctx context.Context, modelID string) *model.Model {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	ms, err := s.models.List(ctx)
	if err != nil {
		return nil
	}
	for i := range ms {
		if s.modelMatchesUpstreamID(&ms[i], modelID) {
			return &ms[i]
		}
	}
	return nil
}

func (s *Server) upstreamLoadCandidates(ctx context.Context, modelID string) []string {
	return s.upstreamLoadCandidatesWithClient(ctx, s.llamaClient(), modelID)
}

func (s *Server) upstreamLoadCandidatesWithClient(ctx context.Context, client *llama.Client, modelID string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}

	stem := strings.TrimSuffix(filepath.Base(modelID), ".gguf")
	if ids, err := client.ListModelIDs(ctx); err == nil {
		for _, id := range ids {
			idStem := strings.TrimSuffix(filepath.Base(id), ".gguf")
			if id == modelID || idStem == stem || strings.EqualFold(idStem, stem) {
				add(id)
			}
		}
	}

	add(modelID)
	if stem != "" {
		add(stem)
		add(stem + ".gguf")
		add(filepath.Join(s.llamaModelsDir(), stem+".gguf"))
	}
	return out
}

func mergeLoadCandidates(parts ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, part := range parts {
		for _, v := range part {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// loadLlamaCppUpstream loads via the active inference upstream (API proxy path).
func (s *Server) loadLlamaCppUpstream(ctx context.Context, gw *model.Model, upstreamHint string) (string, error) {
	return s.loadLlamaCppUpstreamWithClient(ctx, s.llamaClient(), gw, upstreamHint, s.cfg.Llama.BaseURL, true, nil)
}

// loadLlamaCppUpstreamWithClient uses GET /models ids and POST /models/load on the given llama.cpp client.
func (s *Server) loadLlamaCppUpstreamWithClient(ctx context.Context, client *llama.Client, gw *model.Model, upstreamHint, baseURL string, cacheRouter bool, log InferenceOpLogger) (loadedAs string, err error) {
	upstreamHint = strings.TrimSpace(upstreamHint)
	if baseURL == "" {
		baseURL = s.baseURLForBackend("llamacpp")
	}

	var router bool
	if cacheRouter {
		s.mu.Lock()
		if s.llamaRouter == nil {
			ok, probeErr := client.HasRouter(ctx)
			if probeErr != nil {
				applog.Debugf("loadLlamaCpp: router probe: %v", probeErr)
			}
			s.llamaRouter = &ok
			applog.Infof("llama router mode=%v url=%s", ok, baseURL)
		}
		router = s.llamaRouter != nil && *s.llamaRouter
		s.mu.Unlock()
	} else {
		ok, probeErr := client.HasRouter(ctx)
		if probeErr != nil {
			applog.Debugf("loadLlamaCpp: router probe: %v", probeErr)
		}
		router = ok
		applog.Infof("llama router mode=%v url=%s", router, baseURL)
	}

	if !router {
		applog.Infof("loadLlamaCpp: single-model server, skip POST /models/load (hint=%q)", upstreamHint)
		if upstreamHint != "" {
			return upstreamHint, nil
		}
		if gw != nil {
			return s.resolveLlamaModel(ctx, gw), nil
		}
		return "", nil
	}

	_, _ = client.Refresh(ctx)

	var candidates []string
	if gw != nil {
		candidates = s.llamaLoadCandidatesWithClient(ctx, client, gw)
		if upstreamHint != "" {
			candidates = mergeLoadCandidates(candidates, s.upstreamLoadCandidatesWithClient(ctx, client, upstreamHint))
		}
	} else if upstreamHint != "" {
		candidates = s.upstreamLoadCandidatesWithClient(ctx, client, upstreamHint)
	}
	if entries, err := client.LlamaRouterModels(ctx); err == nil && len(entries) > 0 {
		hint := upstreamHint
		if hint == "" && gw != nil {
			hint = s.resolveLlamaModel(ctx, gw)
		}
		if hint != "" {
			candidates = mergeLoadCandidates(candidates, client.LlamaLoadCandidatesForEntry(hint, entries))
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no load candidates for upstream model %q", upstreamHint)
	}

	if gw != nil && s.cfg.Llama.ModelsMax <= 1 {
		ms, _ := s.models.List(ctx)
		for _, other := range ms {
			if other.ModelKey == gw.ModelKey {
				continue
			}
			for _, cand := range s.llamaLoadCandidatesWithClient(ctx, client, &other) {
				_, _ = client.Unload(ctx, cand)
			}
			_ = s.models.SetStatus(ctx, other.ModelKey, "unloaded")
		}
	} else if s.cfg.Llama.ModelsMax <= 1 {
		_ = client.UnloadAll(ctx)
	}

	applog.Infof("loadLlamaCpp: candidates=%v url=%s", candidates, baseURL)

	var lastErr error
	for _, target := range candidates {
		body, err := client.Load(ctx, target, "")
		if err != nil {
			logUpstreamJSON(log, "llama.cpp load", body, err)
			if isLlamaAlreadyRunning(err) {
				applog.Infof("loadLlamaCpp: %q already running (treat as loaded)", target)
				return target, nil
			}
			lastErr = err
			applog.Infof("loadLlamaCpp: load %q failed upstream_json=%s", target, formatUpstreamJSONBody(body, err))
			if !isLlamaNotFound(err) {
				return "", fmt.Errorf("load model %q via %s: %w", target, baseURL, err)
			}
			continue
		}
		logUpstreamJSON(log, "llama.cpp load", body, nil)
		applog.Infof("loadLlamaCpp: loaded %q upstream_json=%s", target, formatUpstreamJSONBody(body, nil))
		return target, nil
	}

	if lastErr != nil && isLlamaNotFound(lastErr) {
		if cacheRouter {
			s.mu.Lock()
			no := false
			s.llamaRouter = &no
			s.mu.Unlock()
		}
		applog.Infof("loadLlamaCpp: /models/load not found, treating as single-model")
		if upstreamHint != "" {
			return upstreamHint, nil
		}
		if gw != nil {
			return s.resolveLlamaModel(ctx, gw), nil
		}
		return "", nil
	}

	name := upstreamHint
	if gw != nil {
		name = gw.LlamaModelName
		if name == "" {
			name = gw.Filename
		}
	}
	return "", fmt.Errorf("load model %q via %s: %w (tried: %v; run GET %s/models for exact id)", name, baseURL, lastErr, candidates, baseURL)
}

func (s *Server) persistUpstreamName(ctx context.Context, m *model.Model, backend, upstreamID string) {
	if m == nil {
		return
	}
	upstreamID = strings.TrimSpace(upstreamID)
	if upstreamID == "" {
		return
	}
	merged := model.MergeUpstreamName(m.RuntimeJSON, backend, upstreamID)
	if merged == m.RuntimeJSON {
		return
	}
	_ = s.models.SetRuntimeJSON(ctx, m.ModelKey, merged)
}

func (s *Server) persistUpstreamNameForID(ctx context.Context, backend, upstreamID string) {
	upstreamID = strings.TrimSpace(upstreamID)
	if upstreamID == "" {
		return
	}
	ms, _ := s.models.List(ctx)
	for i := range ms {
		if s.modelMatchesUpstreamID(&ms[i], upstreamID) {
			s.persistUpstreamName(ctx, &ms[i], backend, upstreamID)
			return
		}
	}
}

// resolveUpstreamModelName resolves against the active inference upstream (API proxy).
func (s *Server) resolveUpstreamModelName(ctx context.Context, m *model.Model) (string, error) {
	u := s.effectiveUpstream()
	return s.resolveUpstreamModelNameForBackend(ctx, s.llamaClient(), u.Backend, u.ModelName, m)
}

func (s *Server) resolveUpstreamModelNameForBackend(ctx context.Context, client *llama.Client, backend, activeModel string, m *model.Model) (string, error) {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if cached := model.UpstreamNameForBackend(m.RuntimeJSON, backend); cached != "" {
		if ids, err := client.ListModelIDs(ctx); err == nil && len(ids) > 0 {
			for _, id := range ids {
				if llama.ModelIDMatch(id, cached) {
					return id, nil
				}
			}
		} else if backend == "llamacpp" {
			return cached, nil
		} else {
			return cached, nil
		}
	}
	activeModel = strings.TrimSpace(activeModel)
	if activeModel != "" && s.modelMatchesUpstreamID(m, activeModel) {
		return activeModel, nil
	}
	keys := s.gatewayModelMatchKeys(m)
	if ids, err := client.ListModelIDs(ctx); err == nil {
		for _, id := range ids {
			for _, k := range keys {
				if llama.ModelIDMatch(id, k) {
					return id, nil
				}
			}
		}
	}
	if backend == "ollama" {
		return "", fmt.Errorf(
			"Ollama 中找不到与 Gateway 模型 %q 对应的标签（请在「Ollama 库」pull；本地 GGUF 请在「本地 GGUF」加载到 llama.cpp）",
			m.ModelKey,
		)
	}
	if cands := s.llamaLoadCandidatesWithClient(ctx, client, m); len(cands) > 0 {
		return cands[0], nil
	}
	if stem := strings.TrimSuffix(m.LlamaModelName, ".gguf"); stem != "" {
		return stem, nil
	}
	return strings.TrimSuffix(m.Filename, ".gguf"), nil
}

func (s *Server) resolveLlamaModel(ctx context.Context, m *model.Model) string {
	name, err := s.resolveUpstreamModelName(ctx, m)
	if err == nil && name != "" {
		return name
	}
	if stem := strings.TrimSuffix(m.LlamaModelName, ".gguf"); stem != "" {
		return stem
	}
	return strings.TrimSuffix(m.Filename, ".gguf")
}

func isLlamaNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not_found") || strings.Contains(msg, "file not found")
}

func isLlamaAlreadyRunning(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already running") ||
		strings.Contains(msg, "already loaded") ||
		strings.Contains(msg, "model is already")
}

// rewriteJSONModel 把 OpenAI 请求 JSON 里的 "model" 改成上游真实名称；客户端传的 model 只留在审计日志里。
func rewriteJSONModel(body []byte, modelName string) []byte {
	if modelName == "" || len(body) == 0 {
		return body
	}
	var obj map[string]interface{}
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	obj["model"] = modelName
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}
