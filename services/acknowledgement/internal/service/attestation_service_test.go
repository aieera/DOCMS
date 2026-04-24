package service

// Raw-level HMAC roundtrip test. The service wraps this with KMS
// unwrap; the guarantee we pin here is that the same inputs + key
// produce the same tag and that any perturbation changes it.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHMAC_VerifyRoundtrip(t *testing.T) {
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
