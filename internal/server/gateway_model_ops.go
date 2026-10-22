// server（gateway_model_ops）：网关本地模型目录的列表展示与删除（卸内存、删文件、删库记录）。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

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
		_ = s.CancelModelDownload(m.ModelKey)
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
	defer s.downloadMu.Unlock()
	c, ok := s.downloadCancel[modelKey]
	if !ok {
		return fmt.Errorf("download not running")
	}
	c()
	delete(s.downloadCancel, modelKey)
	_ = s.models.SetStatus(context.Background(), modelKey, "download_cancelled")
	return nil
}

// RetryModelDownload restarts HF download from model metadata.
func (s *Server) RetryModelDownload(ctx context.Context, m *model.Model) error {
	if m == nil {
		return fmt.Errorf("model required")
	}
	if m.HFRepo == "" || m.Filename == "" {
		return fmt.Errorf("download metadata missing")
	}
	_ = s.models.SetStatus(ctx, m.ModelKey, "downloading")
	_ = s.limiter.SetDownload(ctx, m.ModelKey, map[string]interface{}{
		"status":     "downloading",
		"updated_at": time.Now().Unix(),
	})
	_ = s.limiter.DeleteDownloadFields(ctx, m.ModelKey, "error")
	go s.downloadModel(m.ModelKey, m.DisplayName, m.HFRepo, m.HFRevision, m.Filename, m.Quantization)
	return nil
}
