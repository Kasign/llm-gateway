package server

import (
	"time"
)

const runtimeSnapshotMaxAge = 15 * time.Second

func (s *Server) latestRequestRuntimeSnapshot() *RequestRuntimeSnapshot {
	if s == nil {
		return nil
	}
	sample := s.runtimeSnapshotSampleLatest()
	now := time.Now()
	if sample == nil {
		return &RequestRuntimeSnapshot{Status: "unavailable", Source: "runtime_sampler"}
	}
	age := now.Sub(sample.SampledAt)
	if age < 0 {
		age = 0
	}
	out := &RequestRuntimeSnapshot{
		Status: sample.Status,
		Source: sample.Source,
		SampledAt: sample.SampledAt.UTC().Format(time.RFC3339Nano),
		AgeMS: age.Milliseconds(),
	}
	if age > runtimeSnapshotMaxAge {
		out.Status = "unavailable"
		out.Source = "stale_runtime_sampler"
		return out
	}
	if sample.Snapshot != nil {
		out.Backend = sample.Snapshot.Backend
		out.ActiveModel = sample.Snapshot.ActiveModel
		out.LoadedModelCount = len(sample.Snapshot.Loaded)
		out.LoadedRAMBytes = sample.Snapshot.LoadedRAM
		out.LoadedVRAMBytes = sample.Snapshot.LoadedVRAM
		// 部分 Provider/测试夹具可能只填充 Loaded 条目；汇总值缺失时从已加载条目回退。
		if out.LoadedRAMBytes == 0 || out.LoadedVRAMBytes == 0 {
			var loadedRAM, loadedVRAM int64
			for _, item := range sample.Snapshot.Loaded {
				if !item.Loaded {
					continue
				}
				loadedRAM += item.RAMBytes
				loadedVRAM += item.VRAMBytes
			}
			if out.LoadedRAMBytes == 0 {
				out.LoadedRAMBytes = loadedRAM
			}
			if out.LoadedVRAMBytes == 0 {
				out.LoadedVRAMBytes = loadedVRAM
			}
		}
	}
	if cpu, ok := s.cpuSnapshotPercent(); ok {
		out.HostCPUPercent = &cpu
	} else if out.Status == "complete" {
		out.Status = "partial"
	}
	if out.Status == "" {
		out.Status = "partial"
	}
	return out
}

