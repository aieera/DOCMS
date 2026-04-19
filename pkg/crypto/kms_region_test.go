package crypto

// Wave 11.7 — region-aware LocalKeyManager tests.
//
// Invariants pinned:
//   - A kekID claiming an unconfigured region fails fast (no silent
//     fallback to the global master).
//   - Ciphertext wrapped under the US-region master does NOT decrypt
//     when the EU-region master is loaded alongside (per-region
//     isolation).
//   - A pre-regional kekID ("vaultdms/tenant/<uuid>") still uses the
//     global master — backwards compatibility with pre-11.7 data.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func newB64Secret(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func TestLocalKM_BackwardsCompat_NoRegionMap(t *testing.T) {
	km, err := NewLocalKeyManager(newB64Secret(t), nil)
	require.NoError(t, err)
	_, wrapped, err := km.GenerateDataKey(context.Background(), "vaultdms/tenant/abc")
	require.NoError(t, err)
	plain, err := km.DecryptDataKey(context.Background(), "vaultdms/tenant/abc", wrapped)
	require.NoError(t, err)
	require.Len(t, plain, 32)
}

func TestLocalKM_RegionalSelection(t *testing.T) {
	us := newB64Secret(t)
	eu := newB64Secret(t)
	km, err := NewMultiRegionLocalKeyManager(newB64Secret(t), map[string]string{
		"us-east-1": us,
		"eu-west-1": eu,
	}, nil)
	require.NoError(t, err)

	// Wrap under us-east-1 master.
	_, wrapped, err := km.GenerateDataKey(context.Background(), "vaultdms/tenant/abc/us-east-1")
	require.NoError(t, err)

	// Decrypting under us-east-1 works.
	plain, err := km.DecryptDataKey(context.Background(), "vaultdms/tenant/abc/us-east-1", wrapped)
	require.NoError(t, err)
	require.Len(t, plain, 32)

	// Decrypting with the eu-west-1 suffix must NOT work — different
	// master → different KEK → AEAD tag fails.
	_, err = km.DecryptDataKey(context.Background(), "vaultdms/tenant/abc/eu-west-1", wrapped)
	require.Error(t, err)
}

func TestLocalKM_UnknownRegionFailsFast(t *testing.T) {
	km, err := NewMultiRegionLocalKeyManager(newB64Secret(t), map[string]string{
		"us-east-1": newB64Secret(t),
	}, nil)
	require.NoError(t, err)

	// Claiming ap-southeast-2 (unconfigured) must return an error
	// rather than silently falling back to the global master — the
	// silent fallback would break the per-region isolation promise.
	_, _, err = km.GenerateDataKey(context.Background(), "vaultdms/tenant/abc/ap-southeast-2")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no configured master")
}

func TestLocalKM_RotationSuffixPreservesRegion(t *testing.T) {
	// "@v2" rotation suffix on a regional kekID still picks the
	// right region's master. The trim strips the suffix before the
	// region lookup; downstream HKDF uses the full kekID as salt so
	// rotation still produces a distinct KEK from v1.
	us := newB64Secret(t)
	km, err := NewMultiRegionLocalKeyManager(newB64Secret(t), map[string]string{
		"us-east-1": us,
	}, nil)
	require.NoError(t, err)

	_, wrapped, err := km.GenerateDataKey(context.Background(), "vaultdms/tenant/abc/us-east-1@v2")
	require.NoError(t, err)

	// v2 kekID decrypts v2 ciphertext.
	_, err = km.DecryptDataKey(context.Background(), "vaultdms/tenant/abc/us-east-1@v2", wrapped)
	require.NoError(t, err)

	// v1 kekID does NOT decrypt v2 ciphertext (different salt →
	// different KEK even with the same master).
	_, err = km.DecryptDataKey(context.Background(), "vaultdms/tenant/abc/us-east-1", wrapped)
	require.Error(t, err)
}

func TestLocalKM_PreRegionalAliasStillWorks(t *testing.T) {
	// A pre-11.7 kekID (no region suffix) resolves to the default
	// master even when regionMasters is populated. This lets
	// deployments roll forward without re-wrapping every legacy DEK.
	km, err := NewMultiRegionLocalKeyManager(newB64Secret(t), map[string]string{
		"us-east-1": newB64Secret(t),
	}, nil)
	require.NoError(t, err)

	_, wrapped, err := km.GenerateDataKey(context.Background(), "vaultdms/tenant/legacy-uuid")
	require.NoError(t, err)
	plain, err := km.DecryptDataKey(context.Background(), "vaultdms/tenant/legacy-uuid", wrapped)
	require.NoError(t, err)
	require.Len(t, plain, 32)
}
