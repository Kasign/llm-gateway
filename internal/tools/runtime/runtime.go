// Package runtime 提供单次工具调用的安全执行边界。
//
// Runtime 位于统一 Tools 模块内部，不负责模型多轮调度，也不依赖具体工具实现。
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/whl/llm-gateway/internal/tools"
)

const (
	DefaultMaxConcurrentExecutions = 4
	MaxConcurrentExecutions         = 64
	DefaultExecutionTimeout         = 30 * time.Second
	MaxExecutionTimeout             = 5 * time.Minute
	DefaultMaxResultBytes           = 64 * 1024
	MaxResultBytes                  = 1024 * 1024
)

var (
	ErrRuntimeUnavailable       = errors.New("tool runtime is unavailable")
	ErrRegistryRequired         = errors.New("tool registry is required")
	ErrInvalidContext           = errors.New("tool execution context is nil")
	ErrAuthorizationUnavailable = errors.New("tool authorization is unavailable")
	ErrUnauthorized             = errors.New("tool execution is not authorized")
	ErrConcurrencyLimit         = errors.New("tool execution concurrency limit reached")
	ErrExecutionTimeout         = errors.New("tool execution timed out")
	ErrInvalidArguments         = errors.New("tool arguments are invalid")
	ErrInvalidResult            = errors.New("tool result is not valid JSON")
	ErrResultTooLarge           = errors.New("tool result exceeds the configured size limit")
	ErrExecutionFailed          = errors.New("tool execution failed")
	ErrExecutorPanicked         = errors.New("tool executor panicked")
)

// Authorizer 根据服务端可信请求上下文判断当前请求是否允许执行指定工具。
// 返回 false 表示明确拒绝；返回 error 表示授权依赖不可用。两种情况都必须 fail closed。
type Authorizer interface {
	Authorize(ctx context.Context, toolName string) (allowed bool, err error)
}

// Config 定义 Runtime 进程级执行资源边界。零值字段使用安全默认值。
type Config struct {
	MaxConcurrentExecutions int
	ExecutionTimeout        time.Duration
	MaxResultBytes          int
}

// Runtime 管理单次工具执行，不负责请求级模型—工具循环。
type Runtime struct {
	registry         *tools.Registry
	authorizer       Authorizer
	maxConcurrent    int
	executionTimeout time.Duration
	maxResultBytes   int

	mu     sync.Mutex
	active int
}

// ExecutionError 对调用方隐藏 Executor 的原始错误文本，同时保留 Unwrap 供内部诊断。
type ExecutionError struct {
	cause error
}

// AuthorizationError 对外隐藏授权依赖的原始错误，同时保留 Unwrap 供内部诊断。
type AuthorizationError struct {
	cause error
}

func (e *AuthorizationError) Error() string { return ErrAuthorizationUnavailable.Error() }

func (e *AuthorizationError) Unwrap() error {
	if e == nil { return nil }
	return e.cause
}

func (e *AuthorizationError) Is(target error) bool { return target == ErrAuthorizationUnavailable }

func (e *ExecutionError) Error() string { return ErrExecutionFailed.Error() }

func (e *ExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *ExecutionError) Is(target error) bool { return target == ErrExecutionFailed }

// NewRuntime 构造 Runtime。Authorizer 可以为空，但为空时所有执行都会 fail closed。
func NewRuntime(registry *tools.Registry, authorizer Authorizer, cfg Config) (*Runtime, error) {
	if registry == nil {
		return nil, ErrRegistryRequired
	}
	if cfg.MaxConcurrentExecutions == 0 {
		cfg.MaxConcurrentExecutions = DefaultMaxConcurrentExecutions
	}
	if cfg.MaxConcurrentExecutions < 1 || cfg.MaxConcurrentExecutions > MaxConcurrentExecutions {
		return nil, fmt.Errorf("max concurrent executions must be between 1 and %d", MaxConcurrentExecutions)
	}
	if cfg.ExecutionTimeout == 0 {
		cfg.ExecutionTimeout = DefaultExecutionTimeout
	}
	if cfg.ExecutionTimeout < time.Millisecond || cfg.ExecutionTimeout > MaxExecutionTimeout {
		return nil, fmt.Errorf("execution timeout must be between 1ms and %s", MaxExecutionTimeout)
	}
	if cfg.MaxResultBytes == 0 {
		cfg.MaxResultBytes = DefaultMaxResultBytes
	}
	if cfg.MaxResultBytes < 1 || cfg.MaxResultBytes > MaxResultBytes {
		return nil, fmt.Errorf("max result bytes must be between 1 and %d", MaxResultBytes)
	}
	return &Runtime{
		registry: registry, authorizer: authorizer,
		maxConcurrent: cfg.MaxConcurrentExecutions,
		executionTimeout: cfg.ExecutionTimeout,
		maxResultBytes: cfg.MaxResultBytes,
	}, nil
}

// Execute 查找、授权、校验并执行一个已注册工具。
// 并发达到上限时立即失败，不创建等待队列；超时通过派生 Context 传递给 Executor。
func (r *Runtime) Execute(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	if r == nil {
		return nil, ErrRuntimeUnavailable
	}
	if ctx == nil {
		return nil, ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	definition, executor, err := r.registry.Lookup(name)
	if err != nil {
		return nil, err
	}
	if r.authorizer == nil {
		return nil, ErrAuthorizationUnavailable
	}
	allowed, authErr := invokeAuthorizer(ctx, r.authorizer, definition.Name)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if authErr != nil {
		return nil, &AuthorizationError{cause: authErr}
	}
	if !allowed {
		return nil, ErrUnauthorized
	}
	if err := tools.ValidateArguments(definition.Parameters, arguments); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !r.tryAcquire() {
		return nil, ErrConcurrencyLimit
	}
	defer r.release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	executionCtx, cancel := context.WithTimeout(ctx, r.executionTimeout)
	defer cancel()
	argumentCopy := append(json.RawMessage(nil), arguments...)
	result, executeErr := invokeExecutor(executionCtx, executor, argumentCopy)
	if executeErr != nil {
		if errors.Is(executeErr, ErrExecutorPanicked) {
			return nil, ErrExecutorPanicked
		}
		if errors.Is(executionCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w", ErrExecutionTimeout, context.DeadlineExceeded)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &ExecutionError{cause: executeErr}
	}
	if err := executionCtx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w", ErrExecutionTimeout, context.DeadlineExceeded)
		}
		return nil, err
	}
	if len(result) == 0 {
		return nil, ErrInvalidResult
	}
	if len(result) > r.maxResultBytes {
		return nil, ErrResultTooLarge
	}
	if !utf8.Valid(result) || !json.Valid(result) {
		return nil, ErrInvalidResult
	}
	return append(json.RawMessage(nil), result...), nil
}

func (r *Runtime) tryAcquire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active >= r.maxConcurrent {
		return false
	}
	r.active++
	return true
}

func (r *Runtime) release() {
	r.mu.Lock()
	if r.active > 0 {
		r.active--
	}
	r.mu.Unlock()
}

func invokeAuthorizer(ctx context.Context, authorizer Authorizer, name string) (allowed bool, err error) {
	defer func() {
		if recover() != nil {
			allowed = false
			err = ErrAuthorizationUnavailable
		}
	}()
	return authorizer.Authorize(ctx, name)
}

func invokeExecutor(ctx context.Context, executor tools.Executor, arguments json.RawMessage) (result json.RawMessage, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = ErrExecutorPanicked
		}
	}()
	return executor.Execute(ctx, arguments)
}
