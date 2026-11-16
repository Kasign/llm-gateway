// server（gateway_model_ops）：网关本地模型目录的列表展示与删除（卸内存、删文件、删库记录）。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/whl/llm-gateway/internal/model"
)

// ListGatewayModelRows returns models from DB with resolved upstream names for the active proxy backend.
func (s *Server) ListGatewayModelRows(ctx context.Context) ([]map[string]interface{}, error) {
	ms, err := s.models.List(ctx)
	if err != nil {
		return nil, err
	}
	be := s.effectiveUpstream().Backend
	items := make([]map[string]interface{}, 0, len(ms))
	for _, m := range ms {
		var row map[string]interface{}
		b, _ := json.Marshal(m)
		_ = json.Unmarshal(b, &row)
		up := model.ParseRuntimeJSON(m.RuntimeJSON)
		row["upstream_llamacpp_id"] = up.LlamaCppID
		row["upstream_ollama_name"] = up.OllamaName
		row["upstream_name"] = model.UpstreamNameForBackend(m.RuntimeJSON, be)
		disk := model.CachedDiskBytes(&m)
		row["size_bytes"] = disk
		row["disk_bytes"] = disk
		items = append(items, row)
	}
	return items, nil
}

// DeleteGatewayModel unloads from llama.cpp, removes local GGUF, deletes DB row.
func (s *Server) DeleteGatewayModel(ctx context.Context, m *model.Model) error {
	if m == nil {
		return fmt.Errorf("model required")
	}
	if m.Status == "downloading" {
		if err := s.stopAndWaitDownload(ctx, m.ModelKey); err != nil {
			return err
		}
	}
	_ = s.unloadModelUpstream(ctx, m)
	if lp := strings.TrimSpace(m.LocalPath); lp != "" {
		if e := os.Remove(lp); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	if err := s.models.Delete(ctx, m.ModelKey); err != nil {
		return err
	}
	_, _ = s.InferenceRefreshRegistry(ctx, "llamacpp")
	return nil
}

// CancelModelDownload stops an in-flight HF download for modelKey.
func (s *Server) CancelModelDownload(modelKey string) error {
	s.downloadMu.Lock()
	cancel, ok := s.downloadCancel[modelKey]
	s.downloadMu.Unlock()
	if !ok {
		return fmt.Errorf("download not running")
	}
	// 不提前移除任务或修改终态；由工作协程在实际退出后写入取消状态并清理登记。
	cancel()
	return nil
}

// stopAndWaitDownload cancels an active download and waits until its worker has stopped
// touching the partial/final file before the caller removes model data.
func (s *Server) stopAndWaitDownload(ctx context.Context, modelKey string) error {
	s.downloadMu.Lock()
	cancel := s.downloadCancel[modelKey]
	done := s.downloadDone[modelKey]
	s.downloadMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RetryModelDownload restarts HF download from model metadata.
func (s *Server) RetryModelDownload(ctx context.Context, m *model.Model) error {
	if m == nil {
		return fmt.Errorf("model required")
	}
	if m.HFRepo == "" || m.Filename == "" {
		return fmt.Errorf("download metadata missing")
	}
	return s.startDownloadModel(m.ModelKey, m.DisplayName, m.HFRepo, m.HFRevision, m.Filename, m.Quantization)
}
