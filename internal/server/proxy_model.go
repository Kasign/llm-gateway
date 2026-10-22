// server（proxy_model）：决定 /v1/* 实际转发到上游的哪一个模型，并在需要时触发加载。
package server

import (
	"context"
	"strings"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/llama"
	"github.com/whl/llm-gateway/internal/model"
)

// proxyModelTarget 表示一次代理应使用的网关 model_key 与上游 ID；调用方 JSON 里的 model 可能被忽略。
// proxyModelTarget is the model used for /v1/* traffic (caller "model" is ignored).
type proxyModelTarget struct {
	Hint       string
	ModelKey   string
	UpstreamID string
	Model      *model.Model
}

func (s *Server) configuredProxyModelHint() string {
	u := s.effectiveUpstream()
	if h := strings.TrimSpace(u.ModelName); h != "" {
		return h
	}
	// 远程 Provider 不能回退到启动时残留的本地模型名。
	if !isLocalInferenceBackend(u.Backend) {
		return ""
	}
	return strings.TrimSpace(s.cfg.Llama.ModelName)
}

// resolveProxyModelTarget 在 chat 代理路径上决定「网关用哪个模型」：读管理台/配置 hint，查 DB，得到上游 ID。
// 切换上游会清掉不属于新后端的模型名；此时若新上游已加载模型，或目录里只有一个模型，则采用它，避免空配置直接 503。
// 调用方 body 里的 model 不参与此决策（之后会用 rewriteJSONModel 覆盖）。
func (s *Server) resolveProxyModelTarget(ctx context.Context) (proxyModelTarget, error) {
	var zero proxyModelTarget
	active := s.effectiveUpstream()
	// 远程 Provider 的模型 ID 属于 Provider 配置，不应按本地模型表再次映射：
	// 例如名称碰巧与本地 model_key 相同，也必须原样转发给远程供应商。
	if remote, ok := s.remoteProviderConfig(active.Backend); ok {
		modelID := strings.TrimSpace(active.ModelName)
		if modelID == "" {
			modelID = strings.TrimSpace(remote.DefaultModel)
		}
		if modelID == "" {
			return zero, &core.Error{Status: 503, Code: "no_proxy_model", Message: "未配置远程 Provider 默认模型"}
		}
		return proxyModelTarget{Hint: modelID, ModelKey: modelID, UpstreamID: modelID}, nil
	}
	hint := s.configuredProxyModelHint()
	adopted := false
	if hint == "" {
		hint = s.proxyModelIDFromUpstream(ctx, s.effectiveUpstream().Backend)
		adopted = hint != ""
	}
	if hint == "" {
		return zero, &core.Error{Status: 503, Code: "no_proxy_model", Message: "未配置推理模型：请在管理台为当前上游加载模型"}
	}
	target, err := s.proxyTargetForHint(ctx, hint)
	if err != nil {
		return zero, err
	}
	if adopted {
		name := strings.TrimSpace(target.UpstreamID)
		if name == "" {
			name = hint
		}
		s.maybeUpdateActiveUpstreamModel(ctx, s.effectiveUpstream().Backend, name)
	}
	return target, nil
}

func (s *Server) proxyTargetForHint(ctx context.Context, hint string) (proxyModelTarget, error) {
	if m, err := s.models.GetByKey(ctx, hint); err == nil {
		return s.proxyTargetFromGatewayModel(ctx, m, hint)
	}
	if m := s.gatewayModelForUpstreamID(ctx, hint); m != nil {
		return s.proxyTargetFromGatewayModel(ctx, m, hint)
	}
	return proxyModelTarget{Hint: hint, ModelKey: hint, UpstreamID: hint}, nil
}

// proxyModelIDFromUpstream 只看该后端自己的运行时快照，不读另一个后端留下的 ModelName。
func (s *Server) proxyModelIDFromUpstream(ctx context.Context, backend string) string {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend != "llamacpp" && backend != "ollama" {
		return ""
	}
	if s.cfg == nil || strings.TrimSpace(s.baseURLForBackend(backend)) == "" {
		return ""
	}
	list, err := s.InferenceList(ctx, backend, false)
	if err != nil || list.Snapshot == nil {
		return ""
	}
	return proxyModelIDFromSnapshot(list.Snapshot, backend)
}

// proxyModelIDFromSnapshot 优先用已在内存中的模型；都没加载且目录只有一个时用那一个，便于 auto_load。
func proxyModelIDFromSnapshot(snap *llama.RuntimeSnapshot, backend string) string {
	if id := activeModelIDFromSnapshot(snap, backend); id != "" {
		return id
	}
	if snap == nil || len(snap.Catalog) != 1 {
		return ""
	}
	return strings.TrimSpace(snap.Catalog[0].ID)
}

func (s *Server) proxyTargetFromGatewayModel(ctx context.Context, m *model.Model, hint string) (proxyModelTarget, error) {
	var zero proxyModelTarget
	if !m.Enabled {
		return zero, &core.Error{Status: 403, Code: "model_disabled", Message: "model disabled"}
	}
	actual, err := s.resolveUpstreamModelName(ctx, m)
	if err != nil {
		return zero, &core.Error{Status: 502, Code: "upstream_model_mismatch", Message: err.Error()}
	}
	if actual == "" {
		actual = strings.TrimSpace(m.LlamaModelName)
	}
	if actual == "" {
		actual = strings.TrimSuffix(m.Filename, ".gguf")
	}
	if actual == "" {
		actual = hint
	}
	return proxyModelTarget{Hint: hint, ModelKey: m.ModelKey, UpstreamID: actual, Model: m}, nil
}

// ensureProxyModelReady 在 auto_load_on_proxy 开启时，尽量保证模型已在 llama/Ollama 内存中。
func (s *Server) ensureProxyModelReady(ctx context.Context, t proxyModelTarget) error {
	if ok, _ := s.llamaClient().ModelLoadedInMemory(ctx, t.UpstreamID); ok {
		return nil
	}
	be := s.effectiveUpstream().Backend
	var loadErr error
	if t.Model != nil {
		_, loadErr = s.InferenceLoad(ctx, be, "", t.Model.ModelKey, nil)
	} else {
		_, loadErr = s.InferenceLoad(ctx, be, t.UpstreamID, "", nil)
	}
	if loadErr != nil && !isLlamaNotFound(loadErr) && !isLlamaAlreadyRunning(loadErr) {
		applog.Printf("ensureProxyModelReady: load %q failed: %v", t.UpstreamID, loadErr)
		return loadErr
	}
	if err := s.llamaClient().WaitUntilModelReady(ctx, t.UpstreamID); err != nil {
		applog.Printf("ensureProxyModelReady: wait model %q: %v", t.UpstreamID, err)
		return err
	}
	return nil
}
