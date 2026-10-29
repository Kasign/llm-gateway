package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/provider"
	"github.com/whl/llm-gateway/internal/settings"
	"github.com/whl/llm-gateway/internal/store"
)

func TestAgentRouteHandlerStripsPrefixAndPreservesPublicURL(t *testing.T) {
	cfg := config.AgentRouteConfig{
		ID: "deepseek", Enabled: true, PathPrefix: "/deepseek",
		ProviderID: "deepseek", DefaultModel: "deepseek-chat",
	}
	s := &Server{}
	var gotPath, gotProvider, gotModel, publicURL string
	handler := s.agentRouteHandler(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		route, ok := agentRouteFromContext(r.Context())
		if !ok {
			t.Fatal("trusted Agent route missing from context")
		}
		gotProvider, gotModel = route.ProviderID, route.DefaultModel
		publicURL = agentRequestPublicURL(r, nil)
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://gateway.example/deepseek/v1/chat/completions?token=private&stream=true", strings.NewReader("{}"))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d", res.Code)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("normalized path = %q", gotPath)
	}
	if gotProvider != "deepseek" || gotModel != "deepseek-chat" {
		t.Fatalf("route = provider %q model %q", gotProvider, gotModel)
	}
	if !strings.Contains(publicURL, "/deepseek/v1/chat/completions?token=***&stream=true") {
		t.Fatalf("public URL was not preserved/redacted: %q", publicURL)
	}
}

func TestResolveInferenceUsesAgentRouteWithoutChangingActiveProvider(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{
		cfg: cfg,
		upstream: settings.Upstream{Backend: "llamacpp", BaseURL: "http://llama:8080", ModelName: "local.gguf"},
		webProviders: map[string]managedProvider{
			"deepseek": {ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", APIKeyHeader: "authorization", DefaultModel: "deepseek-chat"},
		},
	}
	choice, err := s.resolveInference(context.Background(), core.State{
		Request: core.Request{
			ID: "agent-route-test", Method: http.MethodPost, Path: "/v1/chat/completions",
			RouteProviderID: "deepseek", RouteDefaultModel: "deepseek-chat",
		},
		ClientModel: "caller-model-must-not-select-provider",
	})
	if err != nil {
		t.Fatal(err)
	}
	if choice.ProviderID != "deepseek" || choice.UpstreamID != "deepseek-chat" || choice.ModelKey != "deepseek-chat" {
		t.Fatalf("route model choice = %+v", choice)
	}
	if s.effectiveUpstream().Backend != "llamacpp" {
		t.Fatalf("Agent routing mutated active Provider: %q", s.effectiveUpstream().Backend)
	}
}

// TestAgentRoutesForwardToDistinctConfiguredProviders 验证 Agent 前缀经过路由归一、Pipeline 和真实 Provider HTTP 适配器后，仍命中各自上游。
func TestAgentRoutesForwardToDistinctConfiguredProviders(t *testing.T) {
	type upstreamCapture struct {
		path          string
		trace         string
		model         string
		stream        bool
		providerKey   string
		authorization string
		gatewayKey    string
	}
	var captureMu sync.Mutex
	captures := make(map[string]upstreamCapture)

	newUpstream := func(providerID, responseID string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "cannot read request", http.StatusBadRequest)
				return
			}
			var requestPayload struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
			}
			if err := json.Unmarshal(body, &requestPayload); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			capture := upstreamCapture{
				path: r.URL.Path, trace: r.URL.Query().Get("trace"),
				model: requestPayload.Model, stream: requestPayload.Stream,
				providerKey: r.Header.Get("Api-Key"),
				authorization: r.Header.Get("Authorization"), gatewayKey: r.Header.Get("X-API-Key"),
			}
			captureMu.Lock()
			captures[providerID] = capture
			captureMu.Unlock()

			if requestPayload.Stream {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				_, _ = fmt.Fprintf(w,
					"data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n",
					responseID, requestPayload.Model, "stream from "+providerID)
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w,
				`{"id":%q,"object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`,
				responseID, requestPayload.Model, "response from "+providerID)
		}))
	}

	deepseekUpstream := newUpstream("deepseek", "deepseek-result")
	defer deepseekUpstream.Close()
	openaiUpstream := newUpstream("openai", "openai-result")
	defer openaiUpstream.Close()

	cfg := &config.Config{}
	cfg.Llama.AutoLoadOnProxy = false
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
		webProviders: map[string]managedProvider{
			"deepseek": {ID: "deepseek", Enabled: true, BaseURL: deepseekUpstream.URL + "/v1", APIKey: "deepseek-upstream-secret", APIKeyHeader: "api-key", DefaultModel: "deepseek-chat", AllowInsecureHTTP: true, AllowPrivateNetwork: true},
			"openai": {ID: "openai", Enabled: true, BaseURL: openaiUpstream.URL + "/v1", APIKey: "openai-upstream-secret", APIKeyHeader: "api-key", DefaultModel: "gpt-4.1", AllowInsecureHTTP: true, AllowPrivateNetwork: true},
		},
	}
	s.pipe = s.newInferencePipeline()
	for id, row := range s.webProviders {
		remote, err := s.buildRemoteProvider(row)
		if err != nil {
			t.Fatalf("build %s Provider: %v", id, err)
		}
		if err := s.providers.Register(id, func() provider.Provider { return remote }); err != nil {
			t.Fatalf("register %s Provider: %v", id, err)
		}
	}
	// 本测试聚焦 data-plane 路由与 Provider 选择，不需要数据库审计观察器；其行为由独立测试覆盖。
	s.pipe.Observe = nil

	routes := []struct {
		config       config.AgentRouteConfig
		providerID   string
		model        string
		resultID     string
	}{
		{config: deepseekRoute, providerID: "deepseek", model: "deepseek-chat", resultID: "deepseek-result"},
		{config: openaiRoute, providerID: "openai", model: "gpt-4.1", resultID: "openai-result"},
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
					requestBody := fmt.Sprintf(`{"model":"caller-model-must-not-win","stream":%t,"messages":[{"role":"user","content":"hello"}]}`, streaming)
					request := httptest.NewRequest(http.MethodPost,
						"http://gateway.example"+tc.config.PathPrefix+"/v1/chat/completions?trace="+tc.providerID,
						strings.NewReader(requestBody))
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Authorization", "Bearer gateway-caller-secret")
					request.Header.Set("X-API-Key", "gateway-caller-secret")
					request.Header.Set("X-Request-ID", fmt.Sprintf("agent-data-plane-%s-%v", tc.providerID, streaming))
					writer := httptest.NewRecorder()
					handler.ServeHTTP(writer, request)
					if writer.Code != http.StatusOK {
						t.Fatalf("gateway status = %d, body=%s", writer.Code, writer.Body.String())
					}

					if streaming {
						body := writer.Body.String()
						if !strings.HasPrefix(writer.Header().Get("Content-Type"), "text/event-stream") ||
							!strings.Contains(body, "stream from "+tc.providerID) ||
							!strings.Contains(body, "data: [DONE]") {
							t.Fatalf("stream response from wrong Provider or malformed SSE: headers=%v body=%q", writer.Header(), body)
						}
					} else {
						var response struct {
							ID      string `json:"id"`
							Model   string `json:"model"`
							Choices []struct {
								Message struct {
									Content string `json:"content"`
								} `json:"message"`
							} `json:"choices"`
						}
						if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
							t.Fatalf("gateway response is not JSON: %v", err)
						}
						if response.ID != tc.resultID || response.Model != tc.model || len(response.Choices) != 1 ||
							response.Choices[0].Message.Content != "response from "+tc.providerID {
							t.Fatalf("response came from the wrong Provider/model: %+v", response)
						}
					}

					captureMu.Lock()
					capture := captures[tc.providerID]
					captureMu.Unlock()
					if capture.path != "/v1/chat/completions" || capture.trace != tc.providerID {
						t.Errorf("upstream path/query = %q / %q", capture.path, capture.trace)
					}
					if capture.model != tc.model {
						t.Errorf("upstream model = %q, want Agent default %q", capture.model, tc.model)
					}
					if capture.stream != streaming {
						t.Errorf("upstream stream = %v, want %v", capture.stream, streaming)
					}
					wantKey := tc.providerID + "-upstream-secret"
					if capture.providerKey != wantKey {
						t.Errorf("upstream provider key = %q, want route-specific key", capture.providerKey)
					}
					if capture.authorization != "" || capture.gatewayKey != "" {
						t.Errorf("Gateway caller credentials leaked upstream: Authorization=%q X-API-Key=%q", capture.authorization, capture.gatewayKey)
					}
				})
			}
		})
	}
	if got := s.effectiveUpstream().Backend; got != "llamacpp" {
		t.Fatalf("Agent data-plane requests changed global active Provider to %q", got)
	}
}

func TestAgentModelListBodyHonorsModelACLAndExposesOnlyFixedTarget(t *testing.T) {
	route := agentRoute{ID: "deepseek-agent", ProviderID: "deepseek", DefaultModel: "deepseek-chat"}
	target := proxyModelTarget{
		ProviderID: "deepseek", ModelKey: "deepseek-chat",
		UpstreamID: "deepseek-chat-upstream", Hint: "deepseek-chat",
	}
	decode := func(t *testing.T, body []byte) struct {
		Object string `json:"object"`
		Data []struct {
			ID          string `json:"id"`
			Object      string `json:"object"`
			OwnedBy     string `json:"owned_by"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	} {
		t.Helper()
		var payload struct {
			Object string `json:"object"`
			Data []struct {
				ID          string `json:"id"`
				Object      string `json:"object"`
				OwnedBy     string `json:"owned_by"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Agent model list is not JSON: %v", err)
		}
		return payload
	}

	cases := []struct {
		name    string
		key     *auth.Key
		wantNum int
	}{
		{name: "unrestricted key", key: &auth.Key{}, wantNum: 1},
		{name: "allow by public model ID", key: &auth.Key{AllowedModels: []string{"deepseek-chat"}}, wantNum: 1},
		{name: "allow by upstream model ID", key: &auth.Key{AllowedModels: []string{"deepseek-chat-upstream"}}, wantNum: 1},
		{name: "deny different model", key: &auth.Key{AllowedModels: []string{"gpt-4.1"}}, wantNum: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, count := agentModelListBody(route, target, tc.key)
			payload := decode(t, body)
			if count != len(payload.Data) {
				t.Fatalf("helper count = %d, response model count = %d", count, len(payload.Data))
			}
			if payload.Object != "list" || len(payload.Data) != tc.wantNum {
				t.Fatalf("model list object/count = %q/%d, want list/%d: %+v", payload.Object, len(payload.Data), tc.wantNum, payload)
			}
			if tc.wantNum == 1 {
				row := payload.Data[0]
				if row.ID != "deepseek-chat" || row.Object != "model" ||
					row.OwnedBy != "deepseek" || row.DisplayName != "deepseek-chat-upstream" {
					t.Fatalf("Agent model row exposed wrong target: %+v", row)
				}
			}
		})
	}
}

func TestRejectedInferenceLogPreservesAgentURLAndRedactsQuerySecrets(t *testing.T) {
	cfg := config.AgentRouteConfig{
		ID: "deepseek", Enabled: true, PathPrefix: "/deepseek",
		ProviderID: "deepseek", DefaultModel: "deepseek-chat",
	}
	s := &Server{}
	var logItem store.RequestLog
	handler := s.agentRouteHandler(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		logItem = s.buildRejectedInferenceLog(r, "rejected-agent-request", now, now, nil,
			http.StatusUnauthorized, "invalid_api_key", "Invalid API key")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	req := httptest.NewRequest(http.MethodPost,
		"http://gateway.example/deepseek/v1/chat/completions?token=private&stream=true", strings.NewReader("{}"))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.Code)
	}
	if !strings.Contains(logItem.RequestURL, "/deepseek/v1/chat/completions?token=***&stream=true") {
		t.Fatalf("rejected Agent request URL lost its public prefix or redaction: %q", logItem.RequestURL)
	}
	if strings.Contains(logItem.RequestURL, "token=private") {
		t.Fatalf("rejected Agent request audit URL leaked query credential: %q", logItem.RequestURL)
	}
}


func TestConcurrentAgentRoutesKeepProviderAndModelIsolation(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{
		cfg: cfg,
		upstream: settings.Upstream{Backend: "llamacpp", BaseURL: "http://llama:8080", ModelName: "local.gguf"},
		webProviders: map[string]managedProvider{
			"deepseek": {ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", APIKeyHeader: "authorization", DefaultModel: "deepseek-chat"},
			"openai": {ID: "openai", Enabled: true, BaseURL: "https://api.openai.com/v1", APIKeyHeader: "authorization", DefaultModel: "gpt-4.1"},
		},
	}

	const iterations = 200
	var wg sync.WaitGroup
	errors := make(chan string, iterations*2)
	for i := 0; i < iterations; i++ {
		for _, tc := range []struct {
			provider string
			model    string
		}{{"deepseek", "deepseek-chat"}, {"openai", "gpt-4.1"}} {
			wg.Add(1)
			go func(i int, providerID, model string) {
				defer wg.Done()
				choice, err := s.resolveInference(context.Background(), core.State{
					Request: core.Request{
						ID: fmt.Sprintf("agent-concurrent-%s-%d", providerID, i),
						Method: http.MethodPost, Path: "/v1/chat/completions",
						RouteProviderID: providerID, RouteDefaultModel: model,
					},
					// Client-provided model is intentionally different; it must never switch a pinned Agent Provider.
					ClientModel: "caller-model-must-not-select-provider",
				})
				if err != nil {
					errors <- fmt.Sprintf("provider %s: resolve failed: %v", providerID, err)
					return
				}
				if choice.ProviderID != providerID || choice.UpstreamID != model || choice.ModelKey != model {
					errors <- fmt.Sprintf("provider %s model %s resolved to %+v", providerID, model, choice)
				}
			}(i, tc.provider, tc.model)
		}
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if got := s.effectiveUpstream().Backend; got != "llamacpp" {
		t.Fatalf("concurrent Agent resolution mutated the global active Provider: %q", got)
	}
}
