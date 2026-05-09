// Redaction review repo (ADR 0079). Reads redaction_candidates +
// redaction_jobs that the intelligence worker writes via the
// candidate-review redaction flow. Disjoint from the legacy
// document_redactions table used by the legal-hold ad-hoc /redact
// endpoint.
package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type RedactionCandidate struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	DocumentID   uuid.UUID
	VersionID    uuid.UUID
	Source       string // 'ner'|'manual'|'llm'
	EntityID     *uuid.UUID
	EntityType   string
	EntityValue  string
	Rectangles   json.RawMessage
	PageNumber   *int32
	CharStart    *int32
	CharEnd      *int32
	Status       string // 'pending'|'approved'|'rejected'|'applied'
	ReviewedBy   *uuid.UUID
	ReviewedAt   *time.Time
	ReviewNote   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type RedactionJob struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	DocumentID          uuid.UUID
	SourceVersionID     uuid.UUID
	RedactedVersionID   *uuid.UUID
	Status              string
	CandidatesSnapshot  json.RawMessage
	CandidateCount      int32
	ErrorMessage        string
	AppliedBy           uuid.UUID
	AppliedAt           time.Time
	CompletedAt         *time.Time
}

type ListRedactionCandidatesOpts struct {
	VersionID  *uuid.UUID
	Status     string
	EntityType string
	Limit      int32
	Offset     int32
}

type RedactionRepository interface {
	ListCandidates(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID, opts ListRedactionCandidatesOpts) ([]RedactionCandidate, int64, error)
	GetCandidate(ctx context.Context, tx pgx.Tx, tenantID, candidateID uuid.UUID) (*RedactionCandidate, error)
	UpdateCandidateStatus(ctx context.Context, tx pgx.Tx, tenantID, candidateID, reviewedBy uuid.UUID, status, note string) error

	CreateJob(ctx context.Context, tx pgx.Tx, j *RedactionJob) error
	GetJob(ctx context.Context, tx pgx.Tx, tenantID, jobID uuid.UUID) (*RedactionJob, error)
	JobForRedactedVersion(ctx context.Context, tx pgx.Tx, tenantID, redactedVersionID uuid.UUID) (*RedactionJob, error)

	// CountApprovedForVersion counts approved candidates eligible for
	// burn-in. Used by the apply handler to short-circuit if zero, and
	// to drive the >50 admin-gate threshold in the UI.
	CountApprovedForVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (int64, error)
	ListApprovedForVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]RedactionCandidate, error)
}

type redactionRepo struct{}

func NewRedactionRepo() RedactionRepository { return &redactionRepo{} }

const selectRedactionCandidateCols = `
    id, tenant_id, document_id, version_id, source,
    entity_id, entity_type, entity_value,
    rectangles, page_number, char_start, char_end,
    status, reviewed_by, reviewed_at, COALESCE(review_note,''),
    created_at, updated_at`

func scanCandidate(row pgx.Row) (*RedactionCandidate, error) {
	var c RedactionCandidate
	if err := row.Scan(
		&c.ID, &c.TenantID, &c.DocumentID, &c.VersionID, &c.Source,
		&c.EntityID, &c.EntityType, &c.EntityValue,
		&c.Rectangles, &c.PageNumber, &c.CharStart, &c.CharEnd,
		&c.Status, &c.ReviewedBy, &c.ReviewedAt, &c.ReviewNote,
		&c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	return &c, nil
}

func (r *redactionRepo) ListCandidates(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID, opts ListRedactionCandidatesOpts) ([]RedactionCandidate, int64, error) {
	limit := opts.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	args := []any{tenantID, documentID}
	wh := "tenant_id = $1 AND document_id = $2"
	if opts.VersionID != nil {
		args = append(args, *opts.VersionID)
		wh += " AND version_id = $" + itoa(len(args))
	}
	if opts.Status != "" {
		args = append(args, opts.Status)
		wh += " AND status = $" + itoa(len(args))
	}
	if opts.EntityType != "" {
		args = append(args, opts.EntityType)
		wh += " AND entity_type = $" + itoa(len(args))
	}

	var total int64
	if err := tx.QueryRow(ctx,
		"SELECT count(*) FROM redaction_candidates WHERE "+wh, args...,
	).Scan(&total); err != nil {
		return nil, 0, mapPgError(err)
	}

	args = append(args, limit, opts.Offset)
	rows, err := tx.Query(ctx,
		"SELECT "+selectRedactionCandidateCols+
			" FROM redaction_candidates WHERE "+wh+
			" ORDER BY page_number ASC NULLS LAST, char_start ASC NULLS LAST"+
			" LIMIT $"+itoa(len(args)-1)+" OFFSET $"+itoa(len(args)),
		args...,
	)
	if err != nil {
		return nil, 0, mapPgError(err)
	}
	defer rows.Close()
	out := make([]RedactionCandidate, 0, 64)
	for rows.Next() {
		c, sErr := scanCandidate(rows)
		if sErr != nil {
			return nil, 0, sErr
		}
		out = append(out, *c)
	}
	return out, total, rows.Err()
}

func (r *redactionRepo) GetCandidate(ctx context.Context, tx pgx.Tx, tenantID, candidateID uuid.UUID) (*RedactionCandidate, error) {
	row := tx.QueryRow(ctx,
		"SELECT "+selectRedactionCandidateCols+
			" FROM redaction_candidates WHERE tenant_id = $1 AND id = $2",
		tenantID, candidateID,
	)
	return scanCandidate(row)
}

func (r *redactionRepo) UpdateCandidateStatus(ctx context.Context, tx pgx.Tx, tenantID, candidateID, reviewedBy uuid.UUID, status, note string) error {
	_, err := tx.Exec(ctx, `
        UPDATE redaction_candidates
           SET status = $3, reviewed_by = $4, reviewed_at = now(),
               review_note = NULLIF($5, ''),
               updated_at = now()
         WHERE tenant_id = $1 AND id = $2`,
		tenantID, candidateID, status, reviewedBy, note,
	)
	return mapPgError(err)
}

func (r *redactionRepo) CountApprovedForVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (int64, error) {
	var n int64
	err := tx.QueryRow(ctx, `
        SELECT count(*) FROM redaction_candidates
         WHERE tenant_id = $1 AND version_id = $2 AND status = 'approved'`,
		tenantID, versionID,
	).Scan(&n)
	return n, mapPgError(err)
}

func (r *redactionRepo) ListApprovedForVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]RedactionCandidate, error) {
	rows, err := tx.Query(ctx,
		"SELECT "+selectRedactionCandidateCols+
			" FROM redaction_candidates"+
			" WHERE tenant_id = $1 AND version_id = $2 AND status = 'approved'"+
			" ORDER BY page_number ASC NULLS LAST, char_start ASC NULLS LAST",
		tenantID, versionID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := make([]RedactionCandidate, 0, 64)
	for rows.Next() {
		c, sErr := scanCandidate(rows)
		if sErr != nil {
			return nil, sErr
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

const selectRedactionJobCols = `
    id, tenant_id, document_id, source_version_id, redacted_version_id,
    status, candidates_snapshot, candidate_count,
    COALESCE(error_message,''), applied_by, applied_at, completed_at`

func scanJob(row pgx.Row) (*RedactionJob, error) {
	var j RedactionJob
	if err := row.Scan(
		&j.ID, &j.TenantID, &j.DocumentID, &j.SourceVersionID, &j.RedactedVersionID,
		&j.Status, &j.CandidatesSnapshot, &j.CandidateCount,
		&j.ErrorMessage, &j.AppliedBy, &j.AppliedAt, &j.CompletedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	return &j, nil
}

func (r *redactionRepo) CreateJob(ctx context.Context, tx pgx.Tx, j *RedactionJob) error {
	_, err := tx.Exec(ctx, `
        INSERT INTO redaction_jobs (
            tenant_id, id, document_id, source_version_id,
            status, candidates_snapshot, candidate_count, applied_by, applied_at
        ) VALUES (
            $1, $2, $3, $4, $5, $6::jsonb, $7, $8, now()
        )`,
		j.TenantID, j.ID, j.DocumentID, j.SourceVersionID,
		j.Status, j.CandidatesSnapshot, j.CandidateCount, j.AppliedBy,
	)
	return mapPgError(err)
}

func (r *redactionRepo) GetJob(ctx context.Context, tx pgx.Tx, tenantID, jobID uuid.UUID) (*RedactionJob, error) {
	row := tx.QueryRow(ctx,
		"SELECT "+selectRedactionJobCols+
			" FROM redaction_jobs WHERE tenant_id = $1 AND id = $2",
		tenantID, jobID,
	)
	return scanJob(row)
}

func (r *redactionRepo) JobForRedactedVersion(ctx context.Context, tx pgx.Tx, tenantID, redactedVersionID uuid.UUID) (*RedactionJob, error) {
	row := tx.QueryRow(ctx,
		"SELECT "+selectRedactionJobCols+
			" FROM redaction_jobs"+
			" WHERE tenant_id = $1 AND redacted_version_id = $2"+
			" ORDER BY applied_at DESC LIMIT 1",
		tenantID, redactedVersionID,
	)
	return scanJob(row)
}
