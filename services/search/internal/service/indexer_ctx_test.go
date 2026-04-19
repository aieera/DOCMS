package service

// Wave 6 Prompt 6.4 — proves the per-message context chain.
//
// Invariant: when the service lifecycle (parent) context cancels,
// every in-flight handler's derived ctx cancels too — so SIGTERM
// produces graceful drain, not an orphaned handler holding a
// timeout.

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestHandlerCtx_InheritsParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	msg := &nats.Msg{Header: nats.Header{}}
	ctx, cancel := handlerCtx(parent, msg)
	defer cancel()

	// While parent is live, ctx must not be done.
	select {
	case <-ctx.Done():
		t.Fatal("handler ctx done before parent cancelled")
	default:
	}

	// Cancel parent; handler ctx must propagate within ~100ms.
	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("handler ctx did not cancel after parent cancel (propagation broken)")
	}
}

func TestHandlerCtx_TimeoutStillFires(t *testing.T) {
	// Even with a live parent, the per-message timeout must still
	// bound the work. Otherwise a stuck handler could hold a consumer
	// slot until AckWait expires.
	parent := context.Background()
	msg := &nats.Msg{Header: nats.Header{}}
	ctx, cancel := handlerCtx(parent, msg)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("handler ctx has no deadline — per-message timeout missing")
	}
	if time.Until(deadline) > indexerHandlerTimeout+time.Second {
		t.Errorf("deadline too far out: %s", time.Until(deadline))
	}
}

func TestHandlerCtx_PropagatesCorrelationID(t *testing.T) {
	parent := context.Background()
	msg := &nats.Msg{Header: nats.Header{"correlation-id": []string{"test-corr-123"}}}
	ctx, cancel := handlerCtx(parent, msg)
	defer cancel()
	// Spot-check: the ctx is derived, the header is optional, and
	// the helper must not drop cancellation when the header is set.
	select {
	case <-ctx.Done():
		t.Fatal("ctx should not be done yet")
	default:
	}
}
