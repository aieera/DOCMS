// Package esign wraps third-party e-signature providers (DocuSign +
// Adobe Sign) behind a single ESignClient interface so the signature
// service stays vendor-agnostic. Per ADR 0071, both vendors expose
// the same five-step shape — Connect (OAuth), Send, Status, Get
// signed PDF, Webhook ingest. The interface mirrors that.
//
// Webhook security is provider-specific (DocuSign HMAC-SHA256 over
// the body; Adobe HMAC over clientId+clientSecret+body). The verify
// step happens inside ParseWebhook so the calling handler can bail
// before touching the DB.
package esign

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Provider identifies which adapter handles a request. Stored in
// signature_requests.provider + esign_oauth_tokens.provider.
type Provider string

const (
	ProviderDocuSign  Provider = "docusign"
	ProviderAdobeSign Provider = "adobe_sign"
	// ProviderMock is for CI + Playwright. Production refuses to
	// resolve it; the boot-time factory checks an explicit env flag.
	ProviderMock Provider = "mock"
)

// ESignClient is the surface every connector implements. Safe for
// concurrent use; adapters carry their own HTTP client + token
// cache.
type ESignClient interface {
	Provider() Provider

	// Send creates an envelope and dispatches it to the listed
	// recipients in order. Returns the vendor envelope id + per-
	// recipient signing URL when the vendor offers in-app signing
	// (DocuSign embedded, Adobe in-process).
	Send(ctx context.Context, req SendReq) (*SendResp, error)

	// GetStatus polls the vendor for current envelope state. Used
	// as a webhook-loss reconciler — webhooks are at-least-once
	// but every vendor we surveyed has lost messages during their
	// own incidents.
	GetStatus(ctx context.Context, req StatusReq) (*StatusResp, error)

	// GetSignedDocument pulls the final signed PDF + (when
	// available) the Certificate of Completion the vendor
	// generates. Caller writes both to storage and creates a new
	// version row.
	GetSignedDocument(ctx context.Context, req GetSignedReq) (*GetSignedResp, error)

	// ParseWebhook verifies the vendor-specific HMAC and decodes
	// the payload into our normalized event shape. Returns
	// ErrSignatureMismatch on bad HMAC; the handler maps that to
	// HTTP 401 and skips the DB write.
	ParseWebhook(headers http.Header, raw []byte) (*WebhookEvent, error)
}

// Recipient is one signer on an envelope. Order drives sequential
// signing; identical orders fan out in parallel.
type Recipient struct {
	Name      string
	Email     string
	Order     int
	Role      string // "signer" | "cc" | "approver"
	Embedded  bool   // when true the adapter returns a signing URL
}

// SendReq is the input to Send.
type SendReq struct {
	TenantID   string
	RequestID  string // our signature_request.id; passed back as customField
	Subject    string
	Message    string
	DocumentName string
	DocumentBytes []byte
	Recipients []Recipient
	// ReturnURL is where the vendor sends the user after embedded
	// signing completes; ignored for email-only flows.
	ReturnURL string
}

// SendResp is what we persist on signature_requests.provider_envelope_id.
type SendResp struct {
	EnvelopeID    string
	Status        string
	SigningURLs   map[string]string // recipient email → embedded URL (if any)
	CreatedAt     time.Time
}

// StatusReq is GetStatus input.
type StatusReq struct {
	TenantID   string
	EnvelopeID string
}

// StatusResp is the normalized envelope state. The string values
// come from the signature_requests.status CHECK so the service can
// pass-through directly.
type StatusResp struct {
	EnvelopeID string
	Status     string // "pending" | "in_progress" | "completed" | "declined" | "expired" | "cancelled"
	Recipients []RecipientStatus
	UpdatedAt  time.Time
}

// RecipientStatus is one recipient's per-envelope state.
type RecipientStatus struct {
	Email    string
	Status   string // "pending" | "viewed" | "signed" | "declined"
	SignedAt *time.Time
	IPAddress string
}

// GetSignedReq is GetSignedDocument input.
type GetSignedReq struct {
	TenantID   string
	EnvelopeID string
	// IncludeCoC controls whether the adapter pulls the
	// Certificate of Completion alongside the signed PDF.
	// Default true; false skips the second round-trip.
	IncludeCoC bool
}

// GetSignedResp is the binary payload back from the vendor.
type GetSignedResp struct {
	SignedPDF []byte
	// CoCPDF is the vendor's Certificate of Completion. Empty when
	// IncludeCoC=false or the vendor doesn't issue one for the
	// envelope type.
	CoCPDF []byte
	ContentType string // typically "application/pdf"
}

// WebhookEvent is the normalized shape the handler stores. We keep
// raw_payload too — auditors sometimes need the verbatim vendor
// JSON, and we don't want to be the only path to it.
type WebhookEvent struct {
	Provider    Provider
	EnvelopeID  string
	EventType   string // "envelope.sent" | "recipient.signed" | "envelope.completed" | "envelope.declined" | "envelope.voided"
	ExternalID  string // vendor event id; used for dedup
	OccurredAt  time.Time
	Status      string // normalized envelope status (same vocab as StatusResp.Status)
	RawPayload  []byte
}

// Errors. Adapters wrap with %w so callers can errors.Is.
var (
	ErrNotConfigured     = errors.New("esign: provider not configured")
	ErrUnauthorized      = errors.New("esign: vendor rejected credentials")
	ErrSignatureMismatch = errors.New("esign: webhook signature invalid")
	ErrTransport         = errors.New("esign: transport failure")
	ErrUnknownEnvelope   = errors.New("esign: envelope not found at vendor")
)
