package server

import (
	"encoding/json"
	"strings"

	"github.com/whl/llm-gateway/internal/inferenceparams"
	"github.com/whl/llm-gateway/internal/logutil"
	"github.com/whl/llm-gateway/internal/store"
)

var serviceParamKeys = []string{
	"max_tokens", "max_output_tokens", "temperature", "top_p", "top_k", "min_p",
	"frequency_penalty", "presence_penalty", "repeat_penalty", "seed", "stop",
}

// buildRequestParams 从已经转发给上游的 body 抽出详情「请求参数」。
// 用当时发出去的 JSON，不读管理台当前配置。
func buildRequestParams(l *store.RequestLog, body []byte) map[string]interface{} {
	out := map[string]interface{}{}
	if l != nil {
		if l.ModelRequested != "" {
			out["model_client"] = l.ModelRequested
		}
		if l.ModelActual != "" {
			out["model_upstream"] = l.ModelActual
		}
		if l.ModelRequested != "" && l.ModelActual != "" && l.ModelRequested != l.ModelActual {
			out["model_note"] = "Gateway 已忽略客户端 model，上游实际使用 model_upstream"
		}
	}
	if len(body) == 0 {
		if l != nil {
			out["stream"] = l.Stream
			out["stream_label"] = streamLabel(l.Stream, false)
		}
		return out
	}
	var obj map[string]interface{}
	if json.Unmarshal(body, &obj) != nil {
		out["parse"] = "invalid_json"
		return out
	}
	bodyStream := logutil.JSONTruthy(obj["stream"])
	effective := bodyStream
	if l != nil && l.Stream {
		effective = true
	}
	out["stream"] = effective
	out["stream_in_body"] = bodyStream
	out["stream_label"] = streamLabel(effective, !bodyStream && effective)
	if l != nil && l.Stream && !bodyStream {
		out["stream_note"] = "请求体没有 stream，按 Accept: text/event-stream 推断为流式"
	}
	out["thinking"] = inferenceparams.ThinkingLabel(obj)
	includeUsage := false
	if opts, ok := obj["stream_options"].(map[string]interface{}); ok {
		includeUsage = logutil.JSONTruthy(opts["include_usage"])
	}
	out["include_usage"] = includeUsage
	if includeUsage {
		out["include_usage_label"] = "是"
	} else {
		out["include_usage_label"] = "否"
	}
	params := map[string]interface{}{}
	for _, key := range serviceParamKeys {
		if v, ok := obj[key]; ok && v != nil {
			params[key] = v
		}
	}
	if len(params) > 0 {
		out["service_params"] = params
	}
	return out
}

func streamLabel(effective, inferred bool) string {
	if !effective {
		return "否"
	}
	if inferred {
		return "是（推断）"
	}
	return "是"
}

func tokenSourceLabel(src string) string {
	switch strings.TrimSpace(src) {
	case "usage", "upstream_usage":
		return "上游 usage"
	case "llama_timings":
		return "llama.cpp timings"
	case "":
		return "未读到"
	default:
		return src
	}
}
