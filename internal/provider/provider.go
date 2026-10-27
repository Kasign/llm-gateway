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

// NewHTTPError 将上游 HTTP 状态码映射为稳定的 Provider 错误类别。
// Retryable 只是可重试建议；是否重试还必须检查响应是否已提交给调用方。
func NewHTTPError(providerID string, status int, cause error) *ProviderError {
	kind := ErrorUnknown
	retryable := false

	switch {
	case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusUnprocessableEntity:
		kind = ErrorInvalidRequest
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = ErrorAuthentication
	case status == http.StatusNotFound:
		kind = ErrorModelNotFound
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		kind = ErrorTimeout
		retryable = true
	case status == http.StatusTooManyRequests:
		kind = ErrorRateLimit
		retryable = true
	case status >= 500 && status <= 599:
		kind = ErrorUpstream
		retryable = status == http.StatusInternalServerError ||
			status == http.StatusBadGateway ||
			status == http.StatusServiceUnavailable
	case status >= 300 && status <= 399:
		kind = ErrorProtocol
	}

	return &ProviderError{
		ProviderID: normalizeID(providerID),
		Kind:       kind,
		StatusCode: status,
		Retryable:  retryable,
		Err:        cause,
	}
}

// ToCoreError 将 ProviderError 转换为网关内部错误，不把上游原始响应正文写入对外文案。
func ToCoreError(err error) *core.Error {
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr == nil {
		return nil
	}
	status := http.StatusBadGateway
	switch providerErr.Kind {
	case ErrorInvalidRequest:
		status = http.StatusBadRequest
	case ErrorTimeout:
		status = http.StatusGatewayTimeout
	case ErrorRateLimit:
		status = http.StatusServiceUnavailable
	}
	code := "provider_" + string(providerErr.Kind)
	message := "upstream provider request failed"
	if providerErr.ProviderID != "" {
		message = fmt.Sprintf("upstream provider %s request failed", providerErr.ProviderID)
	}
	return &core.Error{Status: status, Code: code, Message: message}
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
	ModelID      string // Gateway 解析出的上游模型 ID，Provider 不信任调用方原始 model。
	Stream       bool
	OnChunk      func([]byte)
	OnHeaders    func(http.Header, int)
	// OnUpstreamDispatch marks the point where Provider admission succeeded and the outbound HTTP request is about to start.
	OnUpstreamDispatch func()
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
// EnsureReady 供请求阶段使用；Load/Unload/Refresh 供管理操作使用。
type LocalModelManager interface {
	EnsureReady(context.Context, string, string) error
	Load(context.Context, string) error
	Unload(context.Context, string) error
	Refresh(context.Context) error
}

type Factory func() Provider

// Registry 通过工厂注册 Provider，避免请求处理逻辑中堆叠供应商分支。
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
	managers  map[string]LocalModelManager
}

func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
		managers:  make(map[string]LocalModelManager),
	}
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

// RegisterLocal 将本地 Provider 与可选管理器一起注册，避免请求线程看到半完成注册。
func (r *Registry) RegisterLocal(id string, factory Factory, manager LocalModelManager) error {
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
	if manager != nil {
		r.managers[key] = manager
	}
	return nil
}

// ResolveLocalModelManager 获取可选本地控制能力；没有该能力的 Provider 会返回 false。
func (r *Registry) ResolveLocalModelManager(id string) (LocalModelManager, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	manager, ok := r.managers[normalizeID(id)]
	r.mu.RUnlock()
	return manager, ok
}


// Replace 原子替换现有 Provider 的工厂。已经取得旧实例的请求/SSE 不受影响。
func (r *Registry) Replace(id string, factory Factory) error {
	if r == nil {
		return errors.New("provider registry is nil")
	}
	key := normalizeID(id)
	if key == "" || factory == nil {
		return errors.New("provider ID and factory are required")
	}
	if key == "llamacpp" || key == "ollama" {
		return fmt.Errorf("built-in Provider %q cannot be replaced through remote management", key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[key]; !exists {
		return fmt.Errorf("%w: %s", ErrProviderNotFound, key)
	}
	r.factories[key] = factory
	return nil
}

// Unregister 原子移除远程 Provider 的新请求入口；已经解析的旧实例可以完成当前请求。
func (r *Registry) Unregister(id string) error {
	if r == nil {
		return errors.New("provider registry is nil")
	}
	key := normalizeID(id)
	if key == "" {
		return errors.New("provider ID is required")
	}
	if key == "llamacpp" || key == "ollama" {
		return fmt.Errorf("built-in Provider %q cannot be unregistered", key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[key]; !exists {
		return fmt.Errorf("%w: %s", ErrProviderNotFound, key)
	}
	delete(r.factories, key)
	delete(r.managers, key)
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
