package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ClassificationCorrection struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	DocumentID          uuid.UUID
	VersionID           uuid.UUID
	OriginalCategory    string
	CorrectedCategory   string
	OriginalConfidence  *float32
	CorrectionSource    string // 'manual'|'bulk'|'api'
	Note                string
	CorrectedBy         uuid.UUID
	CreatedAt           time.Time
}

type ClassifyCorrectionInput struct {
	DocumentID          uuid.UUID
	VersionID           uuid.UUID
	OriginalCategory    string
	CorrectedCategory   string
	OriginalConfidence  *float32
	CorrectionSource    string
	Note                string
}

type ClassifyCorrectionRepository interface {
	Insert(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, in ClassifyCorrectionInput) (*ClassificationCorrection, error)
	ListForDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]ClassificationCorrection, error)
	CurrentVersionID(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (uuid.UUID, error)
}

type classifyCorrectionRepo struct{}

func NewClassifyCorrectionRepo() ClassifyCorrectionRepository { return &classifyCorrectionRepo{} }

func (r *classifyCorrectionRepo) Insert(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, in ClassifyCorrectionInput) (*ClassificationCorrection, error) {
	src := in.CorrectionSource
	if src == "" {
		src = "manual"
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO classification_corrections
		    (tenant_id, document_id, version_id, original_category,
		     corrected_category, original_confidence, correction_source,
		     note, corrected_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9)
		RETURNING id, tenant_id, document_id, version_id,
		          original_category, corrected_category, original_confidence,
		          correction_source, COALESCE(note, ''), corrected_by, created_at`,
		tenantID, in.DocumentID, in.VersionID,
		in.OriginalCategory, in.CorrectedCategory,
		in.OriginalConfidence, src, in.Note, userID,
	)
	return scanClassifyCorrection(row)
}

func (r *classifyCorrectionRepo) ListForDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]ClassificationCorrection, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, document_id, version_id,
		       original_category, corrected_category, original_confidence,
		       correction_source, COALESCE(note, ''), corrected_by, created_at
		  FROM classification_corrections
		 WHERE tenant_id = $1 AND document_id = $2
		 ORDER BY created_at DESC
		 LIMIT 100`,
		tenantID, documentID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := make([]ClassificationCorrection, 0, 16)
	for rows.Next() {
		c, err := scanClassifyCorrection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, mapPgError(rows.Err())
}

// CurrentVersionID resolves a document's current version. Used by the
// correction handler — the user clicks "correct" on the doc, not on a
// specific version, so we look up the head.
func (r *classifyCorrectionRepo) CurrentVersionID(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (uuid.UUID, error) {
	var vid uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM document_versions
		 WHERE tenant_id = $1 AND document_id = $2
		 ORDER BY version_number DESC LIMIT 1`,
		tenantID, documentID,
	).Scan(&vid)
	if err != nil {
		return uuid.Nil, mapPgError(err)
	}
	return vid, nil
}

func scanClassifyCorrection(s rowScanner) (*ClassificationCorrection, error) {
	var (
		c       ClassificationCorrection
		conf    *float32
	)
	if err := s.Scan(
		&c.ID, &c.TenantID, &c.DocumentID, &c.VersionID,
		&c.OriginalCategory, &c.CorrectedCategory, &conf,
		&c.CorrectionSource, &c.Note, &c.CorrectedBy, &c.CreatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	c.OriginalConfidence = conf
	return &c, nil
}
