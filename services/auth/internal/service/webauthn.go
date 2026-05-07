// ADR 0070 — WebAuthn / Passkey service skeleton.
//
// Scope of THIS commit: data layer + config + HMAC session-token
// helpers + stub service methods that 501. The actual
// BeginRegistration / FinishRegistration / BeginLogin / FinishLogin
// handlers wire to go-webauthn/webauthn in a follow-up commit; the
// integration with the existing UserRepository (uuid.UUID + tx
// pattern) + recovery-code reuse needs careful surgery that's
// best done in a focused PR rather than as part of this baseline.
//
// What lands here is the foundation:
//   - WebAuthnConfig loader (env-driven RP metadata)
//   - HMAC-wrapped session tokens (no server-side session store)
//   - Service.WebAuthn handle wired through Config / New
//   - ErrWebAuthnNotImplemented sentinel that the stub handlers
//     return so a 501 surfaces cleanly when the deploy-side wiring
//     hasn't happened yet
package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// SessionTokenTTL bounds the registration/login begin → finish
// round-trip. ADR 0070 §"Flows" — 10 minutes is conservative.
const SessionTokenTTL = 10 * time.Minute

// StepUpTTL is the fresh-presence window. ADR 0070 §"Step-up auth".
const StepUpTTL = 5 * time.Minute

// ErrInvalidSession — the HMAC token didn't verify or has expired
// between begin and finish. Handler maps to 400 with a "restart
// the flow" hint.
var ErrInvalidSession = errors.New("webauthn: session token invalid or expired")

// ErrNoPasskeysRegistered — login flow attempted but the user has
// no creds. Distinct from ErrInvalidCredentials so the handler can
// surface the chicken-and-egg case (user has an account, just
// hasn't added a passkey yet) without leaking enumeration of which
// emails exist — first-time visitors trying the passkey button get
// the same shape as a logged-out attacker probing for users.
// Maps to 404 so the frontend can show "no passkey for this
// account — use password and add one in Settings → Security".
var ErrNoPasskeysRegistered = errors.New("webauthn: no passkeys registered for this account")

// ErrWebAuthnNotImplemented — the per-flow handler hasn't been
// wired yet. Surfaces as 501 from the route. The stub state is
// deliberate: ships the data layer + URL surface so the frontend
// can build against it, then the production wiring lands as a
// targeted follow-up that doesn't conflate 'add the API' with
// 'integrate the lib'.
var ErrWebAuthnNotImplemented = errors.New("webauthn: handler not yet wired (ADR 0070 follow-up)")

// WebAuthnConfig — RP metadata from env. Nil when the deploy
// hasn't set the required vars; service falls back to ALL handlers
// returning ErrWebAuthnNotImplemented in that case.
type WebAuthnConfig struct {
	// RPID is the Relying-Party domain. Browsers verify the
	// assertion is bound to this exact host. Localhost is exempt
	// from HTTPS; prod must be the public domain.
	RPID string
	// RPDisplayName surfaces in the OS authenticator prompt
	// ("Save passkey for VaultDMS").
	RPDisplayName string
	// RPOrigins are the full origins (scheme + port) the lib
	// accepts in clientDataJSON.
	RPOrigins []string
}

// LoadWebAuthnConfigFromEnv reads VAULTDMS_WEBAUTHN_* env vars and
// returns a config (or nil when not configured).
func LoadWebAuthnConfigFromEnv() *WebAuthnConfig {
	rpID := strings.TrimSpace(os.Getenv("VAULTDMS_WEBAUTHN_RPID"))
	if rpID == "" {
		return nil
	}
	originsCSV := os.Getenv("VAULTDMS_WEBAUTHN_ORIGINS")
	if originsCSV == "" {
		originsCSV = "https://" + rpID
	}
	cfg := &WebAuthnConfig{
		RPID:          rpID,
		RPDisplayName: envOr("VAULTDMS_WEBAUTHN_DISPLAY_NAME", "VaultDMS"),
	}
	for _, raw := range strings.Split(originsCSV, ",") {
		o := strings.TrimSpace(raw)
		if o != "" {
			cfg.RPOrigins = append(cfg.RPOrigins, o)
		}
	}
	return cfg
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// ---- HMAC-wrapped session tokens ---------------------------------
//
// Between begin and finish, the lib needs to remember a SessionData
// blob (challenge, allowed creds list, etc.). Rather than a server-
// side session store, we hand the blob back to the client wrapped
// in an HMAC token: the client echoes it on finish, the server
// verifies the HMAC + decodes. Tied to a per-deploy secret so a
// client can't forge a session.
//
// Wire format:
//   base64.URLEncoding(  4-byte body-length  ||  body  ||  32-byte HMAC tag  )
//
// Body is JSON-marshaled SessionEnvelope.

// SessionEnvelope is the payload inside the HMAC token. Generic over
// the session blob (any) so it works for both registration and
// login flows without a pre-commit dependency on the webauthn-lib
// types — the follow-up commit narrows this with SessionData.
type SessionEnvelope struct {
	UserID   string          `json:"u"`
	Flow     string          `json:"f"` // "register" | "login" | "stepup"
	Session  json.RawMessage `json:"s"` // lib-specific session blob, marshaled separately
	IssuedAt int64           `json:"i"`
}

// hmacSecret returns the HMAC key from env. Falls back to the
// shared gateway secret — same trust zone, guaranteed set.
func hmacSecret() ([]byte, error) {
	v := strings.TrimSpace(os.Getenv("VAULTDMS_WEBAUTHN_HMAC_SECRET"))
	if v == "" {
		v = strings.TrimSpace(os.Getenv("VAULTDMS_GATEWAY_SECRET"))
	}
	if v == "" {
		return nil, errors.New("VAULTDMS_WEBAUTHN_HMAC_SECRET (or VAULTDMS_GATEWAY_SECRET) not set")
	}
	return []byte(v), nil
}

// WrapSession HMAC-wraps an envelope into the URL-safe base64 token
// the client echoes back to /finish.
func WrapSession(env SessionEnvelope) (string, error) {
	if env.IssuedAt == 0 {
		env.IssuedAt = time.Now().Unix()
	}
	secret, err := hmacSecret()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	tag := mac.Sum(nil)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(body)))
	combined := make([]byte, 0, 4+len(body)+len(tag))
	combined = append(combined, lenBuf[:]...)
	combined = append(combined, body...)
	combined = append(combined, tag...)
	return base64.URLEncoding.EncodeToString(combined), nil
}

// UnwrapSession verifies the HMAC + TTL, returns the envelope.
// expectedFlow is the load-bearing check: a token issued for a
// "register" flow must NOT decode under "login".
func UnwrapSession(token, expectedFlow string) (*SessionEnvelope, error) {
	secret, err := hmacSecret()
	if err != nil {
		return nil, err
	}
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return nil, ErrInvalidSession
	}
	if len(raw) < 4+32 {
		return nil, ErrInvalidSession
	}
	bodyLen := int(binary.BigEndian.Uint32(raw[:4]))
	if bodyLen <= 0 || 4+bodyLen+32 != len(raw) {
		return nil, ErrInvalidSession
	}
	body := raw[4 : 4+bodyLen]
	tag := raw[4+bodyLen:]
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), tag) {
		return nil, ErrInvalidSession
	}
	var env SessionEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, ErrInvalidSession
	}
	if env.Flow != expectedFlow {
		return nil, ErrInvalidSession
	}
	if time.Since(time.Unix(env.IssuedAt, 0)) > SessionTokenTTL {
		return nil, ErrInvalidSession
	}
	return &env, nil
}
