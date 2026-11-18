package llama

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultMetricsResponseLimit int64 = 4 << 20

var ErrMetricsResponseTooLarge = errors.New("llama.cpp metrics response exceeds size limit")

// MetricType 保留 Prometheus 暴露的原始类型；未知类型不会被推断为 Gauge 或 Counter。
type MetricType string

const (
	MetricTypeUnknown   MetricType = "unknown"
	MetricTypeCounter   MetricType = "counter"
	MetricTypeGauge     MetricType = "gauge"
	MetricTypeHistogram MetricType = "histogram"
	MetricTypeSummary   MetricType = "summary"
	MetricTypeUntyped   MetricType = "untyped"
)

// MetricSample 是一次 /metrics 抓取中的单个样本。Name、Type、Help 和 Labels 均来自上游。
type MetricSample struct {
	Name      string            `json:"name"`
	Type      MetricType        `json:"type"`
	Help      string            `json:"help,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     float64           `json:"value"`
	Timestamp *int64            `json:"timestamp_ms,omitempty"`
}

// MetricsSnapshot 保留动态发现的指标，不依赖编译时固定指标清单。
type MetricsSnapshot struct {
	SampledAt time.Time      `json:"sampled_at"`
	Samples   []MetricSample `json:"samples"`
}

// FetchMetrics 读取 llama.cpp /metrics。调用方应为 ctx 设置独立短超时。
// modelID 仅在 router mode 且已明确目标模型时传入；空值不擅自猜测默认模型。
func (c *Client) FetchMetrics(ctx context.Context, modelID string, maxBytes int64) (int, []byte, error) {
	if c == nil || c.IsOllama() {
		return 0, nil, errors.New("llama.cpp metrics is unavailable for this backend")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMetricsResponseLimit
	}
	path := "/metrics"
	if strings.TrimSpace(modelID) != "" {
		path += "?model=" + url.QueryEscape(strings.TrimSpace(modelID))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return 0, nil, err
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(body)) > maxBytes {
		return resp.StatusCode, nil, ErrMetricsResponseTooLarge
	}
	return resp.StatusCode, body, nil
}

// ParsePrometheusMetrics 动态解析 Prometheus text exposition format。
// 未声明 TYPE 的样本标记为 untyped；不因指标名称后缀推断语义。
func ParsePrometheusMetrics(body []byte, sampledAt time.Time) (*MetricsSnapshot, error) {
	out := &MetricsSnapshot{SampledAt: sampledAt, Samples: make([]MetricSample, 0)}
	types := make(map[string]MetricType)
	helps := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "# HELP ") && !strings.HasPrefix(line, "# TYPE ") {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			rest := strings.TrimPrefix(line, "# HELP ")
			name, help, ok := strings.Cut(rest, " ")
			if !ok || name == "" {
				return nil, fmt.Errorf("metrics line %d: invalid HELP declaration", lineNo)
			}
			helps[name] = help
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			fields := strings.Fields(line)
			if len(fields) != 4 {
				return nil, fmt.Errorf("metrics line %d: invalid TYPE declaration", lineNo)
			}
			t := MetricType(strings.ToLower(fields[3]))
			switch t {
			case MetricTypeCounter, MetricTypeGauge, MetricTypeHistogram, MetricTypeSummary, MetricTypeUntyped:
			default:
				t = MetricTypeUnknown
			}
			types[fields[2]] = t
			continue
		}
		sample, err := parsePrometheusSample(line)
		if err != nil {
			return nil, fmt.Errorf("metrics line %d: %w", lineNo, err)
		}
		sample.Type = types[sample.Name]
		if sample.Type == "" {
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				if strings.HasSuffix(sample.Name, suffix) {
					sample.Type = types[strings.TrimSuffix(sample.Name, suffix)]
					if sample.Type != "" { break }
				}
			}
		}
		if sample.Type == "" {
			sample.Type = MetricTypeUntyped
		}
		sample.Help = helps[sample.Name]
		if sample.Help == "" {
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				if strings.HasSuffix(sample.Name, suffix) {
					sample.Help = helps[strings.TrimSuffix(sample.Name, suffix)]
					if sample.Help != "" { break }
				}
			}
		}
		out.Samples = append(out.Samples, sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read metrics exposition: %w", err)
	}
	if len(out.Samples) == 0 {
		return nil, errors.New("metrics exposition contains no samples")
	}
	return out, nil
}

func parsePrometheusSample(line string) (MetricSample, error) {
	var out MetricSample
	i := sampleIdentityEnd(line)
	if i <= 0 || i >= len(line) {
		return out, errors.New("missing sample value")
	}
	identity := line[:i]
	rest := strings.TrimSpace(line[i:])
	if identity == "" || rest == "" {
		return out, errors.New("invalid sample")
	}
	labelStart := strings.IndexByte(identity, '{')
	if labelStart >= 0 {
		if !strings.HasSuffix(identity, "}") {
			return out, errors.New("unterminated label set")
		}
		out.Name = identity[:labelStart]
		labels, err := parsePrometheusLabels(identity[labelStart+1 : len(identity)-1])
		if err != nil {
			return out, err
		}
		out.Labels = labels
	} else {
		out.Name = identity
	}
	if out.Name == "" {
		return out, errors.New("empty metric name")
	}
	fields := strings.Fields(rest)
	if len(fields) < 1 || len(fields) > 2 {
		return out, errors.New("invalid sample value or timestamp")
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return out, fmt.Errorf("invalid sample value: %w", err)
	}
	out.Value = value
	if len(fields) == 2 {
		ts, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return out, fmt.Errorf("invalid sample timestamp: %w", err)
		}
		out.Timestamp = &ts
	}
	return out, nil
}

func sampleIdentityEnd(line string) int {
	inLabels, inQuotes, escaped := false, false, false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if inQuotes {
			if escaped { escaped = false; continue }
			if ch == '\\' { escaped = true; continue }
			if ch == '"' { inQuotes = false }
			continue
		}
		switch ch {
		case '"':
			if inLabels { inQuotes = true }
		case '{':
			inLabels = true
		case '}':
			inLabels = false
		case ' ', '\t':
			if !inLabels { return i }
		}
	}
	return -1
}

func parsePrometheusLabels(input string) (map[string]string, error) {
	labels := make(map[string]string)
	for i := 0; i < len(input); {
		for i < len(input) && (input[i] == ' ' || input[i] == ',') { i++ }
		if i == len(input) { break }
		start := i
		for i < len(input) && input[i] != '=' && input[i] != ',' { i++ }
		if i >= len(input) || input[i] != '=' { return nil, errors.New("invalid label assignment") }
		key := strings.TrimSpace(input[start:i])
		i++
		if key == "" || i >= len(input) || input[i] != '"' { return nil, errors.New("invalid label value") }
		i++
		var value strings.Builder
		closed := false
		for i < len(input) {
			ch := input[i]
			i++
			if ch == '"' { closed = true; break }
			if ch == '\\' {
				if i >= len(input) { return nil, errors.New("invalid label escape") }
				esc := input[i]; i++
				switch esc {
				case 'n': value.WriteByte('\n')
				case '"', '\\': value.WriteByte(esc)
				default: return nil, errors.New("unsupported label escape")
				}
				continue
			}
			value.WriteByte(ch)
		}
		if !closed { return nil, errors.New("unterminated label value") }
		if _, exists := labels[key]; exists { return nil, errors.New("duplicate label") }
		labels[key] = value.String()
		for i < len(input) && input[i] == ' ' { i++ }
		if i < len(input) && input[i] != ',' { return nil, errors.New("invalid label separator") }
	}
	return labels, nil
}
