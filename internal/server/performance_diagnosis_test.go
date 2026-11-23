package server

import "testing"

func TestBuildPerformanceDiagnosisUsesNativePromptAndDecodeEvidence(t *testing.T) {
	promptN, predictedN := int64(100), int64(24)
	promptMS, predictedMS, promptPPS, predictedPPS := 125.5, 80.25, 796.8, 300.0
	got := buildPerformanceDiagnosis(analysisInput{
		ModelTimings: &RequestModelTimings{
			PromptN: &promptN, PredictedN: &predictedN,
			PromptMS: &promptMS, PredictedMS: &predictedMS,
			PromptPerSecond: &promptPPS, PredictedPerSecond: &predictedPPS,
		},
	})
	if got.Status != "observed_only" || got.PrimaryBottleneck != nil || len(got.Candidates) != 2 {
		t.Fatalf("expected two candidates without a primary bottleneck: %+v", got)
	}
	if got.Candidates[0].Category != "prompt_eval" || got.Candidates[1].Category != "decode" {
		t.Fatalf("unexpected candidate order/categories: %+v", got.Candidates)
	}
	if got.Candidates[0].Evidence[0].Value != promptMS || got.Candidates[1].Evidence[0].Value != predictedMS {
		t.Fatalf("native timings were not retained: %+v", got.Candidates)
	}
}

func TestBuildPerformanceDiagnosisRequiresMeasuredQueueWait(t *testing.T) {
	wait := int64(125)
	got := buildPerformanceDiagnosis(analysisInput{
		ProviderQueueWaitMS: &wait,
		ProviderQueueWaitStatus: "granted",
	})
	if len(got.Candidates) != 1 || got.Candidates[0].Category != "provider_queue" {
		t.Fatalf("measured queue wait should be a candidate: %+v", got)
	}
	got = buildPerformanceDiagnosis(analysisInput{
		ProviderQueueWaitMS: &wait,
		ProviderQueueWaitStatus: "unknown",
	})
	if len(got.Candidates) != 0 || got.Status != "insufficient_data" {
		t.Fatalf("unknown queue status must not be treated as measured wait: %+v", got)
	}
}

func TestBuildPerformanceDiagnosisUsesDispatchAndHeadersAnchors(t *testing.T) {
	dispatch, headers := int64(100), int64(350)
	got := buildPerformanceDiagnosis(analysisInput{Timeline: []requestTimelineEvent{
		{Name: "upstream_dispatched", OffsetMS: &dispatch, Status: timelineStatusMeasured},
		{Name: "upstream_headers", OffsetMS: &headers, Status: timelineStatusMeasured},
	}})
	if len(got.Candidates) != 1 || got.Candidates[0].Category != "upstream_headers_wait" ||
		got.Candidates[0].Evidence[0].Value != 250 {
		t.Fatalf("timeline interval was not derived from paired anchors: %+v", got)
	}
	got = buildPerformanceDiagnosis(analysisInput{Timeline: []requestTimelineEvent{
		{Name: "upstream_dispatched", OffsetMS: &dispatch, Status: timelineStatusMeasured},
		{Name: "upstream_headers", Status: timelineStatusUnknown},
	}})
	if len(got.Candidates) != 0 {
		t.Fatalf("unknown event time must not create a duration: %+v", got)
	}
}

func TestBuildPerformanceDiagnosisDoesNotPromoteRuntimeSnapshotOrGatewayWindow(t *testing.T) {
	age := int64(900)
	cpu := 95.0
	out := int64(20)
	got := buildPerformanceDiagnosis(analysisInput{
		OutputTokens: &out,
		SpeedSource: "gateway_window",
		RuntimeSnapshot: &RequestRuntimeSnapshot{Status: "complete", HostCPUPercent: &cpu, AgeMS: age},
	})
	if got.Status != "insufficient_data" || got.PrimaryBottleneck != nil || len(got.Candidates) != 0 {
		t.Fatalf("unmatched runtime snapshot or Gateway window must not be promoted to bottleneck evidence: %+v", got)
	}
	if len(got.Limitations) < 2 {
		t.Fatalf("expected explicit scope limitations: %+v", got.Limitations)
	}
}

func TestBuildPerformanceDiagnosisDoesNotPromoteSingleRequestToPrimary(t *testing.T) {
	wait := int64(250)
	got := buildPerformanceDiagnosis(analysisInput{ProviderQueueWaitMS: &wait, ProviderQueueWaitStatus: "granted"})
	if got.PrimaryBottleneck != nil {
		t.Fatalf("single request cannot establish a primary bottleneck: %+v", got)
	}
	if got.Limitations[0] == "" {
		t.Fatalf("missing baseline/SLA limitation: %+v", got)
	}
}

func TestBuildRequestAnalysisIncludesPerformanceDiagnosisAndLegacyJSONRemainsReadable(t *testing.T) {
	promptN := int64(64)
	promptMS := 50.0
	got := buildRequestAnalysis(analysisInput{ModelTimings: &RequestModelTimings{PromptN: &promptN, PromptMS: &promptMS}})
	raw := encodeRequestAnalysis(got)
	decoded, err := decodeRequestAnalysis(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PerformanceDiagnosis == nil || decoded.PerformanceDiagnosis.Status != "observed_only" {
		t.Fatalf("performance diagnosis missing after persistence round trip: %s", raw)
	}
	legacy, err := decodeRequestAnalysis(`{"phases_ms":{"total_ms":1},"throughput":{}}`)
	if err != nil || legacy == nil || legacy.PerformanceDiagnosis != nil {
		t.Fatalf("legacy RequestAnalysis should remain readable: %+v, %v", legacy, err)
	}
}
