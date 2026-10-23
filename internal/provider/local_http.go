package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/whl/llm-gateway/internal/core"
)

// LocalHTTPClient 是现有本地 HTTP 传输契约。llama.Client 实现此接口。
type LocalHTTPClient interface {
	Proxy(context.Context, http.ResponseWriter, *http.Request, string, io.Reader) (int64, []byte, int, error)
	StreamProxy(context.Context, http.ResponseWriter, *http.Request, string, io.Reader, func([]byte), func(http.Header, int), func([]byte) error, func()) error
}

type LocalHTTPProvider struct {
	id           string
	capabilities Capabilities
	client       LocalHTTPClient
}

func NewLocalHTTPProvider(id string, capabilities Capabilities, client LocalHTTPClient) *LocalHTTPProvider {
	return &LocalHTTPProvider{
		id:           normalizeID(id),
		capabilities: capabilities,
		client:       client,
	}
}

func (p *LocalHTTPProvider) ID() string { return p.id }

func (p *LocalHTTPProvider) Capabilities() Capabilities { return p.capabilities }

func (p *LocalHTTPProvider) Forward(ctx context.Context, call InferenceCall) (core.Result, error) {
	if p == nil || p.client == nil {
		id := ""
		if p != nil {
			id = p.id
		}
		return core.Result{}, &ProviderError{ProviderID: id, Kind: ErrorUnknown, StatusCode: http.StatusBadGateway, Err: errors.New("local HTTP client is nil")}
	}
	if call.Writer == nil || call.Request == nil {
		return core.Result{}, &ProviderError{ProviderID: p.id, Kind: ErrorInvalidRequest, StatusCode: http.StatusInternalServerError, Err: errors.New("response writer and request are required")}
	}
	path := strings.TrimSpace(call.Path)
	if path == "" && call.Request.URL != nil {
		path = call.Request.URL.Path
	}
	if path == "" {
		return core.Result{}, &ProviderError{ProviderID: p.id, Kind: ErrorInvalidRequest, StatusCode: http.StatusBadRequest, Err: errors.New("upstream path is required")}
	}

	if !call.Stream {
		n, body, status, err := p.client.Proxy(ctx, call.Writer, call.Request, path, bytes.NewReader(call.Body))
		return core.Result{Status: status, UpstreamStatus: status, ResponseBytes: n, Body: body}, err
	}

	var responseBytes int64
	var status int
	err := p.client.StreamProxy(
		ctx,
		call.Writer,
		call.Request,
		path,
		bytes.NewReader(call.Body),
		func(chunk []byte) {
			responseBytes += int64(len(chunk))
			if call.OnChunk != nil {
				call.OnChunk(chunk)
			}
		},
		func(headers http.Header, code int) {
			status = code
			if call.OnHeaders != nil {
				call.OnHeaders(headers, code)
			}
		},
		call.ClientWriter,
		call.OnFirstByte,
	)
	return core.Result{Status: status, UpstreamStatus: status, ResponseBytes: responseBytes}, err
}

// NewLocalRegistry 保留旧版无参数 clientFactory 契约，兼容既有调用方。
func NewLocalRegistry(clientFactory func() LocalHTTPClient, managerFactory ...func(string) LocalModelManager) *Registry {
	var backendFactory func(string) LocalHTTPClient
	if clientFactory != nil {
		backendFactory = func(string) LocalHTTPClient { return clientFactory() }
	}
	return NewLocalRegistryWithBackendClient(backendFactory, managerFactory...)
}

// NewLocalRegistryWithBackendClient 按 Provider ID 获取对应客户端，避免切换后端后复用活动后端客户端。
// managerFactory 可选；没有本地模型控制能力的 Provider 不需要实现空操作。
func NewLocalRegistryWithBackendClient(clientFactory func(string) LocalHTTPClient, managerFactory ...func(string) LocalModelManager) *Registry {
	registry := NewRegistry()
	var makeManager func(string) LocalModelManager
	if len(managerFactory) > 0 {
		makeManager = managerFactory[0]
	}
	register := func(id string, hasManager bool) Factory {
		return func() Provider {
			var client LocalHTTPClient
			if clientFactory != nil {
				client = clientFactory(id)
			}
			capabilities := Capabilities{
				Protocol:             "openai-compatible",
				ChatCompletions:      true,
				Streaming:            true,
				LocalModelManagement: hasManager,
			}
			return NewLocalHTTPProvider(id, capabilities, client)
		}
	}
	registerLocal := func(id string) {
		var manager LocalModelManager
		if makeManager != nil {
			manager = makeManager(id)
		}
		factory := register(id, manager != nil)
		if manager == nil {
			_ = registry.Register(id, factory)
			return
		}
		_ = registry.RegisterLocal(id, factory, manager)
	}
	registerLocal("llamacpp")
	registerLocal("ollama")
	return registry
}
