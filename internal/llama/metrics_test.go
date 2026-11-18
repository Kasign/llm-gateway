package llama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParsePrometheusMetricsDiscoversUnknownMetrics(t *testing.T) {
	body := []byte("# HELP llamacpp:tokens_predicted_total generated tokens\n# TYPE llamacpp:tokens_predicted_total counter\nllamacpp:tokens_predicted_total{model=\"qwen,3\",note=\"a value\",slot=\"1\"} 42\n# TYPE new_metric gauge\nnew_metric 0.75\nunknown_metric{note=\"line\\nvalue\"} 3.5 1700000000000\n")
	sampledAt := time.Unix(1700000000, 0)
	got, err := ParsePrometheusMetrics(body, sampledAt)
	if err != nil { t.Fatal(err) }
	if len(got.Samples) != 3 { t.Fatalf("samples=%d", len(got.Samples)) }
	first := got.Samples[0]
	if first.Name != "llamacpp:tokens_predicted_total" || first.Type != MetricTypeCounter || first.Value != "42" || first.Help != "generated tokens" {
		t.Fatalf("unexpected first sample: %#v", first)
	}
	if first.Labels["model"] != "qwen,3" || first.Labels["note"] != "a value" || first.Labels["slot"] != "1" { t.Fatalf("labels: %#v", first.Labels) }
	if got.Samples[1].Type != MetricTypeGauge { t.Fatalf("type=%q", got.Samples[1].Type) }
	last := got.Samples[2]
	if last.Type != MetricTypeUntyped || last.Labels["note"] != "line\nvalue" || last.Timestamp == nil || *last.Timestamp != 1700000000000 {
		t.Fatalf("unexpected unknown sample: %#v", last)
	}
}

func TestParsePrometheusMetricsMapsHistogramSamples(t *testing.T) {
	body := []byte("# TYPE request_duration_seconds histogram\nrequest_duration_seconds_bucket{le=\"1\"} 2\nrequest_duration_seconds_sum 1.5\nrequest_duration_seconds_count 2\n")
	got, err := ParsePrometheusMetrics(body, time.Now())
	if err != nil { t.Fatal(err) }
	for _, sample := range got.Samples {
		if sample.Type != MetricTypeHistogram { t.Fatalf("%s type=%s", sample.Name, sample.Type) }
	}
}

func TestParsePrometheusMetricsRejectsMalformedSample(t *testing.T) {
	_, err := ParsePrometheusMetrics([]byte("metric{broken=\"x\" 1\n"), time.Now())
	if err == nil { t.Fatal("expected parse error") }
}

func TestParsePrometheusMetricsRejectsEmptyPayload(t *testing.T) {
	_, err := ParsePrometheusMetrics([]byte("# HELP only metadata\n"), time.Now())
	if err == nil { t.Fatal("expected empty payload error") }
}

func TestFetchMetricsUsesConfiguredAuthAndModel(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.RequestURI(), r.Header.Get("Authorization")
		w.Header().Set("Process-Start-Time-Unix", "1700000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metric 1\n"))
	}))
	defer srv.Close()
	client := New(srv.URL, 0, "llamacpp", "upstream-secret")
	status, body, processStart, err := client.FetchMetrics(context.Background(), "model A", 1024)
	if err != nil { t.Fatal(err) }
	if status != http.StatusOK || gotPath != "/metrics?model=model+A" || gotAuth != "Bearer upstream-secret" || string(body) != "metric 1\n" {
		t.Fatalf("status=%d path=%q auth=%q body=%q", status, gotPath, gotAuth, body)
	}
	if processStart == nil || *processStart != 1700000000 {
		t.Fatalf("process start time header was not captured: %v", processStart)
	}
}

func TestParseProcessStartTimeUnixRejectsMissingOrInvalidHeader(t *testing.T) {
	for _, raw := range []string{"", "unknown", "0", "-1"} {
		if got := parseProcessStartTimeUnix(raw); got != nil {
			t.Fatalf("header %q should not produce a process identity: %v", raw, *got)
		}
	}
}

func TestFetchMetricsEnforcesResponseLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer srv.Close()
	client := New(srv.URL, 0, "llamacpp", "")
	_, _, _, err := client.FetchMetrics(context.Background(), "", 4)
	if err != ErrMetricsResponseTooLarge { t.Fatalf("error=%v", err) }
}


func TestParsePrometheusMetricsPreservesSpecialAndLargeValues(t *testing.T) {
	body := []byte("large_counter 900719925474099312345\nspecial_metric NaN\n")
	got, err := ParsePrometheusMetrics(body, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Samples[0].Value != "900719925474099312345" || got.Samples[1].Value != "NaN" {
		t.Fatalf("values were not preserved exactly: %#v", got.Samples)
	}
}
