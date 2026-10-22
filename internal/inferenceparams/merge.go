package inferenceparams

import (
	"encoding/json"
	"strings"
)

// ApplyChat merges gateway Params into a chat/responses JSON object map.
func ApplyChat(mode string, defs Params, body map[string]interface{}) {
	if body == nil {
		return
	}
	gatewayOnly := mode == ModeGatewayOnly
	setFloat := func(key string, v *float64) {
		if v == nil {
			return
		}
		if !gatewayOnly {
			if _, ok := body[key]; ok {
				return
			}
		}
		body[key] = *v
	}
	setInt := func(key string, v *int) {
		if v == nil {
			return
		}
		if !gatewayOnly {
			if _, ok := body[key]; ok {
				return
			}
		}
		body[key] = *v
	}
	setFloat("temperature", defs.Temperature)
	setFloat("top_p", defs.TopP)
	setInt("top_k", defs.TopK)
	setFloat("min_p", defs.MinP)
	setFloat("frequency_penalty", defs.FrequencyPenalty)
	setFloat("presence_penalty", defs.PresencePenalty)
	setFloat("repeat_penalty", defs.RepeatPenalty)
	setInt("max_tokens", defs.MaxTokens)
	setInt("seed", defs.Seed)
	if len(defs.Stop) > 0 {
		if gatewayOnly || body["stop"] == nil {
			body["stop"] = defs.Stop
		}
	}
	if defs.MaxOutputCap != nil && *defs.MaxOutputCap > 0 {
		capOutput(body, "max_tokens", *defs.MaxOutputCap)
		capOutput(body, "max_output_tokens", *defs.MaxOutputCap)
	}
}

// thinkingChoice 是把多种思考字段收成的一个开关。
// 开启和关闭同时出现时按开启：留下的开启字段仍会让上游进入思考。
type thinkingChoice int

const (
	thinkingUnset thinkingChoice = iota
	thinkingOn
	thinkingOff
)

// applyResolvedThinking 与采样共用合并策略，再把结果写回同一份请求体。
// 配置为透传时不改这些字段。上游是否支持思考不在这里判断。
func applyResolvedThinking(backend, mode string, configured *string, body map[string]interface{}) {
	if body == nil {
		return
	}
	choice := resolveThinking(mode, configured, clientThinking(body))
	switch choice {
	case thinkingOn:
		applyThinkingOn(backend, body)
	case thinkingOff:
		applyThinkingOff(backend, body)
	}
}

// resolveThinking 的规则与 temperature 相同：仅网关配置时用配置；客户端优先时，客户端写了就用客户端的。
func resolveThinking(mode string, configured *string, client thinkingChoice) thinkingChoice {
	cfg, ok := configuredThinking(configured)
	if !ok {
		return thinkingUnset
	}
	if mode == ModeGatewayOnly {
		return cfg
	}
	if client != thinkingUnset {
		return client
	}
	return cfg
}

func configuredThinking(configured *string) (thinkingChoice, bool) {
	if configured == nil {
		return thinkingUnset, false
	}
	switch strings.ToLower(strings.TrimSpace(*configured)) {
	case "true":
		return thinkingOn, true
	case "false":
		return thinkingOff, true
	default:
		return thinkingUnset, false
	}
}

func clientThinking(body map[string]interface{}) thinkingChoice {
	if thinkingFieldsOn(body) {
		return thinkingOn
	}
	if thinkingFieldsOff(body) {
		return thinkingOff
	}
	return thinkingUnset
}

func thinkingFieldsOn(body map[string]interface{}) bool {
	if _, ok := body["reasoning"].(map[string]interface{}); ok {
		return true
	}
	if effort, ok := asString(body["reasoning_effort"]); ok && !strings.EqualFold(effort, "none") {
		return true
	}
	switch thinkingType(body) {
	case "enabled", "adaptive":
		return true
	}
	if on, ok := thinkValue(body["think"]); ok && on {
		return true
	}
	if on, ok := asBool(body["enable_thinking"]); ok && on {
		return true
	}
	if on, ok := templateEnableThinking(body); ok && on {
		return true
	}
	return includeHasReasoning(body)
}

func thinkingFieldsOff(body map[string]interface{}) bool {
	if on, ok := asBool(body["enable_thinking"]); ok && !on {
		return true
	}
	if on, ok := templateEnableThinking(body); ok && !on {
		return true
	}
	if on, ok := thinkValue(body["think"]); ok && !on {
		return true
	}
	if thinkingType(body) == "disabled" {
		return true
	}
	effort, ok := asString(body["reasoning_effort"])
	return ok && strings.EqualFold(effort, "none")
}

func applyThinkingOn(backend string, body map[string]interface{}) {
	// 最终开启时，明确的关闭值会把思考关掉，先去掉。客户端自己的开启字段保留。
	clearThinkingOffMarkers(body)
	if thinkingFieldsOn(body) {
		return
	}
	if isOllamaBackend(backend) {
		body["think"] = true
		return
	}
	body["enable_thinking"] = true
}

func applyThinkingOff(backend string, body map[string]interface{}) {
	// 最终关闭时，这些字段都会让上游打开思考，必须一起去掉。
	delete(body, "reasoning")
	delete(body, "reasoning_effort")
	delete(body, "thinking")
	delete(body, "think")
	delete(body, "enable_thinking")
	stripReasoningInclude(body)
	deleteTemplateEnableThinking(body)
	if !isOllamaBackend(backend) {
		body["enable_thinking"] = false
	}
}

func clearThinkingOffMarkers(body map[string]interface{}) {
	if effort, ok := asString(body["reasoning_effort"]); ok && strings.EqualFold(effort, "none") {
		delete(body, "reasoning_effort")
	}
	if thinkingType(body) == "disabled" {
		delete(body, "thinking")
	}
	if on, ok := thinkValue(body["think"]); ok && !on {
		delete(body, "think")
	}
	if on, ok := asBool(body["enable_thinking"]); ok && !on {
		delete(body, "enable_thinking")
	}
	if on, ok := templateEnableThinking(body); ok && !on {
		deleteTemplateEnableThinking(body)
	}
}

func isOllamaBackend(backend string) bool {
	return strings.EqualFold(strings.TrimSpace(backend), "ollama")
}

func thinkingType(body map[string]interface{}) string {
	obj, ok := body["thinking"].(map[string]interface{})
	if !ok {
		return ""
	}
	t, ok := asString(obj["type"])
	if !ok {
		return ""
	}
	return strings.ToLower(t)
}

func templateEnableThinking(body map[string]interface{}) (bool, bool) {
	obj, ok := body["chat_template_kwargs"].(map[string]interface{})
	if !ok {
		return false, false
	}
	return asBool(obj["enable_thinking"])
}

func deleteTemplateEnableThinking(body map[string]interface{}) {
	obj, ok := body["chat_template_kwargs"].(map[string]interface{})
	if !ok {
		return
	}
	delete(obj, "enable_thinking")
	if len(obj) == 0 {
		delete(body, "chat_template_kwargs")
	}
}

func includeHasReasoning(body map[string]interface{}) bool {
	raw, ok := body["include"].([]interface{})
	if !ok {
		return false
	}
	for _, item := range raw {
		s, ok := asString(item)
		if ok && isReasoningInclude(s) {
			return true
		}
	}
	return false
}

func isReasoningInclude(s string) bool {
	s = strings.TrimSpace(s)
	return s == "reasoning" || strings.HasPrefix(s, "reasoning.")
}

func thinkValue(v interface{}) (bool, bool) {
	if on, ok := asBool(v); ok {
		return on, true
	}
	s, ok := asString(v)
	if !ok {
		return false, false
	}
	if strings.EqualFold(s, "false") {
		return false, true
	}
	return true, true
}

func asBool(v interface{}) (bool, bool) {
	b, ok := v.(bool)
	return b, ok
}

func asString(v interface{}) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return s, true
}

func stripReasoningInclude(body map[string]interface{}) {
	raw, ok := body["include"].([]interface{})
	if !ok {
		return
	}
	kept := make([]interface{}, 0, len(raw))
	for _, item := range raw {
		if s, ok := asString(item); ok && isReasoningInclude(s) {
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == 0 {
		delete(body, "include")
		return
	}
	body["include"] = kept
}

func capOutput(body map[string]interface{}, key string, capMax int) {
	switch n := body[key].(type) {
	case float64:
		if int(n) > capMax {
			body[key] = capMax
		}
	case int:
		if n > capMax {
			body[key] = capMax
		}
	}
}

// MergeEffective overlays saved admin overrides onto backend factory defaults (proxy 用).
func MergeEffective(factory, saved Params) Params {
	out := factory
	if saved.MaxTokens != nil {
		out.MaxTokens = saved.MaxTokens
	}
	if saved.Temperature != nil {
		out.Temperature = saved.Temperature
	}
	if saved.TopP != nil {
		out.TopP = saved.TopP
	}
	if saved.TopK != nil {
		out.TopK = saved.TopK
	}
	if saved.MinP != nil {
		out.MinP = saved.MinP
	}
	if saved.FrequencyPenalty != nil {
		out.FrequencyPenalty = saved.FrequencyPenalty
	}
	if saved.PresencePenalty != nil {
		out.PresencePenalty = saved.PresencePenalty
	}
	if saved.RepeatPenalty != nil {
		out.RepeatPenalty = saved.RepeatPenalty
	}
	if saved.Seed != nil {
		out.Seed = saved.Seed
	}
	if len(saved.Stop) > 0 {
		out.Stop = append([]string(nil), saved.Stop...)
	}
	if saved.EnableThinking != nil {
		if *saved.EnableThinking == "" {
			out.EnableThinking = nil
		} else {
			out.EnableThinking = saved.EnableThinking
		}
	}
	if saved.KeepAlive != nil {
		out.KeepAlive = saved.KeepAlive
	}
	if saved.MaxOutputCap != nil {
		out.MaxOutputCap = saved.MaxOutputCap
	}
	return out
}

// ThinkingLabel 根据请求体里的思考字段给出展示用状态，不改 body。
func ThinkingLabel(body map[string]interface{}) string {
	switch clientThinking(body) {
	case thinkingOn:
		return "开启"
	case thinkingOff:
		return "关闭"
	default:
		return "未声明"
	}
}

// MergeChatBody 用同一套合并策略改写采样和思考，返回完整请求体。
// 主流程的 Mutate 回调只把这份结果写回 State。JSON 无法解析时原样返回。
func MergeChatBody(backend, mode string, defs Params, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var obj map[string]interface{}
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	ApplyChat(mode, defs, obj)
	applyResolvedThinking(backend, mode, defs.EnableThinking, obj)
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}
