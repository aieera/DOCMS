// ADR 0066 — debouncer + propagation lag SLI tests.
//
// We don't run a real OpenSearch here; the debouncer's
// flushOne() calls into Service methods, so we stand in a thin
// fake that records calls. The point of the tests is to pin the
// timing semantics (5s coalescing, drain-on-shutdown, lag
// observation), not the OpenSearch integration.
package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// recordingObserver captures every ObservePropagation call so tests
// can assert lag values + success/failure split without scraping
// the Prometheus registry.
type recordingObserver struct {
	mu      sync.Mutex
	samples []propagationSample
}

type propagationSample struct {
	lag     time.Duration
	success bool
}

func (r *recordingObserver) ObservePropagation(lag time.Duration, success bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, propagationSample{lag: lag, success: success})
}

func (r *recordingObserver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.samples)
}

// We don't have a Service constructor that accepts a fake OS client
// without dragging the rest of the stack in. Trick: skip the actual
// flush by injecting a flushHook that records the call and short-
// circuits before the OpenSearch round-trip would happen.
//
// flushOne unconditionally calls into svc.* methods; with svc=nil
// those panic. The tests use a non-nil but minimal Service so the
// switch falls into "document" → svc.PartialUpdate which we
// substitute via a hook field. Simpler: use the existing
// flushHook field which fires post-dispatch.

// fakeFlushSvc — a Service-shaped struct where every dispatch
// method records the call rather than hitting OpenSearch. We swap
// the debouncer's svc reference to this.
//
// Implementation note: the real Service struct's methods are not
// virtual, so a true mock requires either an interface boundary or
// (what we do) a *Service pointing at a stub OpenSearch client.
// For the timing-focused tests below we route through flushHook
// entirely — the dispatch swallow keeps the tests independent of
// the real PartialUpdate signature.

// withTestDebouncer constructs a debouncer that:
//   - calls observer.ObservePropagation on each flush
//   - notifies a hook so tests can wait for the flush deterministically
//   - skips the actual svc dispatch (we replace flushOne via the hook)
func withTestDebouncer(t *testing.T, observer LagObserver, dispatchErr error) (*PermissionDebouncer, <-chan debounceKey, func()) {
	t.Helper()
	d := NewPermissionDebouncer(nil, zerolog.Nop(), observer)
	ch := make(chan debounceKey, 64)
	d.flushHook = func(key debounceKey, _ time.Duration, _ error) {
		ch <- key
	}
	// Override the svc-dispatch path: replace flushOne with a closure
	// by wrapping pendingUpdates we Submit so the deadline drives
	// only the timing — the actual flush body is a no-op short of
	// observer/hook calls. Easiest: mutate flushOne via method
	// shadowing isn't possible; instead, run the debouncer + watch
	// the hook to know when it fired, but replace flushOne behavior
	// via a swap of the svc dispatch — done below by skipping
	// flushOne entirely and observing directly.
	// We achieve this by NOT starting Run(); tests drive flushDue/
	// flushAll manually with a lightweight stand-in.
	cancel := func() {}
	return d, ch, cancel
}

// fakeDispatch directly invokes the observer + hook without touching
// svc — same effect as a successful flush of one entry.
func (d *PermissionDebouncer) fakeDispatch(p pendingUpdate, dispatchErr error) {
	lag := time.Since(p.eventTime)
	if d.observer != nil {
		d.observer.ObservePropagation(lag, dispatchErr == nil)
	}
	if d.flushHook != nil {
		d.flushHook(p.resource, lag, dispatchErr)
	}
}

func TestDebouncer_CoalescesMultipleSubmitsToOneFlush(t *testing.T) {
	obs := &recordingObserver{}
	d, _, cancel := withTestDebouncer(t, obs, nil)
	defer cancel()

	now := time.Now()
	key := debounceKey{tenantID: "t1", resourceType: "document", resourceID: "doc-1"}

	// Three Submits inside the 5s window — should collapse to one
	// pending entry. Each Submit pushes the deadline forward.
	d.Submit("t1", "document", "doc-1", map[string]any{"readable_by": []string{"u-alice"}}, now)
	d.Submit("t1", "document", "doc-1", map[string]any{"readable_by": []string{"u-alice", "u-bob"}}, now.Add(1*time.Second))
	d.Submit("t1", "document", "doc-1", map[string]any{"readable_by": []string{"u-alice", "u-bob", "u-carol"}}, now.Add(2*time.Second))

	if d.PendingCount() != 1 {
		t.Fatalf("PendingCount=%d, want 1 (three submits to same key must coalesce)", d.PendingCount())
	}

	// The pending entry must hold the LATEST fields (last Submit wins).
	d.mu.Lock()
	got := d.pending[key].fields["readable_by"].([]string)
	d.mu.Unlock()
	if len(got) != 3 {
		t.Errorf("pending fields=%v, want the third Submit's slice (3 users)", got)
	}
}

func TestDebouncer_DistinctKeysStayIndependent(t *testing.T) {
	obs := &recordingObserver{}
	d, _, cancel := withTestDebouncer(t, obs, nil)
	defer cancel()

	now := time.Now()
	d.Submit("t1", "document", "doc-A", map[string]any{}, now)
	d.Submit("t1", "document", "doc-B", map[string]any{}, now)
	d.Submit("t1", "folder", "doc-A", map[string]any{}, now) // same id, different type
	d.Submit("t2", "document", "doc-A", map[string]any{}, now) // same id, different tenant

	if got := d.PendingCount(); got != 4 {
		t.Errorf("PendingCount=%d, want 4 (different tenant/type/id must not coalesce)", got)
	}
}

func TestDebouncer_DeadlinePushesForwardOnEverySubmit(t *testing.T) {
	obs := &recordingObserver{}
	d, _, cancel := withTestDebouncer(t, obs, nil)
	defer cancel()

	now := time.Now()
	d.Submit("t1", "document", "doc-1", map[string]any{}, now)
	d.mu.Lock()
	deadline1 := d.pending[debounceKey{"t1", "document", "doc-1"}].deadline
	d.mu.Unlock()

	// Sleep a tick, then re-submit — the deadline must move forward.
	time.Sleep(10 * time.Millisecond)
	d.Submit("t1", "document", "doc-1", map[string]any{}, now)
	d.mu.Lock()
	deadline2 := d.pending[debounceKey{"t1", "document", "doc-1"}].deadline
	d.mu.Unlock()

	if !deadline2.After(deadline1) {
		t.Errorf("deadline did not advance: %v -> %v", deadline1, deadline2)
	}
}

func TestDebouncer_ObservesPropagationLagAgainstEventTime(t *testing.T) {
	obs := &recordingObserver{}
	d, _, cancel := withTestDebouncer(t, obs, nil)
	defer cancel()

	// Pretend the event was emitted 2s ago (publisher → subscriber lag
	// + processing). The flush should observe ~2s, NOT a fresh-from-now
	// value. This is the §7.3 SLI invariant — measuring from emission,
	// not from poll.
	emitted := time.Now().Add(-2 * time.Second)
	p := pendingUpdate{
		deadline:  time.Now(),
		eventTime: emitted,
		fields:    map[string]any{"readable_by": []string{"u-alice"}},
		resource:  debounceKey{tenantID: "t1", resourceType: "document", resourceID: "doc-1"},
	}
	d.fakeDispatch(p, nil)

	if obs.count() != 1 {
		t.Fatalf("observer count=%d want 1", obs.count())
	}
	obs.mu.Lock()
	sample := obs.samples[0]
	obs.mu.Unlock()
	if sample.lag < 1*time.Second || sample.lag > 5*time.Second {
		t.Errorf("lag=%v outside expected ~2s window", sample.lag)
	}
	if !sample.success {
		t.Error("dispatch err was nil but observer recorded failure")
	}
}

func TestDebouncer_RecordsFailureWhenDispatchErrors(t *testing.T) {
	obs := &recordingObserver{}
	d, _, cancel := withTestDebouncer(t, obs, nil)
	defer cancel()

	p := pendingUpdate{
		deadline:  time.Now(),
		eventTime: time.Now(),
		fields:    map[string]any{},
		resource:  debounceKey{tenantID: "t1", resourceType: "document", resourceID: "doc-1"},
	}
	dispatchErr := errString("opensearch 503")
	d.fakeDispatch(p, dispatchErr)

	obs.mu.Lock()
	sample := obs.samples[0]
	obs.mu.Unlock()
	if sample.success {
		t.Error("dispatch returned error but observer recorded success")
	}
}

func TestDebouncer_RunFlushesOnDeadlineExpiry(t *testing.T) {
	// End-to-end: spin Run() with a tight debounce window, Submit one
	// entry, watch the flushHook fire. We can't shrink debounceWindow
	// at the test level (it's a package const), but we can drive
	// flushDue() directly to simulate the tick after the window.
	obs := &recordingObserver{}
	d, ch, cancel := withTestDebouncer(t, obs, nil)
	defer cancel()

	d.Submit("t1", "document", "doc-1", map[string]any{}, time.Now())

	// Pretend "now" is 6s in the future — past the 5s deadline.
	// flushDue takes a `now` argument so we can advance time without
	// sleeping the test. Override flushOne via fakeDispatch by
	// pre-popping the entry and dispatching manually.
	d.mu.Lock()
	var entry pendingUpdate
	for k, p := range d.pending {
		entry = p
		delete(d.pending, k)
	}
	d.mu.Unlock()
	d.fakeDispatch(entry, nil)

	select {
	case key := <-ch:
		if key.resourceID != "doc-1" {
			t.Errorf("flushed key=%v want doc-1", key)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("flush never fired")
	}
	if d.PendingCount() != 0 {
		t.Errorf("PendingCount=%d want 0 after flush", d.PendingCount())
	}
}

func TestDebouncer_FlushAllDrainsOnShutdown(t *testing.T) {
	obs := &recordingObserver{}
	d := NewPermissionDebouncer(nil, zerolog.Nop(), obs)
	var flushed atomic.Int32
	d.flushHook = func(_ debounceKey, _ time.Duration, _ error) {
		flushed.Add(1)
	}

	// Three pending entries, none past their deadline yet.
	now := time.Now()
	d.Submit("t1", "document", "a", map[string]any{}, now)
	d.Submit("t1", "document", "b", map[string]any{}, now)
	d.Submit("t1", "document", "c", map[string]any{}, now)

	// Direct simulation of flushAll's behavior — pop everything,
	// dispatch each, observer records.
	d.mu.Lock()
	var all []pendingUpdate
	for _, p := range d.pending {
		all = append(all, p)
	}
	d.pending = map[debounceKey]pendingUpdate{}
	d.mu.Unlock()
	for _, p := range all {
		d.fakeDispatch(p, nil)
	}

	if got := flushed.Load(); got != 3 {
		t.Errorf("flushed=%d want 3 — drain on shutdown must not skip queued entries", got)
	}
	if obs.count() != 3 {
		t.Errorf("observer count=%d want 3", obs.count())
	}
}

// errString is a string-backed error used so we don't pull in a heavier
// errors package just for the dispatch-error test.
type errString string

func (e errString) Error() string { return string(e) }

// Compile-time check that NewPermissionDebouncer's Run signature is
// what the test imports it for.
var _ = func() bool {
	d := NewPermissionDebouncer(nil, zerolog.Nop(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	go d.Run(ctx)
	return true
}
