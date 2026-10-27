package server

import (
	"context"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
)

func TestContextWithOptionalTimeoutZeroHasNoDeadline(t *testing.T) {
	ctx, cancel := contextWithOptionalTimeout(context.Background(), 0)
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Fatal("zero timeout must not add an overall deadline")
	}
	select {
	case <-ctx.Done():
		t.Fatal("zero timeout context must remain active until its parent is cancelled")
	default:
	}
}

func TestContextWithOptionalTimeoutPositiveHasDeadline(t *testing.T) {
	ctx, cancel := contextWithOptionalTimeout(context.Background(), 30)
	defer cancel()

	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("positive timeout must add a deadline")
	}
}

func TestContextWithOptionalTimeoutZeroStillPropagatesParentCancel(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel := contextWithOptionalTimeout(parent, 0)
	defer cancel()
	cancelParent()

	select {
	case <-ctx.Done():
	default:
		t.Fatal("parent cancellation must propagate when timeout is disabled")
	}
}

func TestProxyRequestContextHonorsExplicitZeroTimeout(t *testing.T) {
	server := &Server{cfg: &config.Config{}}
	ctx, cancel := server.proxyRequestContext(context.Background())
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Fatal("explicit zero request timeout must not add an overall deadline")
	}
}

func TestProxyRequestContextHonorsPositiveTimeout(t *testing.T) {
	cfg := &config.Config{}
	cfg.Llama.RequestTimeout = 30
	server := &Server{cfg: cfg}
	ctx, cancel := server.proxyRequestContext(context.Background())
	defer cancel()

	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("positive request timeout must add an overall deadline")
	}
}
