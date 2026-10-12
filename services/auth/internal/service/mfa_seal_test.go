// L-1 — MFA secrets (and the LDAP bind password / push ack keys that share
// the helper) under a KeyManager. currentKEK() minted a FRESH DEK on every
// call once Config.KMS was set and stored nothing to recover it, so a
// secret sealed at setup could never be opened again. With a KMS the
// secret is now a self-contained `v2:` envelope (wrapped DEK stored with
// the ciphertext); the legacy local-KEK format keeps decrypting.
package service

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/crypto"
)

func testKMS(t *testing.T) crypto.KeyManager {
	t.Helper()
	master := make([]byte, crypto.DEKSize)
	_, _ = rand.Read(master)
	km, err := crypto.NewLocalKeyManager(base64.StdEncoding.EncodeToString(master), nil)
	require.NoError(t, err)
	return km
}

func TestMFASecret_KMSRoundTrip(t *testing.T) {
	s := &Service{kms: testKMS(t)}
	enc, err := s.encryptMFASecret("JBSWY3DPEHPK3PXP")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(enc, "v2:"), "KMS-sealed secrets use the v2 envelope, got %q", enc)

	got, err := s.decryptMFASecret(enc)
	require.NoError(t, err, "a KMS-sealed secret must open again")
	require.Equal(t, "JBSWY3DPEHPK3PXP", got)

	// The bind-password / push-ack-key helper shares the envelope.
	sealed, err := s.SealBindPassword("bind-pw")
	require.NoError(t, err)
	pw, err := s.unsealBindPassword(sealed)
	require.NoError(t, err)
	require.Equal(t, "bind-pw", pw)

	// Tampering with the wrapped DEK fails closed.
	parts := strings.SplitN(enc, ":", 3)
	require.Len(t, parts, 3)
	bad := parts[0] + ":" + base64.StdEncoding.EncodeToString([]byte("not-a-wrapped-dek-but-long-enough")) + ":" + parts[2]
	_, err = s.decryptMFASecret(bad)
	require.Error(t, err)
}

func TestMFASecret_LegacyDecrypts(t *testing.T) {
	kek := make([]byte, crypto.DEKSize)
	_, _ = rand.Read(kek)

	// A secret sealed before the KMS was configured (local KEK, no prefix).
	legacy := &Service{localKek: kek}
	enc, err := legacy.encryptMFASecret("LEGACYSECRET234")
	require.NoError(t, err)
	require.False(t, strings.HasPrefix(enc, "v2:"))

	// After the deployment adds a KMS, the legacy row still opens.
	upgraded := &Service{localKek: kek, kms: testKMS(t)}
	got, err := upgraded.decryptMFASecret(enc)
	require.NoError(t, err, "legacy local-KEK secrets must keep decrypting once a KMS is configured")
	require.Equal(t, "LEGACYSECRET234", got)

	// And without the local KEK the legacy row cannot be opened.
	_, err = (&Service{kms: testKMS(t)}).decryptMFASecret(enc)
	require.Error(t, err)
}
