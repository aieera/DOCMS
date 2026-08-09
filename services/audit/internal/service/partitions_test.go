package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// BUG-01 regression guards for the partition maintainer's scheduling
// contract. The bug this whole change exists to kill was a maintenance
// job that was documented but never ran, so the properties worth pinning
// are the ones that decide whether maintenance actually happens:
// it runs before the caller proceeds, a failure is retried quickly rather
// than once a day, and it stops cleanly on shutdown.

func TestStartMaintainer_FirstPassRunsSynchronouslyAndReportsItsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	want := errors.New("database unavailable")
	var ran atomic.Int64

	got := startMaintainer(ctx, time.Hour, time.Hour, func(context.Context) error {
		ran.Add(1)
		return want
	})

	// Synchronous: the pass must already have happened by the time
	// startMaintainer returns, because main.go relies on partitions being
	// provisioned before the NATS consumer starts.
	if n := ran.Load(); n != 1 {
		t.Fatalf("first pass must run synchronously before returning; ran=%d", n)
	}
	if !errors.Is(got, want) {
		t.Fatalf("first pass error must be returned so the caller can be loud about it; got %v", got)
	}
}

func TestStartMaintainer_RetriesFastAfterFailureThenSettlesToSlowCadence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A failing pass must reschedule at retryInterval (here: immediate),
	// NOT at interval (here: effectively never). If the implementation
	// ever used the slow cadence for failures, this test times out.
	const slow = time.Hour
	const fast = time.Millisecond

	var calls atomic.Int64
	failUntil := int64(3)
	passes := make(chan error, 8)

	startMaintainer(ctx, slow, fast, func(context.Context) error {
		if calls.Add(1) <= failUntil {
			return errors.New("still broken")
		}
		return nil
	}, passes)

	deadline := time.After(5 * time.Second)
	for i := int64(1); i <= failUntil+1; i++ {
		select {
		case err := <-passes:
			if i <= failUntil && err == nil {
				t.Fatalf("pass %d: expected failure", i)
			}
			if i > failUntil && err != nil {
				t.Fatalf("pass %d: expected success, got %v", i, err)
			}
		case <-deadline:
			t.Fatalf("timed out after %d passes: a failed pass must be retried at retryInterval, not interval", i-1)
		}
	}

	// The pass that succeeded must have rescheduled at the SLOW cadence,
	// so nothing more may arrive promptly.
	select {
	case <-passes:
		t.Fatal("after a successful pass the maintainer must settle back to the slow interval")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestStartMaintainer_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int64
	passes := make(chan error, 64)
	startMaintainer(ctx, time.Millisecond, time.Millisecond, func(context.Context) error {
		calls.Add(1)
		return nil
	}, passes)

	<-passes // first pass observed; the loop is running
	cancel()

	// Drain briefly, then confirm the loop has stopped making progress.
	time.Sleep(50 * time.Millisecond)
	settled := calls.Load()
	time.Sleep(50 * time.Millisecond)
	if after := calls.Load(); after != settled {
		t.Fatalf("maintainer kept running after context cancellation: %d → %d", settled, after)
	}
}

// nakDelayFor decides how long a failed audit event waits before
// redelivery. It must never return zero: an immediate NAK burns the
// consumer's whole MaxDeliver budget in milliseconds against a fault that
// needs seconds to clear, which is how a transient failure turns into
// permanently lost audit evidence.
func TestNakDelayFor_AlwaysBacksOff(t *testing.T) {
	// A plain core-NATS message carries no JetStream metadata; it must
	// still get a real delay rather than 0.
	d := nakDelayFor(&nats.Msg{Subject: "dms.document.created.v1"})
	if d <= 0 {
		t.Fatalf("delay for a message without JetStream metadata must be > 0, got %v", d)
	}
	if d != nakRetryDelays[0] {
		t.Fatalf("metadata-less message should get the first backoff step %v, got %v", nakRetryDelays[0], d)
	}
}

func TestNakRetryDelays_AreMonotonicAndNonZero(t *testing.T) {
	if len(nakRetryDelays) == 0 {
		t.Fatal("backoff schedule must not be empty")
	}
	for i, d := range nakRetryDelays {
		if d <= 0 {
			t.Fatalf("backoff step %d must be > 0, got %v", i, d)
		}
		if i > 0 && d <= nakRetryDelays[i-1] {
			t.Fatalf("backoff must increase: step %d (%v) <= step %d (%v)", i, d, i-1, nakRetryDelays[i-1])
		}
	}
	// The schedule has to buy real time — a five-attempt budget that
	// expires in under a minute is not a retry policy.
	var total time.Duration
	for _, d := range nakRetryDelays {
		total += d
	}
	if total < time.Minute {
		t.Fatalf("total retry window %v is too short to survive a transient database fault", total)
	}
}
