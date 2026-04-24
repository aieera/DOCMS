package service

// Pure-Go unit tests: no containers. Exercises the three pieces
// that don't need a live Postgres:
//   1. Hash-chain determinism (eventSelfHash + canonicalJSON)
//   2. HMAC verification roundtrip
//   3. StaticRecipientResolver policy validation + dedup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/model"
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

func TestHMAC_VerifyRoundtrip(t *testing.T) {
	// Raw-level test: the service wraps this with KMS unwrap, but
	// the guarantee we care about is that the same inputs + key
	// produce the same tag and that wrong-input-or-key fails.
	key := []byte("this-is-a-32-byte-test-key-aaaaa")
	campaign := uuid.New()
	user := uuid.New()
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(campaign.String()))
	mac.Write([]byte("|"))
	mac.Write([]byte(user.String()))
	mac.Write([]byte("|"))
	mac.Write([]byte(now.Format(time.RFC3339Nano)))
	tag := mac.Sum(nil)

	// Recompute — must match.
	mac2 := hmac.New(sha256.New, key)
	mac2.Write([]byte(campaign.String()))
	mac2.Write([]byte("|"))
	mac2.Write([]byte(user.String()))
	mac2.Write([]byte("|"))
	mac2.Write([]byte(now.Format(time.RFC3339Nano)))
	require.True(t, hmac.Equal(tag, mac2.Sum(nil)))

	// Different timestamp → different tag.
	mac3 := hmac.New(sha256.New, key)
	mac3.Write([]byte(campaign.String()))
	mac3.Write([]byte("|"))
	mac3.Write([]byte(user.String()))
	mac3.Write([]byte("|"))
	mac3.Write([]byte(now.Add(time.Nanosecond).Format(time.RFC3339Nano)))
	require.False(t, hmac.Equal(tag, mac3.Sum(nil)))

	// Humane pretty for a review-time sanity glance (not asserted):
	_ = hex.EncodeToString(tag)
}

func TestStaticRecipientResolver_DedupUsers(t *testing.T) {
	r := StaticRecipientResolver{}
	u1 := uuid.New()
	u2 := uuid.New()
	out, err := r.Resolve(context.Background(), uuid.New(), model.RecipientPolicy{
		Users: []uuid.UUID{u1, u2, u1},
	})
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Contains(t, out, u1)
	require.Contains(t, out, u2)
}

func TestStaticRecipientResolver_RejectsGroups(t *testing.T) {
	r := StaticRecipientResolver{}
	_, err := r.Resolve(context.Background(), uuid.New(), model.RecipientPolicy{
		Groups: []uuid.UUID{uuid.New()},
	})
	require.Error(t, err)
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
