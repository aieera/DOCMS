// Package model holds domain types for the notification service.
package model

import "time"

// Channel is a delivery channel.
type Channel string

const (
	ChannelInApp Channel = "in_app"
	ChannelEmail Channel = "email"
	ChannelSlack Channel = "slack"
	ChannelTeams Channel = "teams"
	ChannelPush  Channel = "push"
	ChannelSMS   Channel = "sms"
)

// Notification is one row in the notifications table.
type Notification struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenant_id"`
	UserID       string     `json:"user_id"`
	Type         string     `json:"type"` // document.shared, workflow.step_assigned, comment.created, etc.
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	ResourceType string     `json:"resource_type,omitempty"`
	ResourceID   string     `json:"resource_id,omitempty"`
	Channel      Channel    `json:"channel"`
	Read         bool       `json:"read"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
	ReadAt       *time.Time `json:"read_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// UserPreference controls per-user notification settings.
type UserPreference struct {
	TenantID       string `json:"tenant_id"`
	UserID         string `json:"user_id"`
	EmailEnabled   bool   `json:"email_enabled"`
	PushEnabled    bool   `json:"push_enabled"`
	SlackEnabled   bool   `json:"slack_enabled"`
	SMSEnabled     bool   `json:"sms_enabled"`
	QuietHoursFrom int    `json:"quiet_hours_from"` // 0-23 UTC
	QuietHoursTo   int    `json:"quiet_hours_to"`
}

// DeliveryPayload is what the NATS consumer receives.
type DeliveryPayload struct {
	TenantID     string   `json:"tenant_id"`
	UserIDs      []string `json:"user_ids"`
	Type         string   `json:"type"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	ResourceType string   `json:"resource_type,omitempty"`
	ResourceID   string   `json:"resource_id,omitempty"`
}
