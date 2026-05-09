// HMAC-protected return-URL state — see ADR 0070 §"return_url is
// signed". The QTSP redirects the browser to a URL we control;
// the redirect target carries a `state` query parameter the QTSP
// echoes back unmodified. We sign it with a per-session secret so
// the browser can't tamper with the session id (e.g. swap to a
// different tenant's session and watch what happens).
package tsp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// NewStateSecret returns 32 random bytes hex-encoded. The signature
// service stores this on the tsp_signing_sessions row and re-derives
// the state HMAC for verification on the return-URL hit.
func NewStateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SignState produces the state token as `<sessionID>.<hmacHex>`.
// SessionID is plaintext so the handler can look up the row;
// the HMAC binds it to the session-specific secret so a swapped
// session id won't validate.
func SignState(sessionID, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sessionID))
	return sessionID + "." + hex.EncodeToString(mac.Sum(nil))
}

// VerifyState returns the session id if `state` is well-formed and
// HMAC-valid against `secret`. Empty string + false on failure.
func VerifyState(state, secret string) (string, bool) {
	for i := 0; i < len(state); i++ {
		if state[i] != '.' {
			continue
		}
		sid := state[:i]
		got := state[i+1:]
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(sid))
		want := hex.EncodeToString(mac.Sum(nil))
		// constant-time compare to avoid timing leaks; both sides
		// are hex of fixed length so the early-return is safe.
		if hmac.Equal([]byte(got), []byte(want)) {
			return sid, true
		}
		return "", false
	}
	return "", false
}
