// Package m365 implements the Microsoft 365 / Microsoft Graph
// connector. Owns:
//
//   * OAuth handshake (per-tenant client_id/secret, multi-tenant
//     Entra app by default — `tenant=common`).
//   * Token refresh with the standard 60-second pre-expiry budget.
//   * A Graph API Client (client.go) covering Sites, Drives,
//     Messages, and Teams channels.
//
// Why a fresh package and not the older providers/microsoft?
//
//   The older package shipped a thin Connector covering PollInbox /
//   SharePointListFiles / SendTeamsNotification with limited scopes
//   and no 401-retry. The user-facing surface we now need
//   (Outlook + Word + Excel + PowerPoint + Teams + SharePoint)
//   requires the full Graph scope set + a robust client, and
//   layering on top of the old package would have made the file
//   too noisy. We leave providers/microsoft in place as the dev
//   artifact behind the email poller until ADR 0112's deprecation
//   window closes.
package m365

import (
	"fmt"
	"strings"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/providers"
)

// ProviderName is the value stored in connector_configs.connector_type
// and used to dispatch the OAuth callback. Matches the prompt's
// expectation of a short, URL-safe identifier.
const ProviderName = "m365"

// Authorize / token endpoints. `{tenant}` resolves to either:
//   * "common"  — the multi-tenant Entra ID app (default; works
//     for any tenant who consents to your app).
//   * a directory (tenant) GUID — single-tenant deployments, where
//     the customer has insisted on hosting their own Entra app.
const (
	authzTmpl = "https://login.microsoftonline.com/%s/oauth2/v2.0/authorize"
	tokenTmpl = "https://login.microsoftonline.com/%s/oauth2/v2.0/token"
)

// Scopes required for the feature surface in the connector prompt.
// `offline_access` is mandatory to receive a refresh_token; without
// it the user has to re-consent every hour.
//
// Calendars.ReadWrite is deliberately NOT in the default list — it's
// scope-creep for tenants that don't need calendar integration.
// Per-tenant opt-in is provided via OptionalCalendarScopes below.
var DefaultScopes = []string{
	"offline_access",
	"User.Read",
	"Files.ReadWrite",
	"Mail.ReadWrite",
	"Mail.Send",
	"Sites.ReadWrite.All",
	"Group.Read.All",
	"ChannelMessage.Send",
}

// OptionalCalendarScopes is the extra grant we add when a tenant
// opts in to calendar integration.
var OptionalCalendarScopes = []string{"Calendars.ReadWrite"}

// Connector mirrors providers/google/google.go's shape — `BaseOAuth`
// handles the four-method OAuth surface, the package adds Graph
// methods via Client.
type Connector struct {
	providers.BaseOAuth
	log      zerolog.Logger
	tenantID string // "common" or an Entra ID directory GUID.
}

// Config is what callers pass to New(). Empty TenantID resolves to
// "common"; pass a directory GUID for single-tenant Entra apps.
type Config struct {
	ClientID       string
	ClientSecret   string
	TenantID       string
	ExtraScopes    []string // typically OptionalCalendarScopes
	Log            zerolog.Logger
}

// New constructs a Connector. Validates that ClientID/Secret are
// present so misconfiguration surfaces at boot, not on the first
// OAuth round-trip.
func New(cfg Config) *Connector {
	tid := strings.TrimSpace(cfg.TenantID)
	if tid == "" {
		tid = "common"
	}
	scopes := append([]string{}, DefaultScopes...)
	if len(cfg.ExtraScopes) > 0 {
		scopes = append(scopes, cfg.ExtraScopes...)
	}
	return &Connector{
		BaseOAuth: providers.BaseOAuth{
			ProviderName:  ProviderName,
			AuthEndpoint:  fmt.Sprintf(authzTmpl, tid),
			TokenEndpoint: fmt.Sprintf(tokenTmpl, tid),
			Scopes:        scopes,
			ClientID:      cfg.ClientID,
			ClientSecret:  cfg.ClientSecret,
			Log:           cfg.Log,
		},
		log:      cfg.Log,
		tenantID: tid,
	}
}

// Name returns the provider name (matches the connector_configs row).
func (c *Connector) Name() string { return ProviderName }

// TenantID returns the Entra directory chosen at construction —
// "common" or the specific directory GUID. Surfaced for logging +
// the admin-UI "Connected to <X>" line.
func (c *Connector) TenantID() string { return c.tenantID }
