// Package model — ADR 0086 unified preferences types.
//
// Lives alongside the legacy flat `UserPreference` (see model.go).
// The flat shape is kept for back-compat with the existing
// /preferences GET/PUT routes; the matrix shape below drives the
// new /preferences/matrix surface and the Decide() pipeline.
package model

import "time"

// PrefCell is one row of the matrix: (user, channel, event_type)
// → enable + digest. Composite PK matches notification_preferences.
type PrefCell struct {
	TenantID       string `json:"tenant_id"`
	UserID         string `json:"user_id"`
	Channel        string `json:"channel"`
	EventType      string `json:"event_type"`
	IsEnabled      bool   `json:"is_enabled"`
	DigestEnabled  bool   `json:"digest_enabled"`
}

// Snooze is one explicit "mute event_type until X" override.
// EventType '*' means mute everything.
type Snooze struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	UserID    string    `json:"user_id"`
	EventType string    `json:"event_type"`
	UntilAt   time.Time `json:"until_at"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// DND is the per-user quiet-hours window, applied on top of the matrix.
// Times are wall-clock in the user-supplied timezone.
type DND struct {
	TenantID  string    `json:"tenant_id"`
	UserID    string    `json:"user_id"`
	DNDStart  string    `json:"dnd_start"` // "HH:MM"
	DNDEnd    string    `json:"dnd_end"`
	Timezone  string    `json:"timezone"` // IANA, e.g. America/New_York
	UpdatedAt time.Time `json:"updated_at"`
}

// Digest is one in-flight digest accumulator row.
type Digest struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	UserID        string    `json:"user_id"`
	EventType     string    `json:"event_type"`
	Channel       string    `json:"channel"`
	Events        []byte    `json:"-"` // raw JSONB; service unmarshals
	Count         int       `json:"count"`
	FlushAfterAt  time.Time `json:"flush_after_at"`
}
