package database

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// H-13 (2026-10-12 review): BaseBackoff was documented as 2s but zero
// meant "retry immediately", and every production caller passed a zero
// config — so a NATS blip of a few seconds burned all MaxAttempts and
// dead-lettered every pending event.
func TestPublisher_DefaultBackoffApplied(t *testing.T) {
	p := NewOutboxPublisherWithConfig(nil, nil, "test", zerolog.Nop(), PublisherConfig{})
	for attempts, want := range map[int]time.Duration{
		1: 2 * time.Second,
		2: 4 * time.Second,
		3: 8 * time.Second,
		9: 5 * time.Minute, // capped at MaxBackoff
	} {
		if got := p.backoff(attempts); got != want {
			t.Errorf("default backoff(%d) = %s, want %s", attempts, got, want)
		}
	}

	// A negative BaseBackoff is the explicit "no backoff" opt-out (tests).
	noWait := NewOutboxPublisherWithConfig(nil, nil, "test", zerolog.Nop(),
		PublisherConfig{BaseBackoff: -1})
	if got := noWait.backoff(3); got != 0 {
		t.Errorf("negative BaseBackoff must disable backoff, got %s", got)
	}
}

// A transient failure's deferral depends only on the row's consecutive
// transient failures: BaseBackoff, doubling, capped at the JetStream
// duplicate window (2m) so a retry after a timed-out publish still dedups.
func TestPublisher_TransientBackoffGrowsWithFailures(t *testing.T) {
	p := NewOutboxPublisherWithConfig(nil, nil, "test", zerolog.Nop(), PublisherConfig{})
	for failures, want := range map[int]time.Duration{
		1:  2 * time.Second,
		2:  4 * time.Second,
		3:  8 * time.Second,
		6:  64 * time.Second,
		7:  2 * time.Minute, // capped at the JetStream duplicate window
		40: 2 * time.Minute,
	} {
		if got := p.transientBackoff(failures); got != want {
			t.Errorf("transientBackoff(%d) = %s, want %s", failures, got, want)
		}
	}
	small := NewOutboxPublisherWithConfig(nil, nil, "test", zerolog.Nop(),
		PublisherConfig{MaxBackoff: 10 * time.Second})
	if got := small.transientBackoff(10); got != 10*time.Second {
		t.Errorf("transientBackoff must respect a MaxBackoff below the window, got %s", got)
	}
	off := NewOutboxPublisherWithConfig(nil, nil, "test", zerolog.Nop(), PublisherConfig{BaseBackoff: -1})
	if got := off.transientBackoff(5); got != 0 {
		t.Errorf("negative BaseBackoff must disable transient backoff, got %s", got)
	}
}
