// server（inference_stats）：首页推理内存块，投影自 admin_state。
package server

import (
	"context"
	"strings"

	"github.com/whl/llm-gateway/internal/llama"
)

func inferenceInMemoryModels(snap *llama.RuntimeSnapshot) []map[string]interface{} {
	if snap == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []map[string]interface{}
	add := func(id, status string, loaded bool) {
		id = strings.TrimSpace(id)
		if id == "" || !loaded {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, map[string]interface{}{
			"id":     id,
			"status": status,
		})
	}
	for _, item := range snap.Catalog {
		add(item.ID, item.Status, item.Loaded)
	}
	for _, item := range snap.Loaded {
		st := item.Status
		if st == "" {
			st = "loaded"
		}
		add(item.ID, st, true)
	}
	return out
}

func inferenceStillLoaded(ctx context.Context, client *llama.Client) ([]map[string]interface{}, int64, int64) {
	snap, err := client.RuntimeSnapshotRescan(ctx, "")
	if err != nil {
		return nil, 0, 0
	}
	return inferenceInMemoryModels(snap), snap.LoadedRAM, snap.LoadedVRAM
}
