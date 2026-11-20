package server

import (
 "testing"
 "time"
 "github.com/whl/llm-gateway/internal/llama"
)

var testRuntimeSnapshotSample = &llama.RuntimeSnapshot{Backend:"ollama", ActiveModel:"m", Loaded: []llama.CatalogItem{{ID:"m",Loaded:true,RAMBytes:128}}}
func TestLatestRequestRuntimeSnapshotStale(t *testing.T) {
 s:=&Server{}; s.setRuntimeSnapshotSample(testRuntimeSnapshotSample,"complete","upstream_runtime")
 s.runtimeSnapshotSampleMu.Lock(); s.runtimeSnapshotSample.SampledAt=time.Now().Add(-runtimeSnapshotMaxAge-time.Second); s.runtimeSnapshotSampleMu.Unlock()
 s.cpuSampleMu.Lock(); s.cpuCachedPercent=21; s.cpuProcLastSample=time.Now(); s.cpuSampleMu.Unlock()
 got:=s.latestRequestRuntimeSnapshot(); if got==nil||got.Status!="unavailable"||got.Source!="stale_runtime_sampler"{t.Fatalf("unexpected: %#v",got)}
 if got.HostCPUStatus!="available" || got.HostCPUPercent==nil || *got.HostCPUPercent!=21 { t.Fatalf("fresh host CPU evidence must survive stale upstream snapshot: %#v",got) }
}
func TestLatestRequestRuntimeSnapshotPartialWithoutCPU(t *testing.T) {
 s:=&Server{}; s.setRuntimeSnapshotSample(testRuntimeSnapshotSample,"complete","upstream_runtime")
 got:=s.latestRequestRuntimeSnapshot(); if got==nil||got.Status!="partial"{t.Fatalf("unexpected: %#v",got)}
 if got.HostCPUStatus!="unavailable" || got.HostCPUPercent!=nil { t.Fatalf("CPU availability must be explicit: %#v",got) }
 if got.LoadedModelCount!=1||got.LoadedRAMBytes!=128{t.Fatalf("unexpected model snapshot: %#v",got)}
}


func TestLatestRequestRuntimeSnapshotSumsLoadedModelMemoryFallback(t *testing.T) {
 s := &Server{}
 snap := &llama.RuntimeSnapshot{Backend:"ollama", Loaded: []llama.CatalogItem{
  {ID:"a", Loaded:true, RAMBytes:128, VRAMBytes:64},
  {ID:"b", Loaded:true, RAMBytes:256, VRAMBytes:32},
  {ID:"not-loaded", Loaded:false, RAMBytes:999, VRAMBytes:999},
 }}
 s.setRuntimeSnapshotSample(snap,"complete","upstream_runtime")
 got := s.latestRequestRuntimeSnapshot()
 if got == nil || got.LoadedModelCount != 2 || got.LoadedRAMBytes != 384 || got.LoadedVRAMBytes != 96 {
  t.Fatalf("expected loaded memory sums RAM=384 VRAM=96, got %#v", got)
 }
}


func TestRuntimeSnapshotSampleUsesDefensiveCopies(t *testing.T) {
 s := &Server{}
 original := &llama.RuntimeSnapshot{Backend:"ollama", Loaded: []llama.CatalogItem{{ID:"model-a", Loaded:true, RAMBytes:128}}}
 s.setRuntimeSnapshotSample(original,"complete","upstream_runtime")
 original.Loaded[0].RAMBytes = 999
 first := s.runtimeSnapshotSampleLatest()
 if first == nil || first.Snapshot.Loaded[0].RAMBytes != 128 {
  t.Fatalf("stored snapshot aliased caller memory: %#v", first)
 }
 first.Snapshot.Loaded[0].RAMBytes = 777
 second := s.runtimeSnapshotSampleLatest()
 if second == nil || second.Snapshot.Loaded[0].RAMBytes != 128 {
  t.Fatalf("returned snapshot aliased internal memory: %#v", second)
 }
}


func TestLatestRequestRuntimeSnapshotIncludesIndependentCPUFreshness(t *testing.T) {
 s := &Server{}
 s.setRuntimeSnapshotSample(testRuntimeSnapshotSample, "complete", "upstream_runtime")
 sampledAt := time.Now().Add(-250 * time.Millisecond)
 s.cpuSampleMu.Lock()
 s.cpuCachedPercent = 37.5
 s.cpuProcLastSample = sampledAt
 s.cpuSampleMu.Unlock()

 got := s.latestRequestRuntimeSnapshot()
 if got == nil || got.HostCPUPercent == nil || *got.HostCPUPercent != 37.5 {
  t.Fatalf("expected host CPU sample, got %#v", got)
 }
 if got.HostCPUStatus != "available" || got.HostCPUSampledAt == "" || got.HostCPUAgeMS == nil {
  t.Fatalf("expected independent CPU freshness metadata, got %#v", got)
 }
 if *got.HostCPUAgeMS < 200 || *got.HostCPUAgeMS > 2000 {
  t.Fatalf("unexpected CPU sample age: %dms", *got.HostCPUAgeMS)
 }
}


func TestLatestRequestRuntimeSnapshotKeepsCPUWhenUpstreamUnavailable(t *testing.T) {
 s := &Server{}
 s.cpuSampleMu.Lock()
 s.cpuCachedPercent = 18
 s.cpuProcLastSample = time.Now()
 s.cpuSampleMu.Unlock()
 got := s.latestRequestRuntimeSnapshot()
 if got == nil || got.Status != "unavailable" || got.Source != "runtime_sampler" {
  t.Fatalf("expected unavailable upstream snapshot, got %#v", got)
 }
 if got.HostCPUStatus != "available" || got.HostCPUPercent == nil || *got.HostCPUPercent != 18 {
  t.Fatalf("CPU sample must be independently reported, got %#v", got)
 }
}
