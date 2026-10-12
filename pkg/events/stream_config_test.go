package events

import (
	"testing"

	"github.com/nats-io/nats.go"
)

// TestStreamConfig_AuditDiscardNew pins O-5: the audit stream is
// evidence. With DiscardOld a full AUDIT_EVENTS stream silently evicts
// its oldest unconsumed messages to admit new ones; DiscardNew makes
// the publish fail instead, so the outbox row stays queued and retries
// (loud backpressure, no lost history). Every other stream keeps
// DiscardOld.
func TestStreamConfig_AuditDiscardNew(t *testing.T) {
	for _, spec := range DefaultStreams {
		cfg := buildPrimaryConfig(spec, 1)
		want := nats.DiscardOld
		if spec.Name == "AUDIT_EVENTS" {
			want = nats.DiscardNew
		}
		if cfg.Discard != want {
			t.Errorf("%s: Discard = %v, want %v", spec.Name, cfg.Discard, want)
		}
	}
}

// TestStreamConfig_DiscardDriftTriggersUpdate: EnsureStreams used to
// compare subjects only, so a broker provisioned before the O-5 change
// would keep AUDIT_EVENTS on DiscardOld forever. A Discard-policy drift
// must count as drift and be pushed with UpdateStream.
func TestStreamConfig_DiscardDriftTriggersUpdate(t *testing.T) {
	var audit StreamSpec
	for _, s := range DefaultStreams {
		if s.Name == "AUDIT_EVENTS" {
			audit = s
		}
	}
	desired := buildPrimaryConfig(audit, 1)
	live := *desired
	live.Discard = nats.DiscardOld
	if !streamDrifted(&live, desired) {
		t.Fatal("discard-policy drift must be reported")
	}
	same := *desired
	if streamDrifted(&same, desired) {
		t.Fatal("identical config must not be reported as drift")
	}
	moved := *desired
	moved.Subjects = []string{"dms.audit.>", "dms.extra.>"}
	if !streamDrifted(&moved, desired) {
		t.Fatal("subject drift must be reported")
	}
}
