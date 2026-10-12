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

// A transient failure has no attempt count to grow from; its deferral is
// the row's age clamped to [BaseBackoff, MaxBackoff].
func TestPublisher_TransientBackoffGrowsWithAge(t *testing.T) {
	p := NewOutboxPublisherWithConfig(nil, nil, "test", zerolog.Nop(), PublisherConfig{})
	for age, want := range map[time.Duration]time.Duration{
		0:                2 * time.Second,
		time.Second:      2 * time.Second,
		30 * time.Second: 30 * time.Second,
		time.Hour:        5 * time.Minute,
	} {
		if got := p.transientBackoff(age); got != want {
			t.Errorf("transientBackoff(%s) = %s, want %s", age, got, want)
		}
	}
}
