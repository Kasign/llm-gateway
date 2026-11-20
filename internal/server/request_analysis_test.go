package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/logutil"
)

func TestBuildRequestAnalysisSeparatesTokenAndSpeedSources(t *testing.T) {
	out := int64(24)
	promptN, predictedN := int64(100), int64(24)
	promptMS, predictedMS, promptPPS, predictedPPS := 125.5, 80.25, 796.8, 300.0
	a := buildRequestAnalysis(analysisInput{
		OutputTokens: &out, TokenSource: "upstream_usage", SpeedSource: "llama_timings",
		PredictedPerSec: &predictedPPS,
		ModelTimings: &RequestModelTimings{
			PromptN: &promptN, PredictedN: &predictedN, PromptMS: &promptMS,
			PredictedMS: &predictedMS, PromptPerSecond: &promptPPS, PredictedPerSecond: &predictedPPS,
		},
	})
	if a.Throughput.TokenSource != "upstream_usage" || a.Throughput.SpeedSource != "llama_timings" {
		t.Fatalf("sources were conflated: %+v", a.Throughput)
	}
	if a.Throughput.OutputTokensPerSec == nil || *a.Throughput.OutputTokensPerSec != predictedPPS {
		t.Fatalf("Decode speed missing: %+v", a.Throughput)
	}
	if a.Throughput.GenerationWindowMS != 0 {
		t.Fatalf("llama Decode speed must not claim a Gateway window: %+v", a.Throughput)
	}
	if a.Throughput.ModelTimings == nil || a.Throughput.ModelTimings.PromptMS == nil ||
		*a.Throughput.ModelTimings.PromptMS != promptMS || a.Throughput.ModelTimings.PredictedMS == nil ||
		*a.Throughput.ModelTimings.PredictedMS != predictedMS {
		t.Fatalf("raw model timings were not retained: %+v", a.Throughput.ModelTimings)
	}
}

func TestBuildRequestAnalysisDoesNotUseNonStreamUpstreamAsDecodeWindow(t *testing.T) {
	out := int64(50)
	a := buildRequestAnalysis(analysisInput{
		TotalMS: 5000, UpstreamMS: 4000, OutputTokens: &out, TokenSource: "upstream_usage",
	})
	if a.Throughput.OutputTokensPerSec != nil || a.Throughput.GenerationWindowMS != 0 {
		t.Fatalf("upstream_ms must not be treated as a Decode window: %+v", a.Throughput)
	}
	if a.Throughput.SpeedSource != "" {
		t.Fatalf("unknown speed source must remain unknown: %+v", a.Throughput)
	}
	if a.Phases.ContentGenerationMS != nil {
		t.Fatalf("non-stream upstream_ms must not be reported as a measured content generation duration: %+v", a.Phases)
	}
}

func TestBuildRequestAnalysisDerivesSpeedOnlyFromStreamContentWindow(t *testing.T) {
	out := int64(50)
	first, last := int64(100), int64(2100)
	a := buildRequestAnalysis(analysisInput{
		TotalMS: 3000, UpstreamMS: 2900, Stream: true, OutputTokens: &out,
		TokenSource: "upstream_usage",
		StreamTiming: streamTimings{FirstContentMS: &first, LastEventMS: last},
	})
	if a.Throughput.SpeedSource != "gateway_window" || a.Throughput.GenerationWindowMS != 2000 ||
		a.Throughput.OutputTokensPerSec == nil || *a.Throughput.OutputTokensPerSec != 25 {
		t.Fatalf("stream window derivation incorrect: %+v", a.Throughput)
	}
}

func TestDecodeRequestAnalysisKeepsLegacySourceWithoutGuessingSpeedSource(t *testing.T) {
	a, err := decodeRequestAnalysis(`{"throughput":{"source":"llama_timings","output_tokens_per_sec":42}}`)
	if err != nil {
		t.Fatal(err)
	}
	if a.Throughput.TokenSource != "llama_timings" {
		t.Fatalf("legacy token source not retained: %+v", a.Throughput)
	}
	if len(a.Timeline) != 0 {
		t.Fatalf("legacy RequestAnalysis without timeline must remain readable without fabricating events: %+v", a.Timeline)
	}
	if a.Throughput.SpeedSource != "" {
		t.Fatalf("legacy mixed source must not be guessed as speed source: %+v", a.Throughput)
	}
}

func TestEncodeRequestAnalysisOmitsUnknownTimings(t *testing.T) {
	a := buildRequestAnalysis(analysisInput{TotalMS: 10})
	raw := encodeRequestAnalysis(a)
	if strings.Contains(raw, `"llama_timings":{}`) {
		t.Fatalf("empty timings object should be omitted: %s", raw)
	}
	var decoded RequestAnalysis
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Throughput.TokenSource != "" || decoded.Throughput.SpeedSource != "" {
		t.Fatalf("unknown sources must stay empty: %+v", decoded.Throughput)
	}
}

func TestBuildRequestAnalysisPreservesTimelineAndUnknownOffsets(t *testing.T) {
	zero := int64(0)
	events := []requestTimelineEvent{
		{Name: "request_received", OffsetMS: &zero, Status: timelineStatusMeasured},
		{Name: "body_read_finished", Status: timelineStatusNotReached},
	}
	out := buildRequestAnalysis(analysisInput{TotalMS: 10, Timeline: events})
	if len(out.Timeline) != 2 {
		t.Fatalf("timeline events were not preserved: %+v", out.Timeline)
	}
	if out.Timeline[0].OffsetMS == nil || *out.Timeline[0].OffsetMS != 0 {
		t.Fatalf("measured zero offset was lost: %+v", out.Timeline[0])
	}
	if out.Timeline[1].OffsetMS != nil || out.Timeline[1].Status != timelineStatusNotReached {
		t.Fatalf("unknown event time was fabricated: %+v", out.Timeline[1])
	}
}

func TestTimelineOnlyAnalysisDoesNotInventPhaseMetrics(t *testing.T) {
	start := time.Now()
	timeline := newRequestTimeline(start)
	timeline.record("request_received", start)
	timeline.markIfMissing("pipeline_started", timelineStatusNotReached)
	a := RequestAnalysis{TimelineOnly: true, Timeline: timeline.snapshot()}
	decoded, err := decodeRequestAnalysis(encodeRequestAnalysis(a))
	if err != nil {
		t.Fatalf("decode timeline-only analysis: %v", err)
	}
	if !decoded.TimelineOnly || len(decoded.Timeline) != 2 {
		t.Fatalf("timeline-only metadata lost: %+v", decoded)
	}
	if len(decoded.Summary) != 0 || decoded.Throughput.OutputTokensPerSec != nil {
		t.Fatalf("timeline-only record must not invent analysis metrics: %+v", decoded)
	}
	if strings.Contains(encodeRequestAnalysis(a), "phases_ms") || strings.Contains(encodeRequestAnalysis(a), "throughput") {
		t.Fatalf("timeline-only JSON must omit unavailable phase metrics: %s", encodeRequestAnalysis(a))
	}
}


func TestRequestModelTimingsPreserveCachePerTokenAndPromptProgress(t *testing.T) {
	cacheN, promptN, predictedN := int64(80), int64(20), int64(10)
	promptMS, promptPerTokenMS, predictedPerTokenMS := 100.0, 5.0, 2.5
	cached, evaluated := int64(80), int64(20)
	harvest := logutil.TokenHarvest{
		CacheN: &cacheN, PromptN: &promptN, PredictedN: &predictedN,
		TokensCached: &cached, UsageCachedTokens: &cached, TokensEvaluated: &evaluated,
		PromptMS: &promptMS, PromptPerTokenMS: &promptPerTokenMS, PredictedPerTokenMS: &predictedPerTokenMS,
		PromptProgress: []logutil.PromptProgressSample{{Total: 100, Cache: 80, Processed: 90, TimeMS: 500}},
	}
	got := requestModelTimings(harvest)
	if got == nil || got.CacheN == nil || *got.CacheN != 80 ||
		got.PromptPerTokenMS == nil || *got.PromptPerTokenMS != 5 ||
		got.PredictedPerTokenMS == nil || *got.PredictedPerTokenMS != 2.5 ||
		got.TokensCached == nil || *got.TokensCached != 80 ||
		got.UsageCachedTokens == nil || *got.UsageCachedTokens != 80 ||
		got.TokensEvaluated == nil || *got.TokensEvaluated != 20 ||
		len(got.PromptProgress) != 1 || got.PromptProgress[0].Processed != 90 {
		t.Fatalf("extended model timings lost: %+v", got)
	}
	raw := encodeRequestAnalysis(buildRequestAnalysis(analysisInput{ModelTimings: got}))
	var decoded RequestAnalysis
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Throughput.ModelTimings == nil || len(decoded.Throughput.ModelTimings.PromptProgress) != 1 ||
		decoded.Throughput.ModelTimings.PromptProgress[0].TimeMS != 500 {
		t.Fatalf("extended model timings did not round-trip: %s", raw)
	}
}
