package server

import (
	"context"

	"github.com/whl/llm-gateway/internal/provider"
)

// serverLocalModelManager 将既有本地模型控制流程适配到可选 Provider 能力。
// 控制逻辑仍留在 server/application 层，推理 Provider 不直接依赖数据库或模型仓库。
type serverLocalModelManager struct {
	s       *Server
	backend string
}

var _ provider.LocalModelManager = (*serverLocalModelManager)(nil)

func (m *serverLocalModelManager) EnsureReady(ctx context.Context, modelKey, upstreamModelID string) error {
	target := proxyModelTarget{ModelKey: modelKey, Hint: modelKey, UpstreamID: upstreamModelID}
	if modelKey != "" {
		if model, err := m.s.models.GetByKey(ctx, modelKey); err == nil && model != nil {
			target.Model = model
		}
	}
	if target.Model == nil && upstreamModelID != "" {
		if model := m.s.gatewayModelForUpstreamID(ctx, upstreamModelID); model != nil {
			target.Model = model
			target.ModelKey = model.ModelKey
		}
	}
	return m.s.ensureProxyModelReady(ctx, target)
}

func (m *serverLocalModelManager) Load(ctx context.Context, modelID string) error {
	if model, err := m.s.models.GetByKey(ctx, modelID); err == nil && model != nil {
		_, err = m.s.InferenceLoad(ctx, m.backend, "", model.ModelKey, nil)
		return err
	}
	if model := m.s.gatewayModelForUpstreamID(ctx, modelID); model != nil {
		_, err := m.s.InferenceLoad(ctx, m.backend, "", model.ModelKey, nil)
		return err
	}
	_, err := m.s.InferenceLoad(ctx, m.backend, modelID, "", nil)
	return err
}

func (m *serverLocalModelManager) Unload(ctx context.Context, modelID string) error {
	if model, err := m.s.models.GetByKey(ctx, modelID); err == nil && model != nil {
		_, err = m.s.InferenceUnload(ctx, m.backend, "", model.ModelKey, nil)
		return err
	}
	if model := m.s.gatewayModelForUpstreamID(ctx, modelID); model != nil {
		_, err := m.s.InferenceUnload(ctx, m.backend, "", model.ModelKey, nil)
		return err
	}
	_, err := m.s.InferenceUnload(ctx, m.backend, modelID, "", nil)
	return err
}

func (m *serverLocalModelManager) Refresh(ctx context.Context) error {
	_, err := m.s.InferenceRefreshRegistry(ctx, m.backend)
	return err
}
