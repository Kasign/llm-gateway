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

var ErrObservationResponseTooLarge = errors.New("llama.cpp observation response exceeds size limit")
var ErrMetricsResponseTooLarge = ErrObservationResponseTooLarge

type MetricType string

const (
	MetricTypeUnknown   MetricType = "unknown"
	MetricTypeCounter   MetricType = "counter"
	MetricTypeGauge     MetricType = "gauge"
	MetricTypeHistogram MetricType = "histogram"
	MetricTypeSummary   MetricType = "summary"
	MetricTypeUntyped   MetricType = "untyped"
)

// MetricSample 保留上游原始名称、类型、帮助文本、标签和值，不猜测未知指标语义。
type MetricSample struct {
	Name      string            `json:"name"`
	Type      MetricType        `json:"type"`
	Help      string            `json:"help,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     string            `json:"value"`
	Timestamp *int64            `json:"timestamp_ms,omitempty"`
}

type MetricsSnapshot struct {
	SampledAt time.Time      `json:"sampled_at"`
	Samples   []MetricSample `json:"samples"`
}

// FetchMetrics 读取 /metrics。调用方必须设置独立短超时；响应体有硬上限。
func (c *Client) FetchMetrics(ctx context.Context, modelID string, maxBytes int64) (int, []byte, error) {
	if c == nil || c.IsOllama() {
		return 0, nil, errors.New("llama.cpp metrics is unavailable for this backend")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMetricsResponseLimit
	}
	path := "/metrics"
	if modelID = strings.TrimSpace(modelID); modelID != "" {
		path += "?model=" + url.QueryEscape(modelID)
	}
	return c.getBounded(ctx, path, maxBytes)
}

// FetchSlots 读取 /slots，但调用方不得将其加入默认轮询，除非已验证目标版本的 idle-sleep 行为。
func (c *Client) FetchSlots(ctx context.Context, maxBytes int64) (int, []byte, error) {
	if c == nil || c.IsOllama() {
		return 0, nil, errors.New("llama.cpp slots is unavailable for this backend")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultSlotsResponseLimit
	}
	return c.getBounded(ctx, "/slots", maxBytes)
}

func (c *Client) getBounded(ctx context.Context, path string, maxBytes int64) (int, []byte, error) {
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
// 标准 TYPE 声明格式为 "# TYPE <name> <type>"，未声明类型的样本标记为 untyped。
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
			fields := strings.SplitN(strings.TrimPrefix(line, "# HELP "), " ", 2)
			if len(fields) != 2 || fields[0] == "" {
				return nil, fmt.Errorf("metrics line %d: invalid HELP declaration", lineNo)
			}
			helps[fields[0]] = fields[1]
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			fields := strings.Fields(line)
			if len(fields) != 4 || fields[0] != "#" || fields[1] != "TYPE" || fields[2] == "" {
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
					if sample.Type != "" {
						break
					}
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
					if sample.Help != "" {
						break
					}
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
	fields := strings.Fields(strings.TrimSpace(line[i:]))
	if len(fields) < 1 || len(fields) > 2 {
		return out, errors.New("invalid sample value or timestamp")
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
	if _, err := strconv.ParseFloat(fields[0], 64); err != nil {
		return out, fmt.Errorf("invalid sample value: %w", err)
	}
	out.Value = fields[0]
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
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inQuotes = false
			}
			continue
		}
		switch ch {
		case '"':
			if inLabels {
				inQuotes = true
			}
		case '{':
			inLabels = true
		case '}':
			inLabels = false
		case ' ', '\t':
			if !inLabels {
				return i
			}
		}
	}
	return -1
}

func parsePrometheusLabels(input string) (map[string]string, error) {
	labels := make(map[string]string)
	for i := 0; i < len(input); {
		for i < len(input) && (input[i] == ' ' || input[i] == ',') {
			i++
		}
		if i == len(input) {
			break
		}
		start := i
		for i < len(input) && input[i] != '=' && input[i] != ',' {
			i++
		}
		if i >= len(input) || input[i] != '=' {
			return nil, errors.New("invalid label assignment")
		}
		key := strings.TrimSpace(input[start:i])
		i++
		if key == "" || i >= len(input) || input[i] != '"' {
			return nil, errors.New("invalid label value")
		}
		i++
		var value strings.Builder
		closed := false
		for i < len(input) {
			ch := input[i]
			i++
			if ch == '"' {
				closed = true
				break
			}
			if ch == '\\' {
				if i >= len(input) {
					return nil, errors.New("invalid label escape")
				}
				esc := input[i]
				i++
				switch esc {
				case 'n':
					value.WriteByte('\n')
				case '"', '\\':
					value.WriteByte(esc)
				default:
					return nil, errors.New("unsupported label escape")
				}
				continue
			}
			value.WriteByte(ch)
		}
		if !closed {
			return nil, errors.New("unterminated label value")
		}
		if _, exists := labels[key]; exists {
			return nil, errors.New("duplicate label")
		}
		labels[key] = value.String()
		for i < len(input) && input[i] == ' ' {
			i++
		}
		if i < len(input) && input[i] != ',' {
			return nil, errors.New("invalid label separator")
		}
	}
	return labels, nil
}
