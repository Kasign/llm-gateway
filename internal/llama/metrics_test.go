package llama

import (
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
	if first.Name != "llamacpp:tokens_predicted_total" || first.Type != MetricTypeCounter || first.Value != 42 || first.Help != "generated tokens" {
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
