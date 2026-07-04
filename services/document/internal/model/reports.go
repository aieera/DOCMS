// Saved analytics reports (ADR 0119).
package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SavedReport is one stored report: a governed analytics.Query spec
// (kept as raw JSON here; the analytics package owns the schema),
// a chart hint, and an optional schedule.
type SavedReport struct {
	TenantID                uuid.UUID       `json:"tenant_id"`
	ID                      uuid.UUID       `json:"id"`
	Name                    string          `json:"name"`
	Description             string          `json:"description"`
	Query                   json.RawMessage `json:"query"`
	ChartType               string          `json:"chart_type"`
	ScheduleEnabled         bool            `json:"schedule_enabled"`
	ScheduleCron            string          `json:"schedule_cron"`
	ScheduleIntervalMinutes int             `json:"schedule_interval_minutes"`
	Channels                []string        `json:"channels"`
	CreatedBy               uuid.UUID       `json:"created_by"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
	LastRunAt               *time.Time      `json:"last_run_at,omitempty"`
}

// ReportGeneratedPayload is the dms.report.generated.v1 event body.
type ReportGeneratedPayload struct {
	ReportID    string `json:"report_id"`
	ReportName  string `json:"report_name"`
	Rows        int    `json:"rows"`
	TriggeredBy string `json:"triggered_by"` // "schedule" | "manual"
	RunBy       string `json:"run_by"`
}
