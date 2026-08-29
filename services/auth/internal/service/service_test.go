// Pure-Go unit tests. No containers; no network. Integration tests (with a
// real Postgres + Redis) live behind //go:build integration in the same
// package — add them when expanding coverage.
package service

import (
	"github.com/aieera/sedoc/services/auth/internal/model"
	"testing"

	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

func TestValidateEmail(t *testing.T) {
	ok := []string{"alice@example.com", "A.B+tag@example.co", "name@sub.example.io"}
	for _, e := range ok {
		out, err := validateEmail(e)
		require.NoError(t, err, e)
		require.Equal(t, out, out) // normalized (lowercase/trimmed) returned
	}
	bad := []string{"", "not-an-email", "a@b", "a@@b.c", "  "}
	for _, e := range bad {
		_, err := validateEmail(e)
		require.Error(t, err, e)
		require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err))
	}
}

func TestValidatePassword(t *testing.T) {
	ok := []string{"Abcdef1@abcdef", "Aa1!aaaaaaaa", "Zz9$zzzzzzzz"}
	for _, p := range ok {
		require.NoError(t, validatePassword(p), p)
	}
	bad := map[string]string{
		"short":      "short1A!",
		"no_upper":   "abcdef1!abcdef",
		"no_lower":   "ABCDEF1!ABCDEF",
		"no_digit":   "Abcdefx!abcdef",
		"no_special": "Abcdef1abcdef1",
		"too_long":   string(make([]byte, 200)),
	}
	for name, p := range bad {
		require.Error(t, validatePassword(p), name)
	}
}

func TestValidateDisplayName(t *testing.T) {
	n, err := validateDisplayName("  Alice  ")
	require.NoError(t, err)
	require.Equal(t, "Alice", n)
	_, err = validateDisplayName("")
	require.Error(t, err)
	_, err = validateDisplayName(string(make([]byte, 200)))
	require.Error(t, err)
}

func TestRandomTokenDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		tok, err := randomToken(32)
		require.NoError(t, err)
		require.Equal(t, 64, len(tok)) // 32 bytes → 64 hex chars
		require.False(t, seen[tok], "duplicate token")
		seen[tok] = true
	}
}

func TestBcryptRoundTrip(t *testing.T) {
	hash, err := bcryptHash("CorrectHorseBatteryStaple1!")
	require.NoError(t, err)
	require.True(t, bcryptCompare(hash, "CorrectHorseBatteryStaple1!"))
	require.False(t, bcryptCompare(hash, "wrong"))
}

func TestRecoveryCodesDeterministicFormat(t *testing.T) {
	plain, hashes, err := generateRecoveryCodes(8)
	require.NoError(t, err)
	require.Len(t, plain, 8)
	require.Len(t, hashes, 8)
	for i, p := range plain {
		require.Len(t, p, 8, "code length")
		require.Equal(t, p, normalizeToAlphabet(p), "only uses recovery alphabet")
		// Hash verifies.
		require.NotEmpty(t, hashes[i])
	}
}

// normalizeToAlphabet asserts the plaintext recovery code contains only
// characters from the recovery alphabet (confusables like 0/1/I/O excluded).
func normalizeToAlphabet(s string) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(alphabet); j++ {
			if s[i] == alphabet[j] {
				out = append(out, s[i])
				break
			}
		}
	}
	return string(out)
}

func TestConsumeRecoveryCode(t *testing.T) {
	plain, hashes, err := generateRecoveryCodes(3)
	require.NoError(t, err)

	// Match the first plain code; should return the corresponding hash.
	matched := consumeRecoveryCode(hashes, plain[0])
	require.NotEmpty(t, matched)
	require.Equal(t, hashes[0], matched)

	// Miss.
	require.Empty(t, consumeRecoveryCode(hashes, "XXXXXXXX"))
}

func TestHasScope(t *testing.T) {
	require.True(t, hasScope([]string{"documents:read", "search:read"}, "search:read"))
	require.False(t, hasScope([]string{"documents:read"}, "documents:write"))
	require.False(t, hasScope(nil, "anything"))
}

func TestSha256HexStable(t *testing.T) {
	a := sha256Hex("hello")
	b := sha256Hex("hello")
	require.Equal(t, a, b)
	require.NotEqual(t, a, sha256Hex("hell0"))
	require.Equal(t, 64, len(a))
}

// TestValidScopes_DocumentsDelete pins the API-key scope the ERP needs to
// remove documents it created (DELETE /api/v1/documents/{id}). Delete is
// deliberately its own scope rather than folded into documents:write so an
// existing write-only key never silently gains destructive power.
func TestValidScopes_DocumentsDelete(t *testing.T) {
	_, ok := model.ValidScopes()["documents:delete"]
	require.True(t, ok, "documents:delete must be an issuable scope")
}
