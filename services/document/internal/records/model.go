// Package records implements the records-management domain: a file plan of
// categories/series, retention schedules attached to plan nodes, and record
// declarations that freeze a document until a certified disposition.
//
// Tables (migration 000080): retention_schedules, record_categories, records.
//
// Events (outbox, same txn as the write):
//
//	dms.record.declared.v1   on POST /api/v1/records/declare
//	dms.record.disposed.v1   on POST /api/v1/records/{id}/dispose
//
// dms.record.disposed.v1 is consumed by services/audit, where it is appended
// to the per-tenant hash-chained audit log — that chain is the tamper-evident,
// verifiable disposition certificate. The record row stores the emitted
// event id as disposition_event_id so a verifier can find the audit entry.
//
// Immutability: while a record is in disposition_state declared|cutoff_pending,
// DocumentService blocks edit/delete/move/new-version on its document
// (see RecordsService.IsDeclaredRecord, mirroring the legal-hold gate).
package records

import (
	"time"

	"github.com/google/uuid"
)

// RetentionSchedule = trigger + retention period + disposition action.
type RetentionSchedule struct {
	ID                  uuid.UUID `json:"id"`
	TenantID            uuid.UUID `json:"tenant_id"`
	Name                string    `json:"name"`
	Description         string    `json:"description,omitempty"`
	TriggerEvent        string    `json:"trigger_event"`         // declaration|creation|event|superseded|fixed_date
	RetentionPeriodDays int       `json:"retention_period_days"` // >= 0
	DispositionAction   string    `json:"disposition_action"`    // destroy|transfer|permanent|review
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// RecordCategory is one node of the file-plan tree. parent_id nil = root.
type RecordCategory struct {
	ID                  uuid.UUID  `json:"id"`
	TenantID            uuid.UUID  `json:"tenant_id"`
	ParentID            *uuid.UUID `json:"parent_id,omitempty"`
	Name                string     `json:"name"`
	Code                string     `json:"code,omitempty"`
	NodeType            string     `json:"node_type"` // category|series
	Description         string     `json:"description,omitempty"`
	RetentionScheduleID *uuid.UUID `json:"retention_schedule_id,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// Record is one declaration linking a document to a file-plan node.
type Record struct {
	ID                  uuid.UUID  `json:"id"`
	TenantID            uuid.UUID  `json:"tenant_id"`
	DocumentID          uuid.UUID  `json:"document_id"`
	CategoryID          uuid.UUID  `json:"category_id"`
	RetentionScheduleID *uuid.UUID `json:"retention_schedule_id,omitempty"`
	DispositionAction   string     `json:"disposition_action,omitempty"`
	DispositionState    string     `json:"disposition_state"` // declared|cutoff_pending|disposed|transferred
	DeclaredBy          *uuid.UUID `json:"declared_by,omitempty"`
	DeclaredAt          time.Time  `json:"declared_at"`
	CutoffDate          *time.Time `json:"cutoff_date,omitempty"`
	DisposedBy          *uuid.UUID `json:"disposed_by,omitempty"`
	DisposedAt          *time.Time `json:"disposed_at,omitempty"`
	DispositionEventID  *uuid.UUID `json:"disposition_event_id,omitempty"`
	// E3.2 certification fields.
	VitalRecord  bool           `json:"vital_record"`
	Frozen       bool           `json:"frozen"`
	FrozenBy     *uuid.UUID     `json:"frozen_by,omitempty"`
	FrozenAt     *time.Time     `json:"frozen_at,omitempty"`
	FreezeReason string         `json:"freeze_reason,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	// DocumentTitle is joined in for the disposition queue UI; not persisted.
	DocumentTitle string `json:"document_title,omitempty"`
}

// ---- input DTOs ----------------------------------------------------------

type ScheduleInput struct {
	Name                string `json:"name"`
	Description         string `json:"description"`
	TriggerEvent        string `json:"trigger_event"`
	RetentionPeriodDays int    `json:"retention_period_days"`
	DispositionAction   string `json:"disposition_action"`
}

type CategoryInput struct {
	ParentID            *uuid.UUID `json:"parent_id"`
	Name                string     `json:"name"`
	Code                string     `json:"code"`
	NodeType            string     `json:"node_type"`
	Description         string     `json:"description"`
	RetentionScheduleID *uuid.UUID `json:"retention_schedule_id"`
}

type DeclareInput struct {
	DocumentID uuid.UUID `json:"document_id"`
	CategoryID uuid.UUID `json:"category_id"`
}

type DisposeInput struct {
	// Certify must be true — disposition is a deliberate, audited act.
	Certify bool   `json:"certify"`
	Reason  string `json:"reason"`
}
