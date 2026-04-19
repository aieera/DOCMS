// Package sso holds the SAML 2.0 and OIDC configuration and flow logic for
// the auth service. Phase A2 ships SAML SP-initiated SSO; OIDC (A3) and
// SCIM (A4) will live alongside.
package sso

import (
	"time"

	"github.com/google/uuid"
)

// ProviderType enumerates the SSO backends this package supports.
type ProviderType string

const (
	ProviderSAML ProviderType = "saml"
	ProviderOIDC ProviderType = "oidc"
)

// SSOConfig is the admin-managed per-tenant record. Rows live in the
// sso_configs table. The `Config` field is the provider-specific payload,
// parsed into one of SAMLConfig / OIDCConfig downstream.
type SSOConfig struct {
	TenantID    uuid.UUID
	ID          uuid.UUID
	Provider    ProviderType
	DisplayName string
	Config      []byte // raw JSONB from sso_configs.config
	IsActive    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SAMLConfig is the JSON body stored in sso_configs.config when Provider is
// "saml". Admin provides IdP metadata either inline or via URL. Attribute
// mapping tells us which SAML assertion attribute maps to which user field.
type SAMLConfig struct {
	// IdP metadata — exactly one of these must be provided.
	IdPMetadataURL string `json:"idp_metadata_url,omitempty"`
	IdPMetadataXML string `json:"idp_metadata_xml,omitempty"`

	// Attribute names (SAML URIs or friendly names) to pull from the
	// incoming assertion. If a mapping is empty we fall back to
	// well-known defaults (see attributeMappingDefaults).
	AttributeMapping AttributeMapping `json:"attribute_mapping"`

	// Optional: force a specific NameIDFormat. Default: emailAddress.
	NameIDFormat string `json:"nameid_format,omitempty"`

	// Optional: restrict signed assertions / signed responses. Default: both.
	RequireSignedAssertion bool `json:"require_signed_assertion,omitempty"`
	RequireSignedResponse  bool `json:"require_signed_response,omitempty"`

	// Optional: clock skew tolerance for NotBefore/NotOnOrAfter.
	// Default: 120s. Max enforced: 600s.
	ClockSkewSeconds int `json:"clock_skew_seconds,omitempty"`
}

// AttributeMapping is the subset of SAML assertion attributes we care about.
// Values are the SAML attribute URI (or friendly name) on the incoming
// assertion; the service reads the corresponding attribute and stores it.
type AttributeMapping struct {
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Groups      string `json:"groups,omitempty"`
}

// OIDCConfig is the JSON body when Provider is "oidc". Ships in Phase A3.
// Defined here so sso_configs rows can round-trip through the repository.
type OIDCConfig struct {
	IssuerURL    string   `json:"issuer_url"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	RedirectURL  string   `json:"redirect_url"`
	Scopes       []string `json:"scopes,omitempty"`
}
