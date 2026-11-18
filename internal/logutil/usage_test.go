package logutil

import (
	"math"
	"testing"
)

func TestParseUsageChatPromptCompletion(t *testing.T) {
	raw := `data: {"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33}}`
	in, out, total, ok := ParseUsageChunk([]byte(raw))
	if !ok || in != 11 || out != 22 || total != 33 {
		t.Fatalf("chat usage: in=%d out=%d total=%d ok=%v", in, out, total, ok)
	}
}

func TestParseUsageResponsesInputOutput(t *testing.T) {
	raw := `data: {"usage":{"input_tokens":8,"output_tokens":16,"total_tokens":24}}`
	in, out, total, ok := ParseUsageChunk([]byte(raw))
	if !ok || in != 8 || out != 16 || total != 24 {
		t.Fatalf("responses usage: in=%d out=%d total=%d ok=%v", in, out, total, ok)
	}
}

func TestParseUsageResponseCompletedNested(t *testing.T) {
	raw := `data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":7}}}`
	in, out, total, ok := ParseUsageChunk([]byte(raw))
	if !ok || in != 3 || out != 7 || total != 10 {
		t.Fatalf("nested usage: in=%d out=%d total=%d ok=%v", in, out, total, ok)
	}
}

func TestParseUsageAllZeroNotOK(t *testing.T) {
	raw := `data: {"usage":{"input_tokens":0,"output_tokens":0}}`
	if _, _, _, ok := ParseUsageChunk([]byte(raw)); ok {
		t.Fatal("all-zero usage should not count as parsed")
	}
}

func TestParseUsageBodyResponses(t *testing.T) {
	in, out, total := ParseUsageBody([]byte(`{"usage":{"input_tokens":4,"output_tokens":5}}`))
	if in == nil || out == nil || total == nil || *in != 4 || *out != 5 || *total != 9 {
		t.Fatalf("body usage: %v %v %v", in, out, total)
	}
}

func TestApplyStreamChunkTokensSplitAcrossChunks(t *testing.T) {
	var h TokenHarvest
	var carry string
	ApplyStreamChunkTokens(&h, []byte(`data: {"usage":{"input_tokens":6,"out`), &carry)
	if !h.Empty() {
		t.Fatalf("half line should wait, got %+v", h)
	}
	ApplyStreamChunkTokens(&h, []byte(`put_tokens":9}}`+"\n"), &carry)
	if h.Empty() || h.InputTokens == nil || *h.InputTokens != 6 || *h.OutputTokens != 9 {
		t.Fatalf("split usage not joined: %+v", h)
	}
}

func TestFlushStreamTokenCarryWithoutNewline(t *testing.T) {
	var h TokenHarvest
	var carry string
	ApplyStreamChunkTokens(&h, []byte(`data: {"usage":{"prompt_tokens":1,"completion_tokens":2}}`), &carry)
	if !h.Empty() {
		t.Fatalf("no newline yet, got %+v", h)
	}
	FlushStreamTokenCarry(&h, &carry)
	if h.Empty() || *h.InputTokens != 1 || *h.OutputTokens != 2 {
		t.Fatalf("flush leftover: %+v", h)
	}
}

func TestTokensFromResponseCompletedSSE(t *testing.T) {
	body := []byte("event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":9}}}\n\n")
	h := TokensFromResponse(body)
	if h.Empty() || h.InputTokens == nil || *h.InputTokens != 2 || *h.OutputTokens != 9 {
		t.Fatalf("sse harvest=%+v", h)
	}
}

func TestParseTimingsKeepsPromptMetricsWithoutDecodeTokens(t *testing.T) {
	timings, ok := parseTimingsJSON(`{"timings":{"prompt_n":0,"prompt_ms":12.5,"prompt_per_second":80}}`)
	if !ok || !timings.PromptNPresent || !timings.PromptMSPresent || !timings.PromptPerSecondPresent {
		t.Fatalf("prompt timings lost: %+v, ok=%v", timings, ok)
	}
	if timings.PromptN != 0 || timings.PromptMS != 12.5 || timings.PromptPerSecond != 80 {
		t.Fatalf("unexpected prompt timings: %+v", timings)
	}
}

func TestParseTimingsRejectsInvalidValuesIndividually(t *testing.T) {
	timings, ok := parseTimingsJSON(`{"timings":{"prompt_n":-1,"predicted_n":2,"prompt_ms":-3,"predicted_ms":4,"predicted_per_second":0}}`)
	if !ok || timings.PromptNPresent || timings.PromptMSPresent || !timings.PredictedNPresent ||
		!timings.PredictedPerSecondPresent || timings.PredictedPerSecond != 0 {
		t.Fatalf("invalid timings handling: %+v, ok=%v", timings, ok)
	}
	if _, ok := parseTimingsJSON(`{"timings":{"prompt_ms":"bad","predicted_per_second":null}}`); ok {
		t.Fatal("malformed timings should not be accepted")
	}
	if _, ok := parseTimingsJSON(`{"timings":{"prompt_ms":1e999}}`); ok {
		t.Fatal("overflowing timings should not be accepted")
	}
	if math.IsNaN(timings.PredictedPerSecond) || math.IsInf(timings.PredictedPerSecond, 0) {
		t.Fatal("non-finite timings escaped validation")
	}
}

func TestTokensFromResponseKeepsUsageAndTimingsSeparately(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30},"timings":{"prompt_n":11,"predicted_n":21,"prompt_ms":5.5,"predicted_ms":2.5,"prompt_per_second":2,"predicted_per_second":8}}`)
	h := TokensFromResponse(body)
	if h.TokenSource != "upstream_usage" || h.SpeedSource != "llama_timings" {
		t.Fatalf("sources mixed: %+v", h)
	}
	if h.InputTokens == nil || *h.InputTokens != 10 || h.OutputTokens == nil || *h.OutputTokens != 20 {
		t.Fatalf("usage counts should retain precedence: %+v", h)
	}
	if h.PromptN == nil || *h.PromptN != 11 || h.PredictedN == nil || *h.PredictedN != 21 ||
		h.PromptMS == nil || *h.PromptMS != 5.5 || h.PredictedMS == nil || *h.PredictedMS != 2.5 ||
		h.PromptPerSecond == nil || *h.PromptPerSecond != 2 || h.PredictedPerSec == nil || *h.PredictedPerSec != 8 {
		t.Fatalf("timings metrics not retained: %+v", h)
	}
}

func TestTokensFromResponseTimingsOnlyAndZeroCounts(t *testing.T) {
	h := TokensFromResponse([]byte(`{"timings":{"prompt_n":0,"predicted_n":0,"prompt_ms":1}}`))
	if h.TokenSource != "llama_timings" || h.InputTokens == nil || h.OutputTokens == nil ||
		*h.InputTokens != 0 || *h.OutputTokens != 0 || h.PromptMS == nil || *h.PromptMS != 1 {
		t.Fatalf("zero counts or prompt timings lost: %+v", h)
	}
	if h.SpeedSource != "" || h.PredictedPerSec != nil {
		t.Fatalf("missing decode speed must stay unknown: %+v", h)
	}
}

func TestTokensFromResponseSSETimingsWithoutFinalNewline(t *testing.T) {
	body := []byte("data: {\"timings\":{\"prompt_ms\":3,\"predicted_ms\":4,\"predicted_per_second\":5}}")
	h := TokensFromResponse(body)
	if h.PromptMS == nil || *h.PromptMS != 3 || h.PredictedMS == nil || *h.PredictedMS != 4 ||
		h.PredictedPerSec == nil || *h.PredictedPerSec != 5 || h.SpeedSource != "llama_timings" {
		t.Fatalf("last SSE timings not harvested: %+v", h)
	}
}

func TestTokenHarvestKeepsZeroUsageValues(t *testing.T) {
	var h TokenHarvest
	h.mergeUsage(0, 0, 0)
	if h.InputTokens == nil || *h.InputTokens != 0 || h.OutputTokens == nil || *h.OutputTokens != 0 || h.TotalTokens == nil || *h.TotalTokens != 0 {
		t.Fatalf("zero usage values must remain observable: %+v", h)
	}
	if h.TokenSource != "upstream_usage" {
		t.Fatalf("unexpected source: %q", h.TokenSource)
	}
}

func TestTokenHarvestKeepsTimingFieldsWhenDecodeSpeedIsZero(t *testing.T) {
	var h TokenHarvest
	h.mergeTimings(Timings{
		PromptN: 12, PromptNPresent: true, PredictedN: 0, PredictedNPresent: true,
		PromptMS: 3, PromptMSPresent: true, PredictedMS: 4, PredictedMSPresent: true,
		PromptPerSecond: 400, PromptPerSecondPresent: true, PredictedPerSecond: 0, PredictedPerSecondPresent: true,
	})
	if h.PromptN == nil || h.PredictedN == nil || h.PredictedMS == nil || h.PromptPerSecond == nil || h.PredictedPerSecond == nil {
		t.Fatalf("timings fields were dropped: %+v", h)
	}
	if h.SpeedSource != "" || h.PredictedPerSec != nil {
		t.Fatalf("zero Decode speed must not be treated as measured speed: %+v", h)
	}
}
