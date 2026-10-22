// server（model_disk_reconcile）：启动或后台一次性把 GGUF 文件大小回填到 DB 缓存。
package server

import (
	"context"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/model"
)

// ReconcileModelDiskSizes 异步扫描已安装模型的 local_path，将 size_bytes 与磁盘对齐（不读 sha）。
func (s *Server) ReconcileModelDiskSizes() {
	go func() {
		ctx := context.Background()
		ms, err := s.models.List(ctx)
		if err != nil {
			applog.Printf("reconcile model disk: list failed: %v", err)
			return
		}
		var updated int
		for _, m := range ms {
			if m.Status == "downloading" {
				continue
			}
			stat := model.StatDiskBytesFromFile(&m)
			if stat <= 0 {
				continue
			}
			if stat == m.SizeBytes {
				continue
			}
			if e := s.models.SetSizeBytes(ctx, m.ModelKey, stat); e != nil {
				applog.Printf("reconcile model disk: key=%s err=%v", m.ModelKey, e)
				continue
			}
			updated++
		}
		if updated > 0 {
			applog.Printf("reconcile model disk: updated %d model(s)", updated)
		}
	}()
}
