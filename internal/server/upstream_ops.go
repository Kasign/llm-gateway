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
// 配置更新阶段与模型自动采纳短暂互斥；实际网络加载不持锁，避免加载成功回调死锁。
// 换后端时不沿用另一个后端的模型名（GGUF 名在 Ollama 上无效）。
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

	s.upstreamChangeMu.Lock()
	current := s.effectiveUpstream()
	if !sameUpstreamRoute(current, cur) {
		s.upstreamChangeMu.Unlock()
		return fmt.Errorf("活动上游已发生变化，请重新提交切换请求")
	}
	if err := s.settings.SaveUpstream(ctx, u); err != nil {
		s.upstreamChangeMu.Unlock()
		return err
	}
	oldBackend, oldURL := cur.Backend, cur.BaseURL
	s.applyUpstream(u, true)
	s.upstreamChangeMu.Unlock()

	// 仅在本地后端之间切换时释放旧模型；切到远程 Provider 不发送本地模型管理请求。
	if isLocalInferenceBackend(oldBackend) && isLocalInferenceBackend(u.Backend) &&
		oldURL != "" && (oldBackend != u.Backend || oldURL != u.BaseURL) {
		oldClient := s.newUpstreamClient(oldURL, oldBackend)
		_ = oldClient.UnloadAll(ctx)
	}

	if loadModelID != "" {
		loadedAs := loadModelID
		if isLocalInferenceBackend(u.Backend) {
			if s.cfg.Llama.ModelsMax <= 1 && u.Backend == "ollama" {
				_ = s.InferenceUnloadAll(ctx, "ollama")
			}
			res, err := s.InferenceLoad(ctx, u.Backend, loadModelID, "", nil)
			if err != nil {
				return err
			}
			if v := strings.TrimSpace(res.LoadedAs); v != "" {
				loadedAs = v
			}
		}

		s.upstreamChangeMu.Lock()
		current = s.effectiveUpstream()
		// InferenceLoad 成功回调可能已把 ModelName 更新成 loadedAs；仅接受本次切换自己的两种状态。
		if !sameUpstreamEndpoint(current, u) ||
			(strings.TrimSpace(current.ModelName) != strings.TrimSpace(u.ModelName) &&
				strings.TrimSpace(current.ModelName) != loadedAs) {
			s.upstreamChangeMu.Unlock()
			return fmt.Errorf("模型加载期间活动上游已再次切换；保留最新上游配置")
		}
		current.ModelName = loadedAs
		byBackend = copyBackendModels(current.ModelsByBackend)
		byBackend[current.Backend] = loadedAs
		current.ModelsByBackend = byBackend
		if err := s.settings.SaveUpstream(ctx, current); err != nil {
			s.upstreamChangeMu.Unlock()
			return err
		}
		s.applyUpstream(current, true)
		s.upstreamChangeMu.Unlock()
		u = current
	} else if oldBackend != u.Backend && strings.TrimSpace(u.ModelName) == "" {
		if id := s.proxyModelIDFromUpstream(ctx, u.Backend); id != "" {
			s.upstreamChangeMu.Lock()
			current = s.effectiveUpstream()
			if !sameUpstreamRoute(current, u) || strings.TrimSpace(current.ModelName) != "" {
				s.upstreamChangeMu.Unlock()
				return fmt.Errorf("自动选择模型期间活动上游已变化；保留最新上游配置")
			}
			current.ModelName = id
			byBackend = copyBackendModels(current.ModelsByBackend)
			byBackend[current.Backend] = id
			current.ModelsByBackend = byBackend
			if err := s.settings.SaveUpstream(ctx, current); err != nil {
				s.upstreamChangeMu.Unlock()
				return err
			}
			s.applyUpstream(current, false)
			s.upstreamChangeMu.Unlock()
			u = current
		}
	}

	s.invalidateAdminRuntimeCaches()
	applog.Infof("runtime upstream updated: backend=%s url=%s model=%s", u.Backend, u.BaseURL, u.ModelName)
	return nil
}

// sameUpstreamEndpoint 比较 Provider 和地址；不比较模型 ID。
func sameUpstreamEndpoint(a, b settings.Upstream) bool {
	return strings.EqualFold(strings.TrimSpace(a.Backend), strings.TrimSpace(b.Backend)) &&
		strings.TrimRight(strings.TrimSpace(a.BaseURL), "/") == strings.TrimRight(strings.TrimSpace(b.BaseURL), "/")
}

// sameUpstreamRoute 比较会影响请求路由的核心字段。
func sameUpstreamRoute(a, b settings.Upstream) bool {
	return sameUpstreamEndpoint(a, b) &&
		strings.TrimSpace(a.ModelName) == strings.TrimSpace(b.ModelName)
}

func copyBackendModels(src map[string]string) map[string]string {
	out := make(map[string]string, len(src)+1)
	for backend, modelName := range src {
		out[backend] = modelName
	}
	return out
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
