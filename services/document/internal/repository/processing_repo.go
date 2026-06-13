package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ProcessingStage is one row of document_processing_stages (ADR 0115) —
// the per-stage status the intelligence workers write and the document
// detail page reads to render "processing failed because X, retry".
type ProcessingStage struct {
	Stage         string     `json:"stage"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	FailureReason *string    `json:"failure_reason,omitempty"`
	FailureDetail *string    `json:"failure_detail,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// ProcessingRepository reads the per-stage processing status rows.
type ProcessingRepository interface {
	ListStages(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]ProcessingStage, error)
}

type processingRepo struct{}

// NewProcessingRepo constructs the repository.
func NewProcessingRepo() ProcessingRepository { return &processingRepo{} }

func (r *processingRepo) ListStages(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]ProcessingStage, error) {
	rows, err := tx.Query(ctx, `
		SELECT stage, status, attempts, failure_reason, failure_detail,
		       started_at, completed_at
		FROM document_processing_stages
		WHERE tenant_id = $1 AND document_id = $2
		ORDER BY stage ASC
	`, tenantID, documentID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()

	var out []ProcessingStage
	for rows.Next() {
		var s ProcessingStage
		if err := rows.Scan(&s.Stage, &s.Status, &s.Attempts,
			&s.FailureReason, &s.FailureDetail, &s.StartedAt, &s.CompletedAt); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
