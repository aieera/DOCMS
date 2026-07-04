package model

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// IRM protected-container export (§5/§8). A container seals one document version
// (encrypted payload in S3 + signed policy header); each recipient gets a
// license whose token, when presented to the online check callback, releases
// the per-recipient-wrapped payload key. Revoking a license (or the container)
// blocks the next open.

// Allowed actions a license may grant. "view" is always implied.
const (
	IRMActionView     = "view"
	IRMActionPrint    = "print"
	IRMActionDownload = "download"
)

func ValidIRMAction(a string) bool {
	switch a {
	case IRMActionView, IRMActionPrint, IRMActionDownload:
		return true
	}
	return false
}

// Recipient binding.
const (
	IRMRecipientUser  = "user"
	IRMRecipientEmail = "email"
)

// IRMContainer is the sealed export.
type IRMContainer struct {
	TenantID       uuid.UUID
	ID             uuid.UUID
	DocumentID     uuid.UUID
	VersionID      uuid.UUID
	SealedBucket   string
	SealedKey      string
	PayloadNonce   []byte
	PayloadSHA256  []byte
	Mime           string
	Title          string
	AllowedActions []string
	ExpiresAt      time.Time
	CreatedBy      uuid.UUID
	CreatedAt      time.Time
	RevokedAt      *time.Time
}

// IRMLicense is one recipient's grant.
type IRMLicense struct {
	TenantID      uuid.UUID
	ID            uuid.UUID
	ContainerID   uuid.UUID
	RecipientType string
	RecipientRef  string
	TokenHash     []byte
	WrappedDEK    []byte
	KeyRef        string
	OpenCount     int
	LastOpenedAt  *time.Time
	RevokedAt     *time.Time
	RevokedBy     *uuid.UUID
	CreatedBy     uuid.UUID
	CreatedAt     time.Time
}

// IRMLicenseEvent is one row of the lifecycle log.
type IRMLicenseEvent struct {
	TenantID     uuid.UUID
	ContainerID  uuid.UUID
	LicenseID    *uuid.UUID
	EventType    string // issued | opened | revoked | denied
	RecipientRef string
	IPHash       string
	UserAgent    string
	Detail       string
}

// IRMContainerSummary is the dashboard row (container + recipient count).
type IRMContainerSummary struct {
	ID             uuid.UUID
	DocumentID     uuid.UUID
	Title          string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	RevokedAt      *time.Time
	RecipientCount int
}

// IRMLicenseKeyRef derives the per-recipient key context used to wrap the
// payload DEK. Distinct per (tenant, license) so recipients are
// cryptographically separated at the wrap layer.
func IRMLicenseKeyRef(tenantID, licenseID uuid.UUID) string {
	return fmt.Sprintf("irm/tenant/%s/license/%s", tenantID, licenseID)
}

// ---- open-time validation (the DoD gate) --------------------------------

// LicenseDenyReason is "" when the open is allowed, else a machine reason the
// handler maps to an HTTP status (revoked/expired -> 410, session_required ->
// 403).
type LicenseDenyReason string

const (
	LicenseOK              LicenseDenyReason = ""
	LicenseRevoked         LicenseDenyReason = "revoked"
	LicenseExpired         LicenseDenyReason = "expired"
	LicenseSessionRequired LicenseDenyReason = "session_required"
)

// ValidateLicenseOpen decides whether a recipient may open the container now.
// Order matters: revocation (license or container) first, then expiry, then the
// internal-binding session check. A user-bound license additionally requires
// the matching authenticated session so a leaked token alone cannot open it.
func ValidateLicenseOpen(container IRMContainer, license IRMLicense, now time.Time, sessionUserID uuid.UUID) LicenseDenyReason {
	if license.RevokedAt != nil || container.RevokedAt != nil {
		return LicenseRevoked
	}
	if now.After(container.ExpiresAt) {
		return LicenseExpired
	}
	if license.RecipientType == IRMRecipientUser {
		// Constant-time compare of the session user against the bound recipient.
		if sessionUserID == uuid.Nil ||
			subtle.ConstantTimeCompare([]byte(sessionUserID.String()), []byte(license.RecipientRef)) != 1 {
			return LicenseSessionRequired
		}
	}
	return LicenseOK
}

// ActionAllowed reports whether the container grants an action ("view" is
// always allowed on a valid license).
func (c IRMContainer) ActionAllowed(action string) bool {
	if action == IRMActionView {
		return true
	}
	for _, a := range c.AllowedActions {
		if a == action {
			return true
		}
	}
	return false
}

// ---- signed policy header (.sdoc) ---------------------------------------

// IRMPolicyHeader is the recipient-facing, HMAC-signed policy manifest — the
// portable ".sdoc". It carries no payload key: opening still requires the online
// callback (which holds the per-recipient wrapped DEK). The license_token is the
// bearer credential the viewer presents to the callback.
type IRMPolicyHeader struct {
	Format         string    `json:"format"`
	ContainerID    string    `json:"container_id"`
	TenantID       string    `json:"tenant_id"`
	DocumentID     string    `json:"document_id"`
	VersionID      string    `json:"version_id"`
	Title          string    `json:"title"`
	Mime           string    `json:"mime"`
	AllowedActions []string  `json:"allowed_actions"`
	ExpiresAt      time.Time `json:"expires_at"`
	RecipientType  string    `json:"recipient_type"`
	RecipientRef   string    `json:"recipient_ref"`
	LicenseToken   string    `json:"license_token"`
	CallbackURL    string    `json:"callback_url"`
	IssuedAt       time.Time `json:"issued_at"`
	Signature      string    `json:"signature"` // base64(HMAC-SHA256(canonical(header without signature)))
}

// IRMFormat is the current container format tag.
const IRMFormat = "sdoc/1"

// canonicalBytes marshals the header with an empty signature for signing/verify
// (deterministic: Go's json.Marshal emits struct fields in declaration order).
func (h IRMPolicyHeader) canonicalBytes() ([]byte, error) {
	c := h
	c.Signature = ""
	return json.Marshal(c)
}

// Sign computes and sets the HMAC-SHA256 signature over the canonical header.
func (h *IRMPolicyHeader) Sign(key []byte) error {
	b, err := h.canonicalBytes()
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	h.Signature = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return nil
}

// Verify checks the header signature in constant time.
func (h IRMPolicyHeader) Verify(key []byte) bool {
	want, err := base64.StdEncoding.DecodeString(h.Signature)
	if err != nil {
		return false
	}
	b, err := h.canonicalBytes()
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	return hmac.Equal(want, mac.Sum(nil))
}
