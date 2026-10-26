// server（proxy_model）：决定 /v1/* 实际转发到上游的哪一个模型，并在需要时触发加载。
package server

import (
	"context"
	"strings"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/llama"
	"github.com/whl/llm-gateway/internal/model"
	"github.com/whl/llm-gateway/internal/settings"
)

// proxyModelTarget 表示一次代理应使用的网关 model_key 与上游 ID；调用方 JSON 里的 model 可能被忽略。
// proxyModelTarget is the model used for /v1/* traffic (caller "model" is ignored).
type proxyModelTarget struct {
	ProviderID string
	Hint       string
	ModelKey   string
	UpstreamID string
	Model      *model.Model
}

func (s *Server) configuredProxyModelHint() string {
	return s.configuredProxyModelHintForUpstream(s.effectiveUpstream())
}

func (s *Server) configuredProxyModelHintForUpstream(u settings.Upstream) string {
	backend := strings.ToLower(strings.TrimSpace(u.Backend))
	if name := strings.TrimSpace(u.ModelName); name != "" {
		return name
	}
	if name := strings.TrimSpace(u.ModelsByBackend[backend]); name != "" {
		return name
	}
	if strings.EqualFold(strings.TrimSpace(s.fileUpstream.Backend), backend) {
		if name := strings.TrimSpace(s.fileUpstream.ModelName); name != "" {
			return name
		}
	}
	if s.cfg != nil && isLocalInferenceBackend(backend) &&
		(strings.TrimSpace(s.cfg.Llama.Backend) == "" || strings.EqualFold(strings.TrimSpace(s.cfg.Llama.Backend), backend)) {
		return strings.TrimSpace(s.cfg.Llama.ModelName)
	}
	return ""
}

// resolveProxyModelTarget 在 chat 代理路径上决定「网关用哪个模型」：读管理台/配置 hint，查 DB，得到上游 ID。
// 切换上游会清掉不属于新后端的模型名；此时若新上游已加载模型，或目录里只有一个模型，则采用它，避免空配置直接 503。
// 调用方 body 里的 model 不参与此决策（之后会用 rewriteJSONModel 覆盖）。
func (s *Server) resolveProxyModelTarget(ctx context.Context) (proxyModelTarget, error) {
	return s.resolveProxyModelTargetForUpstream(ctx, s.effectiveUpstream())
}

func (s *Server) resolveProxyModelTargetForUpstream(ctx context.Context, active settings.Upstream) (proxyModelTarget, error) {
	var zero proxyModelTarget
	active.Backend = strings.ToLower(strings.TrimSpace(active.Backend))
	providerID := active.Backend
	if remote, ok := s.remoteProviderConfig(providerID); ok {
		modelID := strings.TrimSpace(active.ModelName)
		if modelID == "" {
			modelID = strings.TrimSpace(remote.DefaultModel)
		}
		if modelID == "" {
			return zero, &core.Error{Status: 503, Code: "no_proxy_model", Message: "未配置远程 Provider 默认模型"}
		}
		upstreamModelID := modelID
		if mapped := strings.TrimSpace(remote.ModelMappings[modelID]); mapped != "" { upstreamModelID = mapped }
		return proxyModelTarget{ProviderID: providerID, Hint: modelID, ModelKey: modelID, UpstreamID: upstreamModelID}, nil
	}
	hint := s.configuredProxyModelHintForUpstream(active)
	adopted := false
	if hint == "" {
		hint = s.proxyModelIDFromUpstream(ctx, providerID)
		adopted = hint != ""
	}
	if hint == "" {
		return zero, &core.Error{Status: 503, Code: "no_proxy_model", Message: "未配置推理模型：请在管理台为当前上游加载模型"}
	}
	target, err := s.proxyTargetForHintForUpstream(ctx, hint, active)
	if err != nil {
		return zero, err
	}
	if adopted {
		name := strings.TrimSpace(target.UpstreamID)
		if name == "" {
			name = hint
		}
		s.maybeUpdateActiveUpstreamModelForSnapshot(ctx, active, providerID, name)
	}
	return target, nil
}

func (s *Server) proxyTargetForHint(ctx context.Context, hint string) (proxyModelTarget, error) {
	return s.proxyTargetForHintForUpstream(ctx, hint, s.effectiveUpstream())
}

func (s *Server) proxyTargetForHintForUpstream(ctx context.Context, hint string, active settings.Upstream) (proxyModelTarget, error) {
	if m, err := s.models.GetByKey(ctx, hint); err == nil {
		return s.proxyTargetFromGatewayModelForUpstream(ctx, active, m, hint)
	}
	if m := s.gatewayModelForUpstreamID(ctx, hint); m != nil {
		return s.proxyTargetFromGatewayModelForUpstream(ctx, active, m, hint)
	}
	return proxyModelTarget{ProviderID: active.Backend, Hint: hint, ModelKey: hint, UpstreamID: hint}, nil
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
	return s.proxyTargetFromGatewayModelForUpstream(ctx, s.effectiveUpstream(), m, hint)
}

func (s *Server) proxyTargetFromGatewayModelForUpstream(ctx context.Context, active settings.Upstream, m *model.Model, hint string) (proxyModelTarget, error) {
	var zero proxyModelTarget
	if !m.Enabled {
		return zero, &core.Error{Status: 403, Code: "model_disabled", Message: "model disabled"}
	}
	client, err := s.localClientForBackend(active.Backend)
	if err != nil {
		return zero, err
	}
	actual, err := s.resolveUpstreamModelNameForBackend(ctx, client, active.Backend, active.ModelName, m)
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
	return proxyModelTarget{ProviderID: active.Backend, Hint: hint, ModelKey: m.ModelKey, UpstreamID: actual, Model: m}, nil
}

// ensureProxyModelReady 在 auto_load_on_proxy 开启时，尽量保证模型已在 llama/Ollama 内存中。
func (s *Server) ensureProxyModelReady(ctx context.Context, t proxyModelTarget) error {
	backend := strings.TrimSpace(t.ProviderID)
	if backend == "" {
		backend = s.effectiveUpstream().Backend
	}
	return s.ensureProxyModelReadyForBackend(ctx, backend, t)
}

// ensureProxyModelReadyForBackend 使用选定后端的客户端加载并等待模型，避免并发切换影响当前调用。
func (s *Server) ensureProxyModelReadyForBackend(ctx context.Context, backend string, t proxyModelTarget) error {
	client, err := s.localClientForBackend(backend)
	if err != nil {
		return err
	}
	if ok, _ := client.ModelLoadedInMemory(ctx, t.UpstreamID); ok {
		return nil
	}
	// 自动加载只保证本次请求模型就绪；不能把旧请求的加载结果反向写回当前活动模型配置。
	loadCtx := suppressActiveModelAdoption(ctx)
	var loadErr error
	if t.Model != nil {
		_, loadErr = s.InferenceLoad(loadCtx, backend, "", t.Model.ModelKey, nil)
	} else {
		_, loadErr = s.InferenceLoad(loadCtx, backend, t.UpstreamID, "", nil)
	}
	if loadErr != nil && !isLlamaNotFound(loadErr) && !isLlamaAlreadyRunning(loadErr) {
		applog.Printf("ensureProxyModelReady: backend=%s load %q failed: %v", backend, t.UpstreamID, loadErr)
		return loadErr
	}
	if err := client.WaitUntilModelReady(ctx, t.UpstreamID); err != nil {
		applog.Printf("ensureProxyModelReady: backend=%s wait model %q: %v", backend, t.UpstreamID, err)
		return err
	}
	return nil
}

