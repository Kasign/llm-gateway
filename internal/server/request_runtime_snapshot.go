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
	out := &RequestRuntimeSnapshot{Status: "unavailable", Source: "runtime_sampler"}
	s.applyHostCPUSnapshot(out, now)
	if sample == nil {
		return out
	}
	age := now.Sub(sample.SampledAt)
	if age < 0 {
		age = 0
	}
	out.Status = sample.Status
	out.Source = sample.Source
	out.SampledAt = sample.SampledAt.UTC().Format(time.RFC3339Nano)
	out.AgeMS = age.Milliseconds()
	if age > runtimeSnapshotMaxAge {
		out.Status = "unavailable"
		out.Source = "stale_runtime_sampler"
		return out
	}
	if sample.Snapshot != nil {
		out.Backend = sample.Snapshot.Backend
		out.ActiveModel = sample.Snapshot.ActiveModel
		for _, item := range sample.Snapshot.Loaded {
			if item.Loaded {
				out.LoadedModelCount++
			}
		}
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
	if out.Status == "" {
		out.Status = "partial"
	}
	return out
}



func (s *Server) applyHostCPUSnapshot(out *RequestRuntimeSnapshot, now time.Time) {
	if s == nil || out == nil {
		return
	}
	if cpu, sampledAt, ok := s.cpuSnapshotPercent(); ok {
		out.HostCPUPercent = &cpu
		out.HostCPUStatus = "available"
		out.HostCPUSampledAt = sampledAt.UTC().Format(time.RFC3339Nano)
		ageMS := now.Sub(sampledAt).Milliseconds()
		if ageMS < 0 {
			ageMS = 0
		}
		out.HostCPUAgeMS = &ageMS
		return
	}
	out.HostCPUStatus = "unavailable"
	if out.Status == "complete" {
		out.Status = "partial"
	}
}
