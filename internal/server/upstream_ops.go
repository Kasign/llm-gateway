// server（upstream_ops）：切换 /v1 流量走哪个推理后端，并在切换时尽量释放旧上游内存。
package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/settings"
)

// SwitchProxyUpstream 保存新上游到数据库并立即生效；可选在新后端加载指定模型。
// 换后端时不沿用另一个后端的模型名（GGUF 名在 Ollama 上无效）。先恢复该后端上次的模型；没有则采用它当前已加载或唯一的模型。
// SwitchProxyUpstream updates which backend receives /v1 proxy traffic and optionally loads a model there.
func (s *Server) SwitchProxyUpstream(ctx context.Context, u settings.Upstream, loadModelID string) error {
	cur := s.effectiveUpstream()
	requestedModel := strings.TrimSpace(u.ModelName)
	u = s.resolveUpstream(u)
	loadModelID = strings.TrimSpace(loadModelID)
	if requestedModel == "" && loadModelID == "" {
		requestedModel = strings.TrimSpace(u.ModelName)
	}
	name, byBackend := selectSwitchModelName(cur, u.Backend, requestedModel, loadModelID)
	u.ModelName = name
	u.ModelsByBackend = byBackend

	if !s.isConfiguredProvider(u.Backend) {
		return fmt.Errorf("Provider %q is not configured", u.Backend)
	}
	if err := s.validateUpstreamBackend(ctx, u.Backend, u.BaseURL); err != nil {
		return err
	}
	if err := s.settings.SaveUpstream(ctx, u); err != nil {
		return err
	}

	oldBackend, oldURL := cur.Backend, cur.BaseURL
	s.applyUpstream(u, true)
	// 仅在本地后端之间切换时释放旧模型；切到远程 Provider 不发送本地模型管理请求。
	if isLocalInferenceBackend(oldBackend) && isLocalInferenceBackend(u.Backend) &&
		oldURL != "" && (oldBackend != u.Backend || oldURL != u.BaseURL) {
		oldClient := s.newUpstreamClient(oldURL, oldBackend)
		_ = oldClient.UnloadAll(ctx)
	}

	if loadModelID != "" {
		if isLocalInferenceBackend(u.Backend) {
			if s.cfg.Llama.ModelsMax <= 1 && u.Backend == "ollama" {
				_ = s.InferenceUnloadAll(ctx, "ollama")
			}
			if _, err := s.InferenceLoad(ctx, u.Backend, loadModelID, "", nil); err != nil {
				return err
			}
		}
		u.ModelName = loadModelID
		if u.ModelsByBackend == nil {
			u.ModelsByBackend = map[string]string{}
		}
		u.ModelsByBackend[u.Backend] = loadModelID
		_ = s.settings.SaveUpstream(ctx, u)
		s.applyUpstream(u, true)
	} else if oldBackend != u.Backend && strings.TrimSpace(u.ModelName) == "" {
		if id := s.proxyModelIDFromUpstream(ctx, u.Backend); id != "" {
			u.ModelName = id
			if u.ModelsByBackend == nil {
				u.ModelsByBackend = map[string]string{}
			}
			u.ModelsByBackend[u.Backend] = id
			_ = s.settings.SaveUpstream(ctx, u)
			s.applyUpstream(u, false)
		}
	}

	s.invalidateAdminRuntimeCaches()
	applog.Infof("runtime upstream updated: backend=%s url=%s model=%s", u.Backend, u.BaseURL, u.ModelName)
	return nil
}

// selectSwitchModelName 计算切换后的代理模型名，并更新「每个后端上次使用的模型」。
// 请求里显式指定的名字优先；同一后端且未指定时保留当前名字；换后端时只用该后端自己记住的名字。
func selectSwitchModelName(cur settings.Upstream, nextBackend, requested, loadModelID string) (string, map[string]string) {
	cur = settings.Normalize(cur)
	nextBackend = strings.ToLower(strings.TrimSpace(nextBackend))
	byBackend := map[string]string{}
	for k, v := range cur.ModelsByBackend {
		byBackend[k] = v
	}
	if cur.Backend != "" && cur.ModelName != "" {
		byBackend[cur.Backend] = cur.ModelName
	}
	requested = strings.TrimSpace(requested)
	loadModelID = strings.TrimSpace(loadModelID)
	var name string
	switch {
	case requested != "":
		name = requested
	case loadModelID != "":
		name = loadModelID
	case nextBackend == cur.Backend:
		name = cur.ModelName
	default:
		name = strings.TrimSpace(byBackend[nextBackend])
	}
	if name != "" && nextBackend != "" {
		byBackend[nextBackend] = name
	}
	if len(byBackend) == 0 {
		return name, nil
	}
	return name, byBackend
}
