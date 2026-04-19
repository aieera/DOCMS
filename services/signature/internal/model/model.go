// Package model holds domain types for the signature service.
package model

import "time"

// SignatureRequest represents a request to collect signatures on a document.
type SignatureRequest struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	DocumentID   string    `json:"document_id"`
	VersionID    string    `json:"version_id"`
	CreatedBy    string    `json:"created_by"`
	Status       string    `json:"status"` // pending | in_progress | completed | cancelled | expired
	Provider     string    `json:"provider"` // internal | docusign | adobe_sign
	ExternalID   string    `json:"external_id,omitempty"`
	Signers      []Signer  `json:"signers"`
	CreatedAt    time.Time `json:"created_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Signer is one person required to sign.
type Signer struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	Name       string     `json:"name"`
	Role       string     `json:"role"` // signer | approver | cc
	Order      int        `json:"order"`
	Status     string     `json:"status"` // pending | signed | declined
	SigningURL string     `json:"signing_url,omitempty"`
	SignedAt   *time.Time `json:"signed_at,omitempty"`
	IPAddress  string     `json:"ip_address,omitempty"`
}

// VerificationResult is returned by the verify endpoint.
type VerificationResult struct {
	DocumentID    string       `json:"document_id"`
	Signed        bool         `json:"signed"`
	SignatureCount int         `json:"signature_count"`
	Signatures    []SigInfo    `json:"signatures"`
	TamperEvident bool         `json:"tamper_evident"`
}

// SigInfo describes one embedded signature.
type SigInfo struct {
	SignerName string    `json:"signer_name"`
	SignedAt   time.Time `json:"signed_at"`
	Issuer     string    `json:"issuer,omitempty"`
	Valid      bool      `json:"valid"`
}
