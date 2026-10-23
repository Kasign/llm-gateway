package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAnthropicMessagesRealEndpointE2E 是显式 opt-in 的真实 Anthropic Messages 探测。
// 默认跳过网络请求；API Key 仅从本地环境变量读取，不打印、不写日志、不提交到仓库。
func TestAnthropicMessagesRealEndpointE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANTHROPIC_E2E")) != "1" {
		t.Skip("Anthropic Messages E2E is opt-in; set LLM_GATEWAY_ANTHROPIC_E2E=1 to enable")
	}

	baseURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANTHROPIC_E2E_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANTHROPIC_E2E_API_KEY"))
	modelID := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANTHROPIC_E2E_MODEL"))
	if baseURL == "" || apiKey == "" || modelID == "" {
		t.Fatal("Anthropic E2E enabled but required BASE_URL, API_KEY and MODEL variables are not all set")
	}
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || parsedBaseURL == nil || !strings.EqualFold(parsedBaseURL.Scheme, "https") ||
		parsedBaseURL.Host == "" || parsedBaseURL.User != nil || parsedBaseURL.RawQuery != "" ||
		parsedBaseURL.Fragment != "" {
		t.Fatal("LLM_GATEWAY_ANTHROPIC_E2E_BASE_URL must be an HTTPS base URL without user info, query or fragment")
	}

	remote, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		ID: "anthropic-real-e2e", BaseURL: baseURL, APIKey: apiKey, APIKeyHeader: "x-api-key",
	})
	if err != nil {
		t.Fatal("could not construct Anthropic E2E Provider from environment")
	}

	body, err := json.Marshal(map[string]interface{}{
		"model": "client-model-must-be-overridden",
		"max_tokens": 12,
		"stream": false,
		"messages": []map[string]string{{
			"role": "user", "content": "Reply with a brief acknowledgement.",
		}},
	})
	if err != nil {
		t.Fatal("could not build Anthropic E2E request")
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/v1/messages", nil)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Anthropic-Version", "2023-06-01")
	request.Header.Set("User-Agent", "llm-gateway-opt-in-anthropic-e2e")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	writer := httptest.NewRecorder()
	result, forwardErr := remote.Forward(ctx, InferenceCall{
		Writer: writer, Request: request, Path: "/v1/messages",
		Body: body, ModelID: modelID, Stream: false,
	})
	if forwardErr != nil || result.Status != http.StatusOK || writer.Code != http.StatusOK {
		// 不输出上游原始错误或响应正文，避免供应商诊断意外包含敏感信息。
		t.Fatalf("Anthropic Messages E2E failed (HTTP status=%d, writer status=%d); upstream details are intentionally omitted", result.Status, writer.Code)
	}

	var response map[string]json.RawMessage
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal("Anthropic E2E returned a non-JSON Messages response")
	}
	var responseType, role string
	var content []json.RawMessage
	_ = json.Unmarshal(response["type"], &responseType)
	_ = json.Unmarshal(response["role"], &role)
	_ = json.Unmarshal(response["content"], &content)
	if responseType != "message" || role != "assistant" || len(content) == 0 {
		t.Fatalf("Anthropic E2E returned an unexpected Messages envelope (type=%q role=%q content_blocks=%d)", responseType, role, len(content))
	}
	t.Logf("Anthropic Messages E2E succeeded: HTTP 200, response_bytes=%d", result.ResponseBytes)
}
