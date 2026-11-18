package server

import (
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/llama"
)

func TestRuntimeSnapshotSampleLatestReturnsCopy(t *testing.T) {
	s := &Server{}
	snap := &llama.RuntimeSnapshot{
		Backend: "ollama",
		ActiveModel: "m:latest",
		Loaded: []llama.CatalogItem{{ID: "m:latest", Loaded: true, RAMBytes: 10}},
		Catalog: []llama.CatalogItem{{ID: "m:latest", DiskBytes: 20}},
	}
	s.setRuntimeSnapshotSample(snap, "complete", "upstream_runtime")

	got := s.runtimeSnapshotSampleLatest()
	if got == nil || got.Snapshot == nil {
		t.Fatal("expected runtime snapshot")
	}
	if got.Status != "complete" || got.Source != "upstream_runtime" || got.Snapshot.Backend != "ollama" {
		t.Fatalf("unexpected sample: %#v", got)
	}
	if got.SampledAt.IsZero() || time.Since(got.SampledAt) < 0 {
		t.Fatalf("invalid sample timestamp: %v", got.SampledAt)
	}

	got.Snapshot.Loaded[0].RAMBytes = 999
	got.Snapshot.Catalog[0].DiskBytes = 999
	again := s.runtimeSnapshotSampleLatest()
	if again.Snapshot.Loaded[0].RAMBytes != 10 || again.Snapshot.Catalog[0].DiskBytes != 20 {
		t.Fatal("latest snapshot must be isolated from caller mutation")
	}
}

func TestRuntimeSnapshotSampleUnavailable(t *testing.T) {
	s := &Server{}
	s.setRuntimeSnapshotSample(nil, "unavailable", "upstream_runtime")
	got := s.runtimeSnapshotSampleLatest()
	if got == nil || got.Snapshot != nil || got.Status != "unavailable" {
		t.Fatalf("unexpected unavailable sample: %#v", got)
	}
}
