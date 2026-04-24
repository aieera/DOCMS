package service

// Hash-chain + canonical-JSON + outbox subject tests. No containers.

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEventSelfHash_Deterministic(t *testing.T) {
	payload := []byte(`{"a":1,"b":"x"}`)
	prev := []byte{1, 2, 3, 4}
	h1 := eventSelfHash(prev, payload)
	h2 := eventSelfHash(prev, payload)
	require.Equal(t, h1, h2)
	require.Len(t, h1, sha256.Size)
}

func TestEventSelfHash_FirstRowNoPrev(t *testing.T) {
	payload := []byte(`{"k":"v"}`)
	h := eventSelfHash(nil, payload)
	expected := sha256.Sum256(payload)
	require.Equal(t, expected[:], h)
}

func TestCanonicalJSON_StableKeyOrder(t *testing.T) {
	p := map[string]any{"z": 1, "a": 2, "m": 3}
	a, err := canonicalJSON(p)
	require.NoError(t, err)
	b, err := canonicalJSON(p)
	require.NoError(t, err)
	require.Equal(t, a, b)
}

func TestEventSubjectsStable(t *testing.T) {
	// Pin the subject strings — downstream (audit, notification,
	// customer consumers) bind to these names.
	require.Equal(t, "dms.acknowledgement.campaign.created.v1", EventCampaignCreated)
	require.Equal(t, "dms.acknowledgement.campaign.closed.v1", EventCampaignClosed)
	require.Equal(t, "dms.acknowledgement.acknowledged.v1", EventAcknowledged)
	require.Equal(t, "dms.acknowledgement.reminded.v1", EventReminded)
	require.Equal(t, "dms.acknowledgement.escalated.v1", EventEscalated)
}

func TestNotifySubjectsStable(t *testing.T) {
	// Notification fan-out subjects. The notification service's
	// `dms.notify.>` consumer matches on the prefix; breaking
	// these strings silently drops delivery. Pin them.
	require.Equal(t, "dms.notify.acknowledgement.campaign.created.v1", NotifyCampaignCreated)
	require.Equal(t, "dms.notify.acknowledgement.reminded.v1", NotifyReminded)
	require.Equal(t, "dms.notify.acknowledgement.escalated.v1", NotifyEscalated)

	// Every notify subject must live under dms.notify.* so the
	// consumer's wildcard subscription captures it.
	for _, subj := range []string{NotifyCampaignCreated, NotifyReminded, NotifyEscalated} {
		require.True(t,
			len(subj) > len("dms.notify.") && subj[:len("dms.notify.")] == "dms.notify.",
			"notify subject must be under dms.notify.*: %s", subj)
	}
}
