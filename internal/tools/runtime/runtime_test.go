package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/tools"
)

const runtimeTestSchema = `{"type":"object","properties":{"query":{"type":"string","minLength":1}},"required":["query"],"additionalProperties":false}`

type executorFunc func(context.Context, json.RawMessage) (json.RawMessage, error)

func (f executorFunc) Execute(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	return f(ctx, arguments)
}

type authorizerFunc func(context.Context, string) (bool, error)

func (f authorizerFunc) Authorize(ctx context.Context, name string) (bool, error) {
	return f(ctx, name)
}

func newTestRuntime(t *testing.T, cfg Config, authorizer Authorizer, executor tools.Executor) *Runtime {
	t.Helper()
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Definition{
		Name: "web.search", Description: "Search the web",
		Parameters: json.RawMessage(runtimeTestSchema),
	}, executor); err != nil {
		t.Fatal(err)
	}
	r, err := NewRuntime(registry, authorizer, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func allowAll(context.Context, string) (bool, error) { return true, nil }

func TestNewRuntimeDefaultsAndConfigValidation(t *testing.T) {
	registry := tools.NewRegistry()
	r, err := NewRuntime(registry, authorizerFunc(allowAll), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if r.maxConcurrent != DefaultMaxConcurrentExecutions {
		t.Fatalf("max concurrent = %d", r.maxConcurrent)
	}
	if r.executionTimeout != DefaultExecutionTimeout {
		t.Fatalf("execution timeout = %s", r.executionTimeout)
	}
	if r.maxResultBytes != DefaultMaxResultBytes {
		t.Fatalf("max result bytes = %d", r.maxResultBytes)
	}
	if _, err := NewRuntime(nil, authorizerFunc(allowAll), Config{}); !errors.Is(err, ErrRegistryRequired) {
		t.Fatalf("nil registry error = %v", err)
	}
	for _, cfg := range []Config{
		{MaxConcurrentExecutions: -1},
		{MaxConcurrentExecutions: MaxConcurrentExecutions + 1},
		{ExecutionTimeout: MaxExecutionTimeout + time.Millisecond},
		{ExecutionTimeout: -time.Second},
		{MaxResultBytes: MaxResultBytes + 1},
		{MaxResultBytes: -1},
	} {
		if _, err := NewRuntime(registry, authorizerFunc(allowAll), cfg); err == nil {
			t.Errorf("invalid config accepted: %+v", cfg)
		}
	}
}

func TestExecuteValidatesAuthorizesAndCopiesData(t *testing.T) {
	input := json.RawMessage(`{"query":"hello"}`)
	executorOutput := json.RawMessage(`{"ok":true}`)
	var authorizedName string
	r := newTestRuntime(t, Config{}, authorizerFunc(func(_ context.Context, name string) (bool, error) {
		authorizedName = name
		return true, nil
	}), executorFunc(func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
		args[0] = '!'
		return executorOutput, nil
	}))
	result, err := r.Execute(context.Background(), "web.search", input)
	if err != nil {
		t.Fatal(err)
	}
	if authorizedName != "web.search" {
		t.Fatalf("authorized tool name = %q", authorizedName)
	}
	if string(input) != `{"query":"hello"}` {
		t.Fatalf("executor mutated caller arguments: %s", input)
	}
	executorOutput[0] = '!'
	if string(result) != `{"ok":true}` {
		t.Fatalf("returned result aliases executor buffer: %s", result)
	}
	result[0] = '?'
	if string(executorOutput) == string(result) {
		t.Fatal("result and executor buffer should be independent")
	}
}

func TestExecuteFailsClosedOnMissingOrDeniedAuthorization(t *testing.T) {
	calls := 0
	executor := executorFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	})
	t.Run("missing authorizer", func(t *testing.T) {
		r := newTestRuntime(t, Config{}, nil, executor)
		if _, err := r.Execute(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`)); !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("denied", func(t *testing.T) {
		r := newTestRuntime(t, Config{}, authorizerFunc(func(context.Context, string) (bool, error) { return false, nil }), executor)
		if _, err := r.Execute(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`)); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("authorization dependency failure", func(t *testing.T) {
		r := newTestRuntime(t, Config{}, authorizerFunc(func(context.Context, string) (bool, error) { return false, errors.New("sensitive authorization backend detail") }), executor)
		_, err := r.Execute(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`))
		if !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Fatalf("error = %v", err)
		}
		if strings.Contains(err.Error(), "sensitive authorization backend detail") {
			t.Fatalf("authorization error leaked internal detail: %v", err)
		}
	})
	if calls != 0 {
		t.Fatalf("executor called %d times despite authorization failure", calls)
	}
}

func TestExecuteRejectsInvalidArgumentsBeforeExecutor(t *testing.T) {
	calls := 0
	r := newTestRuntime(t, Config{}, authorizerFunc(allowAll), executorFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}))
	_, err := r.Execute(context.Background(), "web.search", json.RawMessage(`{"query":""}`))
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("executor called %d times for invalid arguments", calls)
	}
	if _, err := r.Execute(context.Background(), "missing.tool", json.RawMessage(`{"query":"x"}`)); !errors.Is(err, tools.ErrToolNotFound) {
		t.Fatalf("missing tool error = %v", err)
	}
}

func TestExecuteHonorsExecutionTimeoutAndReleasesSlot(t *testing.T) {
	started := make(chan struct{})
	r := newTestRuntime(t, Config{ExecutionTimeout: 20 * time.Millisecond}, authorizerFunc(allowAll), executorFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	_, err := r.Execute(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`))
	if !errors.Is(err, ErrExecutionTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	<-started
	if got := activeExecutions(r); got != 0 {
		t.Fatalf("active executions after timeout = %d", got)
	}
}

func TestExecutePropagatesParentCancellation(t *testing.T) {
	started := make(chan struct{})
	r := newTestRuntime(t, Config{}, authorizerFunc(allowAll), executorFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.Execute(ctx, "web.search", json.RawMessage(`{"query":"x"}`))
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	if got := activeExecutions(r); got != 0 {
		t.Fatalf("active executions after cancellation = %d", got)
	}
}

func TestExecuteEnforcesFailFastConcurrencyLimit(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	r := newTestRuntime(t, Config{MaxConcurrentExecutions: 1}, authorizerFunc(allowAll), executorFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	}))
	done := make(chan error, 1)
	go func() {
		_, err := r.Execute(context.Background(), "web.search", json.RawMessage(`{"query":"first"}`))
		done <- err
	}()
	<-started
	if _, err := r.Execute(context.Background(), "web.search", json.RawMessage(`{"query":"second"}`)); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("second execution error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := activeExecutions(r); got != 0 {
		t.Fatalf("active executions after release = %d", got)
	}
}

func TestExecuteRejectsInvalidAndOversizedResults(t *testing.T) {
	tests := []struct {
		name   string
		result json.RawMessage
		max    int
		want   error
	}{
		{name: "empty", result: nil, want: ErrInvalidResult},
		{name: "invalid json", result: json.RawMessage(`{"ok":`), want: ErrInvalidResult},
		{name: "invalid utf8", result: json.RawMessage{'"', 0xff, '"'}, want: ErrInvalidResult},
		{name: "too large", result: json.RawMessage(`{"value":"123456789"}`), max: 8, want: ErrResultTooLarge},
		{name: "json null is valid", result: json.RawMessage(`null`), max: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{}
			if tt.max > 0 {
				cfg.MaxResultBytes = tt.max
			}
			r := newTestRuntime(t, cfg, authorizerFunc(allowAll), executorFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return append(json.RawMessage(nil), tt.result...), nil
			}))
			_, err := r.Execute(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`))
			if tt.want == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestExecuteHidesExecutorErrorAndRecoversPanic(t *testing.T) {
	r := newTestRuntime(t, Config{}, authorizerFunc(allowAll), executorFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("sensitive executor detail")
	}))
	_, err := r.Execute(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`))
	if !errors.Is(err, ErrExecutionFailed) || strings.Contains(err.Error(), "sensitive executor detail") {
		t.Fatalf("executor error was not safely classified: %v", err)
	}
	if got := activeExecutions(r); got != 0 {
		t.Fatalf("active executions after executor error = %d", got)
	}

	panicRuntime := newTestRuntime(t, Config{}, authorizerFunc(allowAll), executorFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic("sensitive panic detail")
	}))
	_, err = panicRuntime.Execute(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`))
	if !errors.Is(err, ErrExecutorPanicked) || strings.Contains(err.Error(), "sensitive panic detail") {
		t.Fatalf("panic was not safely classified: %v", err)
	}
	if got := activeExecutions(panicRuntime); got != 0 {
		t.Fatalf("active executions after panic = %d", got)
	}
}

func TestExecuteRejectsNilContext(t *testing.T) {
	r := newTestRuntime(t, Config{}, authorizerFunc(allowAll), executorFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	if _, err := r.Execute(nil, "web.search", json.RawMessage(`{"query":"x"}`)); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("nil context error = %v", err)
	}
}

func activeExecutions(r *Runtime) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

func TestRuntimeConcurrentExecutionDoesNotExceedLimit(t *testing.T) {
	const concurrency = 4
	var started sync.WaitGroup
	started.Add(concurrency)
	release := make(chan struct{})
	r := newTestRuntime(t, Config{MaxConcurrentExecutions: concurrency}, authorizerFunc(allowAll), executorFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		started.Done()
		select {
		case <-release:
			return json.RawMessage(`{}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Execute(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`))
		}()
	}
	started.Wait()
	if got := activeExecutions(r); got != concurrency {
		t.Fatalf("active executions = %d, want %d", got, concurrency)
	}
	close(release)
	wg.Wait()
	if got := activeExecutions(r); got != 0 {
		t.Fatalf("active executions after concurrent calls = %d", got)
	}
}
