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
 got:=s.latestRequestRuntimeSnapshot(); if got==nil||got.Status!="unavailable"||got.Source!="stale_runtime_sampler"{t.Fatalf("unexpected: %#v",got)}
}
func TestLatestRequestRuntimeSnapshotPartialWithoutCPU(t *testing.T) {
 s:=&Server{}; s.setRuntimeSnapshotSample(testRuntimeSnapshotSample,"complete","upstream_runtime")
 got:=s.latestRequestRuntimeSnapshot(); if got==nil||got.Status!="partial"{t.Fatalf("unexpected: %#v",got)}
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
 if got == nil || got.LoadedRAMBytes != 384 || got.LoadedVRAMBytes != 96 {
  t.Fatalf("expected loaded memory sums RAM=384 VRAM=96, got %#v", got)
 }
}
