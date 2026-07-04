// Package siem forwards normalised domain events to external SIEMs (syslog,
// Splunk HEC, Microsoft Sentinel). It runs as an independent durable JetStream
// consumer inside the audit service (siem-* durables), separate from the
// hash-chain consumer, so a slow/down sink can't back up audit ingest. §15.
package siem

import (
	"encoding/json"
	"time"
)

// SinkType enumerates the supported SIEM targets.
type SinkType string

const (
	SinkSyslog   SinkType = "syslog"
	SinkSplunk   SinkType = "splunk_hec"
	SinkSentinel SinkType = "sentinel_hec"
)

// Sink is a per-tenant SIEM forwarding target + its delivery health.
type Sink struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	Name           string     `json:"name"`
	Type           SinkType   `json:"type"`
	Endpoint       string     `json:"endpoint"`
	Token          string     `json:"token,omitempty"` // write-only-ish; masked on read
	Enabled        bool       `json:"enabled"`
	DeliveredCount int64      `json:"delivered_count"`
	FailedCount    int64      `json:"failed_count"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	LastErrorAt    *time.Time `json:"last_error_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// SinkInput is the create/update payload.
type SinkInput struct {
	Name     string   `json:"name"`
	Type     SinkType `json:"type"`
	Endpoint string   `json:"endpoint"`
	Token    string   `json:"token"`
	Enabled  *bool    `json:"enabled"`
}

// NormalizedEvent is the common (ECS-ish) shape every sink receives, decoupling
// the SIEM payload from the internal CloudEvents envelope.
type NormalizedEvent struct {
	Timestamp     string          `json:"@timestamp"`
	TenantID      string          `json:"tenant_id"`
	Subject       string          `json:"event_subject"` // NATS subject, e.g. dms.document.created.v1
	Action        string          `json:"action"`
	Actor         string          `json:"actor,omitempty"`
	ActorName     string          `json:"actor_name,omitempty"`
	ResourceType  string          `json:"resource_type,omitempty"`
	ResourceID    string          `json:"resource_id,omitempty"`
	EventHash     string          `json:"event_hash,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Raw           json.RawMessage `json:"raw,omitempty"`
}
