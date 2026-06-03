// Package service is the auth business-logic layer. Public methods NEVER
// log or return: passwords, password hashes, plaintext tokens, API keys,
// MFA secrets, recovery codes, or SAML/OIDC assertions. Constant-time
// comparison is used for every secret; bcrypt cost is 12.
package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

// Tunables. Exposed as constants so tests and main.go can reference them.
const (
	BCryptCost              = 12
	SessionTTL              = 24 * time.Hour
	SessionSlidingThreshold = 1 * time.Hour // extend when less than this remains
	SessionMaxLifetime      = 7 * 24 * time.Hour
	ConcurrentSessionLimit  = 5
	// Login attempt budget. Raised from 5 → 10 in this 15-minute
	// window so the MFA flow (login → MFA verify → MFA recovery) has
	// headroom for normal user error without locking the account
	// after a single failed enrol attempt. Still tight enough to
	// shoulder-attack credential stuffing.
	LoginAttemptsWindow     = 15 * time.Minute
	LoginAttemptsMax        = 10
	MFASessionTTL           = 5 * time.Minute
	MFAAttemptsMax          = 3
	APIKeyMaxPerUser        = 20
	APIKeyPrefixLen         = 12 // chars of plaintext key shown for identification
)

// Service orchestrates repos + Redis for rate limiting and session cache.
type Service struct {
	pool     *pgxpool.Pool
	rdb      *redis.Client
	users    repository.UserRepository
	sessions repository.SessionRepository
	apiKeys  repository.APIKeyRepository
	webauthn repository.WebAuthnRepository
	outbox   *database.OutboxRepository
	kms      crypto.KeyManager // used to wrap MFA secrets; falls back if nil
	localKek []byte            // non-nil in dev with VAULTDMS_LOCAL_KEK set
	// WebAuthnLib is the *webauthn.WebAuthn instance, opaque to the
	// rest of the service. Wired by webauthn.go when the
	// VAULTDMS_WEBAUTHN_RPID env var is present; nil otherwise
	// (handlers return ErrWebAuthnNotImplemented in that case).
	WebAuthnLib any
	// ldap is the optional LDAP/AD bundle (ADR 0062). Zero value
	// (Repo=nil) disables every LDAP code path.
	ldap LDAPDeps
	// mfa is the optional multi-method MFA bundle (ADR 0063).
	// Nil-valued fields degrade gracefully — e.g. mfa.SMS=nil drops
	// the SMS factor from the login picker.
	mfa MFADeps
	// notifRepo is the per-tenant Twilio/SMTP credential store. When
	// a tenant has saved credentials via the admin UI, the MFA senders
	// are built fresh per-call from these rows instead of reusing the
	// env-mode senders. Nil disables the per-tenant path.
	notifRepo *repository.Repository
	// notifSealKey is derived from LocalKEK with a fixed domain prefix
	// shared between auth and notification services so the same
	// password_sealed column unseals from either side.
	notifSealKey []byte
	log      zerolog.Logger
	now      func() time.Time
	// m365 holds the verifier for the Outlook/Word add-in token
	// exchange. Configured via env (VAULTDMS_M365_AUDIENCE +
	// VAULTDMS_M365_ALLOWED_TIDS). When unset, ExchangeM365Token
	// refuses to run rather than falling back to the pre-audit
	// Graph-only flow that allowed cross-tenant takeover.
	m365 *m365Verifier
}

// Config bundles all service dependencies; allows tests to swap them.
type Config struct {
	Pool     *pgxpool.Pool
	Redis    *redis.Client
	Users    repository.UserRepository
	Sessions repository.SessionRepository
	APIKeys  repository.APIKeyRepository
	WebAuthn repository.WebAuthnRepository // optional; nil disables passkey routes
	Outbox   *database.OutboxRepository
	KMS      crypto.KeyManager // optional; if nil, LocalKEK used as-is for MFA encryption
	LocalKEK []byte            // 32 bytes, required only when KMS is nil
	Logger   zerolog.Logger
}

// New constructs a Service.
func New(cfg Config) *Service {
	return &Service{
		pool:     cfg.Pool,
		rdb:      cfg.Redis,
		users:    cfg.Users,
		sessions: cfg.Sessions,
		apiKeys:  cfg.APIKeys,
		webauthn: cfg.WebAuthn,
		outbox:   cfg.Outbox,
		kms:      cfg.KMS,
		localKek: cfg.LocalKEK,
		log:      cfg.Logger,
		now:      time.Now,
		m365:     newM365Verifier(),
	}
}

// ---- Password policy ------------------------------------------------------

var (
	emailRE   = regexp.MustCompile(`^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)
	specialRE = regexp.MustCompile(`[!@#$%^&*]`)
)

// validateEmail returns a normalized (lowercased, trimmed) email or a
// validation error. Uses the standard domain-literal regex.
func validateEmail(raw string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "" {
		return "", vdmserr.Validation("email", "required")
	}
	if len(e) > 255 {
		return "", vdmserr.Validation("email", "max 255 chars")
	}
	if !emailRE.MatchString(e) {
		return "", vdmserr.Validation("email", "not a valid email address")
	}
	return e, nil
}

// validatePassword enforces: 12–128 chars, ≥1 upper/lower/digit/special.
// Returns a validation error on failure.
func validatePassword(pw string) error {
	if n := len(pw); n < 12 || n > 128 {
		return vdmserr.Validation("password", "length must be 12..128")
	}
	var hasUpper, hasLower, hasDigit, hasSpec bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	hasSpec = specialRE.MatchString(pw)
	if !(hasUpper && hasLower && hasDigit && hasSpec) {
		return vdmserr.Validation("password", "must include upper, lower, digit, and one of !@#$%^&*")
	}
	return nil
}

// validateDisplayName returns a trimmed name or a validation error.
func validateDisplayName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" || len(n) > 100 {
		return "", vdmserr.Validation("display_name", "1..100 characters")
	}
	return n, nil
}

// ---- Token & hash helpers -------------------------------------------------

// randomToken returns `n` bytes of crypto/rand hex-encoded (2n chars).
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// sha256Hex is the canonical "store the hash, forget the plaintext" helper.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// bcryptHash is a thin helper pinning cost 12.
func bcryptHash(plaintext string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plaintext), BCryptCost)
	if err != nil {
		return "", fmt.Errorf("bcrypt: %w", err)
	}
	return string(b), nil
}

// bcryptCompare wraps bcrypt.CompareHashAndPassword — which is itself
// constant-time per docs. Returns true on match.
func bcryptCompare(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// ---- Error normalization --------------------------------------------------

// ErrInvalidCredentials is the opaque error handlers should surface for
// every failed login / wrong-password / unknown-email / suspended-user
// case. We never reveal WHICH of those is true.
var ErrInvalidCredentials = vdmserr.Unauthorized("invalid credentials")

// ErrAccountLocked is returned when too many failed login attempts have
// tripped the rate-limit lockout. The auth HTTP writeError overrides the
// status to 429 (RATE_LIMITED); Forbidden is the closest pkg/errors kind.
var ErrAccountLocked = vdmserr.Forbidden("account temporarily locked")

// asInvalidCredentials maps any login error into the opaque sentinel.
func asInvalidCredentials(err error) error {
	if errors.Is(err, ErrAccountLocked) {
		return err
	}
	return ErrInvalidCredentials
}

// ---- Small utilities ------------------------------------------------------

func newUUID() (uuid.UUID, error) { return uuid.NewV7() }

func (s *Service) clock() time.Time { return s.now().UTC() }
