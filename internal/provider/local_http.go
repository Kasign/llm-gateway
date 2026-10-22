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
		return core.Result{Status: status, ResponseBytes: n, Body: body}, err
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
	return core.Result{Status: status, ResponseBytes: responseBytes}, err
}

// NewLocalRegistry 将两个已支持的本地后端注册到统一推理接口。
// 工厂每次解析时获取当前活动客户端，管理员切换上游后不会持有过期客户端。
func NewLocalRegistry(clientFactory func() LocalHTTPClient) *Registry {
	registry := NewRegistry()
	capabilities := Capabilities{
		Protocol:             "openai-compatible",
		ChatCompletions:      true,
		Streaming:            true,
		LocalModelManagement: true,
	}
	register := func(id string) Factory {
		return func() Provider {
			var client LocalHTTPClient
			if clientFactory != nil {
				client = clientFactory()
			}
			return NewLocalHTTPProvider(id, capabilities, client)
		}
	}
	_ = registry.Register("llamacpp", register("llamacpp"))
	_ = registry.Register("ollama", register("ollama"))
	return registry
}
