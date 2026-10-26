package server

import (
	"context"
	"testing"
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
