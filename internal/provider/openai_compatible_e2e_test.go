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

// TestOpenAICompatibleProviderRealEndpointE2E 是显式 opt-in 的真实供应商探测。
// 默认不访问网络；API Key 只从本地环境变量读取，不打印、不写日志、不提交到仓库。
func TestOpenAICompatibleProviderRealEndpointE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("LLM_GATEWAY_PROVIDER_E2E")) != "1" {
		t.Skip("real Provider E2E is opt-in; set LLM_GATEWAY_PROVIDER_E2E=1 to enable")
	}

	baseURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_E2E_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("LLM_GATEWAY_E2E_API_KEY"))
	modelID := strings.TrimSpace(os.Getenv("LLM_GATEWAY_E2E_MODEL"))
	apiKeyHeader := strings.TrimSpace(os.Getenv("LLM_GATEWAY_E2E_API_KEY_HEADER"))
	if baseURL == "" || apiKey == "" || modelID == "" {
		t.Fatal("E2E enabled but required variables LLM_GATEWAY_E2E_BASE_URL, LLM_GATEWAY_E2E_API_KEY and LLM_GATEWAY_E2E_MODEL are not all set")
	}
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || parsedBaseURL == nil || !strings.EqualFold(parsedBaseURL.Scheme, "https") ||
		parsedBaseURL.Host == "" || parsedBaseURL.User != nil || parsedBaseURL.RawQuery != "" ||
		parsedBaseURL.Fragment != "" {
		t.Fatal("LLM_GATEWAY_E2E_BASE_URL must be an HTTPS base URL without user info, query or fragment")
	}

	remote, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		ID: "real-provider-e2e", BaseURL: baseURL, APIKey: apiKey,
		APIKeyHeader: apiKeyHeader,
	})
	if err != nil {
		t.Fatalf("construct real Provider E2E client from environment: %v", err)
	}

	// 只请求一个极短的 Chat Completions 响应；将调用方 model 设置为明显的占位值，
	// 以便此用例同时验证 Provider 服务端 model 覆盖。请求体不包含任何真实用户数据。
	body := []byte("{\"model\":\"client-model-must-be-overridden\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with a brief acknowledgement.\"}],\"max_tokens\":8,\"temperature\":0}")
	request := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/v1/chat/completions", nil)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "llm-gateway-opt-in-provider-e2e")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	writer := httptest.NewRecorder()
	result, forwardErr := remote.Forward(ctx, InferenceCall{
		Writer: writer, Request: request, Path: "/v1/chat/completions",
		Body: body, ModelID: modelID,
	})
	if forwardErr != nil || result.Status != http.StatusOK || writer.Code != http.StatusOK {
		// 不打印原始 error/body，避免未来某个供应商在错误消息里返回不必要的诊断信息。
		t.Fatalf("real Provider E2E failed (HTTP status=%d, writer status=%d); upstream details are intentionally omitted", result.Status, writer.Code)
	}

	var response struct {
		Model   string            `json:"model"`
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal("real Provider returned a non-JSON Chat Completions response")
	}
	if len(response.Choices) == 0 {
		t.Fatal("real Provider returned no Chat Completions choices")
	}
	t.Logf("real Provider E2E succeeded: HTTP 200, response_bytes=%d", result.ResponseBytes)
}

// TestOpenAIResponsesRealEndpointE2E 是显式 opt-in 的真实 Responses API 探测。
// 默认不访问网络；复用本地 E2E 环境变量，但使用独立开关，避免无意请求可能计费的 Responses 模型。
func TestOpenAIResponsesRealEndpointE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("LLM_GATEWAY_RESPONSES_E2E")) != "1" {
		t.Skip("Responses API E2E is opt-in; set LLM_GATEWAY_RESPONSES_E2E=1 to enable")
	}

	baseURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_E2E_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("LLM_GATEWAY_E2E_API_KEY"))
	modelID := strings.TrimSpace(os.Getenv("LLM_GATEWAY_E2E_MODEL"))
	apiKeyHeader := strings.TrimSpace(os.Getenv("LLM_GATEWAY_E2E_API_KEY_HEADER"))
	if baseURL == "" || apiKey == "" || modelID == "" {
		t.Fatal("Responses E2E enabled but required LLM_GATEWAY_E2E_BASE_URL, LLM_GATEWAY_E2E_API_KEY and LLM_GATEWAY_E2E_MODEL variables are not all set")
	}
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || parsedBaseURL == nil || !strings.EqualFold(parsedBaseURL.Scheme, "https") ||
		parsedBaseURL.Host == "" || parsedBaseURL.User != nil || parsedBaseURL.RawQuery != "" ||
		parsedBaseURL.Fragment != "" {
		t.Fatal("LLM_GATEWAY_E2E_BASE_URL must be an HTTPS base URL without user info, query or fragment")
	}

	remote, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		ID: "real-responses-e2e", BaseURL: baseURL, APIKey: apiKey, APIKeyHeader: apiKeyHeader,
	})
	if err != nil {
		t.Fatal("could not construct Responses E2E Provider from environment")
	}
	body, err := json.Marshal(map[string]interface{}{
		"model": "client-model-must-be-overridden",
		"input": "Reply with a brief acknowledgement.",
		"max_output_tokens": 8,
	})
	if err != nil {
		t.Fatal("could not build Responses E2E request")
	}

	request := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/v1/responses", nil)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "llm-gateway-opt-in-responses-e2e")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	writer := httptest.NewRecorder()
	result, forwardErr := remote.Forward(ctx, InferenceCall{
		Writer: writer, Request: request, Path: "/v1/responses",
		Body: body, ModelID: modelID,
	})
	if forwardErr != nil || result.Status != http.StatusOK || writer.Code != http.StatusOK {
		t.Fatalf("Responses E2E failed (HTTP status=%d, writer status=%d); upstream details are intentionally omitted", result.Status, writer.Code)
	}

	var response map[string]json.RawMessage
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal("Responses E2E returned a non-JSON response")
	}
	var responseID, objectType, status string
	var output []json.RawMessage
	_ = json.Unmarshal(response["id"], &responseID)
	_ = json.Unmarshal(response["object"], &objectType)
	_ = json.Unmarshal(response["status"], &status)
	_ = json.Unmarshal(response["output"], &output)
	if responseID == "" || objectType != "response" || status != "completed" || len(output) == 0 {
		t.Fatalf("Responses E2E returned an unexpected envelope (object=%q status=%q output_count=%d)", objectType, status, len(output))
	}
	t.Logf("Responses E2E succeeded: HTTP 200, response_bytes=%d", result.ResponseBytes)
}
