package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/settings"
)

// TestAgentRoutesForwardResponsesJSONAndSSEToConfiguredProviders 验证 Responses API
// 经过 Agent 前缀归一、模型固定、Pipeline 和 Provider 转发后，仍保持双路由隔离。
func TestAgentRoutesForwardResponsesJSONAndSSEToConfiguredProviders(t *testing.T) {
	type upstreamCapture struct {
		path          string
		trace         string
		debug         string
		model         string
		input         string
		stream        bool
		providerKey   string
		authorization string
		gatewayKey    string
	}
	var captureMu sync.Mutex
	captures := make(map[string]upstreamCapture)

	newUpstream := func(providerID string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "cannot read request", http.StatusBadRequest)
				return
			}
			var payload struct {
				Model  string `json:"model"`
				Input  string `json:"input"`
				Stream bool   `json:"stream"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			capture := upstreamCapture{
				path: r.URL.Path, trace: r.URL.Query().Get("trace"), debug: r.URL.Query().Get("debug"),
				model: payload.Model, input: payload.Input, stream: payload.Stream,
				providerKey: r.Header.Get("Api-Key"), authorization: r.Header.Get("Authorization"),
				gatewayKey: r.Header.Get("X-API-Key"),
			}
			captureMu.Lock()
			captures[providerID] = capture
			captureMu.Unlock()

			responseID := "resp-" + providerID
			if payload.Stream {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				created, _ := json.Marshal(map[string]interface{}{
					"type": "response.created", "sequence_number": 0,
					"response": map[string]interface{}{"id": responseID, "object": "response", "status": "in_progress"},
				})
				delta, _ := json.Marshal(map[string]interface{}{
					"type": "response.output_text.delta", "sequence_number": 1, "delta": "stream from " + providerID,
				})
				completed, _ := json.Marshal(map[string]interface{}{
					"type": "response.completed", "sequence_number": 2,
					"response": map[string]interface{}{"id": responseID, "object": "response", "status": "completed"},
				})
				_, _ = fmt.Fprintf(w, "event: response.created\ndata: %s\n\n", created)
				_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: %s\n\n", delta)
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", completed)
				return
			}

			responseBody, _ := json.Marshal(map[string]interface{}{
				"id": responseID, "object": "response", "model": payload.Model, "status": "completed",
				"output": []interface{}{map[string]interface{}{
					"type": "message",
					"content": []interface{}{map[string]interface{}{
						"type": "output_text", "text": "response from " + providerID,
					}},
				}},
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(responseBody)
		}))
	}

	deepseekUpstream := newUpstream("deepseek")
	defer deepseekUpstream.Close()
	openaiUpstream := newUpstream("openai")
	defer openaiUpstream.Close()

	cfg := &config.Config{}
	cfg.Llama.AutoLoadOnProxy = false
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{
		{
			ID: "deepseek", Enabled: true, BaseURL: deepseekUpstream.URL + "/v1",
			DefaultModel: "deepseek-chat", APIKey: "deepseek-upstream-secret",
			APIKeyHeader: "api-key", AllowInsecureHTTP: true, AllowPrivateNetwork: true,
		},
		{
			ID: "openai", Enabled: true, BaseURL: openaiUpstream.URL + "/v1",
			DefaultModel: "gpt-4.1", APIKey: "openai-upstream-secret",
			APIKeyHeader: "api-key", AllowInsecureHTTP: true, AllowPrivateNetwork: true,
		},
	}
	deepseekRoute := config.AgentRouteConfig{
		ID: "deepseek-agent", Enabled: true, PathPrefix: "/deepseek",
		ProviderID: "deepseek", DefaultModel: "deepseek-chat",
	}
	openaiRoute := config.AgentRouteConfig{
		ID: "openai-agent", Enabled: true, PathPrefix: "/openai",
		ProviderID: "openai", DefaultModel: "gpt-4.1",
	}
	cfg.Agents = []config.AgentRouteConfig{deepseekRoute, openaiRoute}

	s := &Server{
		cfg: cfg,
		upstream: settings.Upstream{
			Backend: "llamacpp", BaseURL: "http://llama-unused.invalid:8080", ModelName: "local.gguf",
		},
		fileUpstream: settings.Upstream{
			Backend: "llamacpp", BaseURL: "http://llama-unused.invalid:8080", ModelName: "local.gguf",
		},
	}
	s.pipe = s.newInferencePipeline()
	// 本测试聚焦 Agent data-plane；鉴权、限流和数据库审计由独立集成测试覆盖。
	s.pipe.Observe = nil

	routes := []struct {
		config     config.AgentRouteConfig
		providerID string
		model      string
	}{
		{config: deepseekRoute, providerID: "deepseek", model: "deepseek-chat"},
		{config: openaiRoute, providerID: "openai", model: "gpt-4.1"},
	}
	for _, tc := range routes {
		t.Run(tc.providerID, func(t *testing.T) {
			handler := s.agentRouteHandler(tc.config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				route, ok := agentRouteFromContext(r.Context())
				if !ok {
					http.Error(w, "trusted Agent route missing", http.StatusInternalServerError)
					return
				}
				body, readErr := io.ReadAll(r.Body)
				if readErr != nil {
					http.Error(w, "request body read failed", http.StatusBadRequest)
					return
				}
				id := r.Header.Get("X-Request-ID")
				if id == "" {
					http.Error(w, "request ID missing", http.StatusBadRequest)
					return
				}
				sink := &responseSink{w: w}
				_, runErr := s.pipe.Run(r.Context(), core.Request{
					ID: id, Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
					Header: headerMap(r.Header), Body: body,
					RouteProviderID: route.ProviderID, RouteDefaultModel: route.DefaultModel,
					Sink: sink, Started: time.Now(), RequestURL: agentRequestPublicURL(r, nil),
				})
				s.callDrop(id)
				if runErr != nil && !sink.HeaderWritten() {
					status, code, message := core.Unpack(runErr)
					writeInferenceError(w, r.URL.Path, status, code, message)
				}
			}))

			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream_%v", streaming), func(t *testing.T) {
					requestBody, err := json.Marshal(map[string]interface{}{
						"model": "caller-model-must-not-win", "input": "hello from Codex", "stream": streaming,
					})
					if err != nil {
						t.Fatal(err)
					}
					request := httptest.NewRequest(http.MethodPost,
						"http://gateway.example"+tc.config.PathPrefix+"/v1/responses?trace="+tc.providerID+"&debug=1",
						strings.NewReader(string(requestBody)))
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Authorization", "Bearer gateway-caller-secret")
					request.Header.Set("X-API-Key", "gateway-caller-secret")
					request.Header.Set("X-Request-ID", fmt.Sprintf("agent-responses-%s-%v", tc.providerID, streaming))
					writer := httptest.NewRecorder()
					handler.ServeHTTP(writer, request)
					if writer.Code != http.StatusOK {
						t.Fatalf("gateway status = %d, body=%s", writer.Code, writer.Body.String())
					}

					if streaming {
						responseBody := writer.Body.String()
						if !strings.HasPrefix(writer.Header().Get("Content-Type"), "text/event-stream") {
							t.Fatalf("stream Content-Type = %q", writer.Header().Get("Content-Type"))
						}
						for _, marker := range []string{
							"event: response.created",
							"event: response.output_text.delta",
							"stream from " + tc.providerID,
							"event: response.completed",
							"sequence_number",
						} {
							if !strings.Contains(responseBody, marker) {
								t.Errorf("stream response missing %q: %q", marker, responseBody)
							}
						}
					} else {
						var response struct {
							ID     string            `json:"id"`
							Object string            `json:"object"`
							Model  string            `json:"model"`
							Status string            `json:"status"`
							Output []json.RawMessage `json:"output"`
						}
						if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
							t.Fatalf("gateway response is not JSON: %v", err)
						}
						if response.ID != "resp-"+tc.providerID || response.Object != "response" ||
							response.Model != tc.model || response.Status != "completed" || len(response.Output) != 1 {
							t.Fatalf("response came from the wrong Provider/model or has invalid envelope: %+v", response)
						}
						var output struct {
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						}
						if err := json.Unmarshal(response.Output[0], &output); err != nil {
							t.Fatalf("response output is malformed: %v", err)
						}
						if len(output.Content) != 1 || output.Content[0].Text != "response from "+tc.providerID {
							t.Fatalf("response text came from wrong Provider: %+v", output)
						}
					}

					captureMu.Lock()
					capture := captures[tc.providerID]
					captureMu.Unlock()
					if capture.path != "/v1/responses" || capture.trace != tc.providerID || capture.debug != "1" {
						t.Errorf("upstream path/query = %q / trace=%q debug=%q", capture.path, capture.trace, capture.debug)
					}
					if capture.model != tc.model || capture.input != "hello from Codex" {
						t.Errorf("upstream model/input = %q / %q, want %q / %q", capture.model, capture.input, tc.model, "hello from Codex")
					}
					if capture.stream != streaming {
						t.Errorf("upstream stream = %v, want %v", capture.stream, streaming)
					}
					if capture.providerKey != tc.providerID+"-upstream-secret" {
						t.Errorf("upstream Provider credential does not match Agent %q", tc.providerID)
					}
					if capture.authorization != "" || capture.gatewayKey != "" {
						t.Errorf("Gateway caller credentials leaked upstream: Authorization=%q X-API-Key=%q", capture.authorization, capture.gatewayKey)
					}
				})
			}
		})
	}
	if got := s.effectiveUpstream().Backend; got != "llamacpp" {
		t.Fatalf("Agent Responses requests changed global active Provider to %q", got)
	}
}
