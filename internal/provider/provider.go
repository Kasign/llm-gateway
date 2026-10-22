// Package provider 定义推理 Provider 的稳定边界。
//
// Provider 只负责一次推理调用；模型加载、卸载和下载属于可选的本地管理能力，
// 不放在 Provider 必须实现的方法上，避免远程 Provider 实现无意义的空操作。
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/whl/llm-gateway/internal/core"
)

type Capabilities struct {
	Protocol             string `json:"protocol"`
	ChatCompletions      bool   `json:"chat_completions"`
	Streaming            bool   `json:"streaming"`
	LocalModelManagement bool   `json:"local_model_management,omitempty"`
}

type ModelDescriptor struct {
	ProviderID      string       `json:"provider_id"`
	ModelID         string       `json:"model_id"` // Gateway 对调用方公开的稳定 ID。
	UpstreamModelID string       `json:"upstream_model_id"`
	DisplayName     string       `json:"display_name,omitempty"`
	Capabilities    Capabilities `json:"capabilities"`
}

type UsageSource string

const (
	UsageSourceProvider  UsageSource = "provider"
	UsageSourceEstimated UsageSource = "estimated"
	UsageSourceUnknown   UsageSource = "unknown"
)

// Usage 保留数值和来源可信度。估算值不应伪装成上游真实用量。
type Usage struct {
	InputTokens  *int64     `json:"input_tokens,omitempty"`
	OutputTokens *int64     `json:"output_tokens,omitempty"`
	TotalTokens  *int64     `json:"total_tokens,omitempty"`
	Source       UsageSource `json:"source"`
}

type ErrorKind string

const (
	ErrorInvalidRequest ErrorKind = "invalid_request"
	ErrorAuthentication ErrorKind = "authentication"
	ErrorRateLimit      ErrorKind = "rate_limit"
	ErrorModelNotFound  ErrorKind = "model_not_found"
	ErrorTimeout        ErrorKind = "timeout"
	ErrorProtocol       ErrorKind = "protocol"
	ErrorUpstream       ErrorKind = "upstream"
	ErrorUnknown        ErrorKind = "unknown"
)

var (
	ErrProviderNotFound = errors.New("provider not found")
	ErrProviderExists   = errors.New("provider already registered")
)

// ProviderError 保存稳定错误分类，不把上游原始错误正文直接用作常规日志文本。
type ProviderError struct {
	ProviderID string
	Kind       ErrorKind
	StatusCode int
	Retryable  bool
	Err        error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	message := fmt.Sprintf("provider %s: %s", e.ProviderID, e.Kind)
	if e.StatusCode != 0 {
		message += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	return message
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// InferenceCall 是在鉴权、路由、策略检查和请求改写后交给 Provider 的标准调用。
type InferenceCall struct {
	Writer       http.ResponseWriter
	Request      *http.Request
	Path         string
	Body         []byte
	Stream       bool
	OnChunk      func([]byte)
	OnHeaders    func(http.Header, int)
	ClientWriter func([]byte) error
	OnFirstByte  func()
}

// Provider 只声明推理能力。
type Provider interface {
	ID() string
	Capabilities() Capabilities
	Forward(context.Context, InferenceCall) (core.Result, error)
}

// LocalModelManager 是可选控制面能力，不属于所有 Provider 的必选接口。
type LocalModelManager interface {
	Load(context.Context, string) error
	Unload(context.Context, string) error
	Refresh(context.Context) error
}

type Factory func() Provider

// Registry 通过工厂注册 Provider，避免请求处理逻辑中堆叠供应商分支。
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

func normalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func (r *Registry) Register(id string, factory Factory) error {
	if r == nil {
		return errors.New("provider registry is nil")
	}
	key := normalizeID(id)
	if key == "" || factory == nil {
		return errors.New("provider ID and factory are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[key]; exists {
		return fmt.Errorf("%w: %s", ErrProviderExists, key)
	}
	r.factories[key] = factory
	return nil
}

func (r *Registry) Resolve(id string) (Provider, error) {
	if r == nil {
		return nil, errors.New("provider registry is nil")
	}
	key := normalizeID(id)
	r.mu.RLock()
	factory, ok := r.factories[key]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotFound, key)
	}
	p := factory()
	if p == nil || normalizeID(p.ID()) != key {
		return nil, &ProviderError{ProviderID: key, Kind: ErrorUnknown}
	}
	return p, nil
}
