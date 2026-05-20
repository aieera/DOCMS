// Package model holds domain types for the connector service.
package model

import "time"

// ---- Webhooks -------------------------------------------------------------

// WebhookSubscription is one row in webhook_subscriptions.
type WebhookSubscription struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	URL       string    `json:"url"`
	Secret    string    `json:"secret,omitempty"`
	Events    []string  `json:"events"`
	Active    bool      `json:"active"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// WebhookDelivery is one row in webhook_deliveries.
type WebhookDelivery struct {
	ID             string     `json:"id"`
	SubscriptionID string     `json:"subscription_id"`
	TenantID       string     `json:"tenant_id"`
	EventType      string     `json:"event_type"`
	Payload        []byte     `json:"payload"`
	StatusCode     int        `json:"status_code"`
	ResponseBody   string     `json:"response_body,omitempty"`
	Attempts       int        `json:"attempts"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
	DeadLettered   bool       `json:"dead_lettered"`
	CreatedAt      time.Time  `json:"created_at"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
}

// RetryBackoff defines the retry schedule: 5s, 30s, 2m, 15m, 1h, 6h.
var RetryBackoff = []time.Duration{
	5 * time.Second, 30 * time.Second, 2 * time.Minute,
	15 * time.Minute, 1 * time.Hour, 6 * time.Hour,
}

// ---- Connector Configs ----------------------------------------------------

// ConnectorConfig mirrors the connector_configs table created in the
// initial document-service migration. config_encrypted carries the
// vendor client_id + client_secret; oauth_tokens_encrypted carries the
// access/refresh pair. Both are sealed with the per-tenant KEK before
// the repository writes them — fields below carry the sealed bytes.
type ConnectorConfig struct {
	ID                   string     `json:"id"`
	TenantID             string     `json:"tenant_id"`
	ConnectorType        string     `json:"connector_type"` // 'google', 'salesforce', 'm365', etc.
	DisplayName          string     `json:"display_name"`
	ConfigEncrypted      []byte     `json:"-"`
	OAuthTokensEncrypted []byte     `json:"-"`
	IsActive             bool       `json:"is_active"`
	LastSyncAt           *time.Time `json:"last_sync_at,omitempty"`
	SyncStatus           string     `json:"sync_status,omitempty"`
	CreatedBy            string     `json:"-"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// ProviderConfig is the plaintext shape of config_encrypted. Vendor
// client_id + client_secret + any vendor-specific knobs (instance URL
// for Salesforce, region for ServiceNow, etc.).
type ProviderConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// Provider-specific overrides land here:
	InstanceURL string `json:"instance_url,omitempty"`
	Region      string `json:"region,omitempty"`
	// Extra is a free-form key/value bag for vendor-specific
	// settings that don't deserve a typed column. Microsoft 365
	// uses `entra_tenant` here (the directory GUID, or "common"
	// for the multi-tenant Entra app). Adding a new key is a
	// no-migration change; older rows just won't carry it.
	Extra map[string]string `json:"extra,omitempty"`
}

// OAuthTokens is the plaintext shape of oauth_tokens_encrypted.
// Each provider's adapter unseals this on every call.
type OAuthTokens struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenExpiry  time.Time `json:"token_expiry"`
	Scopes       []string  `json:"scopes"`
}

// ---- MCP ------------------------------------------------------------------

// MCPToolCall is a JSON-RPC request for the MCP server.
type MCPToolCall struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// MCPToolResult is a JSON-RPC response.
type MCPToolResult struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *MCPError `json:"error,omitempty"`
}

// MCPError is a JSON-RPC error.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
