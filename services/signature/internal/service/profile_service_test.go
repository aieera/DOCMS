package service

// Pure-Go unit tests for the ProfileService crypto + invariants.
// The DB-backed paths (repo, pgx tx) are exercised by a follow-up
// integration test using testcontainers; here we concentrate on
// validateCreateProfile + the S3-key helper + envelope roundtrip.

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/pkg/crypto"
	"github.com/vaultdms/vaultdms/services/signature/internal/model"
)

func TestValidateCreateProfile_Happy(t *testing.T) {
	err := validateCreateProfile(CreateProfileInput{
		Name:  "My signature",
		Kind:  model.ProfileKindDraw,
		Image: []byte("dummy-pixels"),
	})
	require.NoError(t, err)
}

func TestValidateCreateProfile_TypedRequiresFont(t *testing.T) {
	err := validateCreateProfile(CreateProfileInput{
		Name:  "Typed sig",
		Kind:  model.ProfileKindTyped,
		Image: []byte("x"),
	})
	require.Error(t, err)
}

func TestValidateCreateProfile_RejectsLargeImage(t *testing.T) {
	big := make([]byte, ProfileMaxBytes+1)
	err := validateCreateProfile(CreateProfileInput{
		Name:  "Oversize",
		Kind:  model.ProfileKindUpload,
		Image: big,
	})
	require.Error(t, err)
}

func TestValidateCreateProfile_RejectsUnknownKind(t *testing.T) {
	err := validateCreateProfile(CreateProfileInput{
		Name:  "x",
		Kind:  "bogus",
		Image: []byte("x"),
	})
	require.Error(t, err)
}

func TestProfileS3Key_UsesBothUUIDs(t *testing.T) {
	tenantA := uuid.New()
	user := uuid.New()
	profile := uuid.New()
	key := profileS3Key(tenantA, user, profile)
	require.Contains(t, key, tenantA.String())
	require.Contains(t, key, user.String())
	require.Contains(t, key, profile.String())

	// Different tenant → different path prefix.
	tenantB := uuid.New()
	keyB := profileS3Key(tenantB, user, profile)
	require.NotEqual(t, key, keyB)
}

// TestEnvelopeRoundtrip_UsingPkgCrypto confirms the encrypt/decrypt
// primitives the service calls actually round-trip. Guards against a
// future refactor that silently swaps GCM for something we can't
// decrypt.
func TestEnvelopeRoundtrip_UsingPkgCrypto(t *testing.T) {
	plaintext := []byte("\x89PNG\r\n\x1a\nfake-drawing-bytes")
	dek, err := crypto.GenerateDEK()
	require.NoError(t, err)
	ct, nonce, err := crypto.EncryptData(plaintext, dek)
	require.NoError(t, err)
	require.NotEqual(t, plaintext, ct)

	pt, err := crypto.DecryptData(ct, nonce, dek)
	require.NoError(t, err)
	require.Equal(t, plaintext, pt)

	// Wrong nonce → decryption fails. Guards against a repo that
	// forgets to persist the nonce alongside the ciphertext.
	badNonce := make([]byte, len(nonce))
	_, err = crypto.DecryptData(ct, badNonce, dek)
	require.Error(t, err)
}

func TestZeroBytes_Wipes(t *testing.T) {
	b := []byte{1, 2, 3, 4}
	zeroBytes(b)
	require.Equal(t, []byte{0, 0, 0, 0}, b)
}
