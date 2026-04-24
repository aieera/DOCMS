// PKCE helpers — RFC 7636. Every modern OAuth2 provider VaultDMS
// integrates with either requires PKCE (Salesforce connected apps
// from ~2023) or strongly prefers it (Microsoft identity platform,
// Google). The flow:
//
//   1. Client generates a 43–128 char cryptographically random
//      verifier. We use 64 chars (48 bytes b64url-encoded).
//   2. Client derives the challenge = BASE64URL(SHA256(verifier)).
//   3. Challenge + method=S256 are sent on the authorize redirect.
//   4. Verifier is sent on the /token code exchange.
//
// Verifiers are single-use: the caller must delete the stored
// verifier immediately after ExchangeCode succeeds OR fails — a
// failed exchange does not mean the code is replayable (auth
// servers one-shot codes), but the verifier-state row should not
// linger longer than the max flow duration (~10 min).

package providers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// NewCodeVerifier returns a fresh high-entropy verifier string.
// 48 random bytes → 64 base64url chars → well inside RFC's 43–128
// range and comfortably above the 256-bit entropy floor.
func NewCodeVerifier() (string, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("pkce verifier rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// CodeChallenge returns the S256 challenge for a verifier.
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
