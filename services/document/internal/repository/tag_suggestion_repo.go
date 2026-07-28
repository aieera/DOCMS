package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// TagSuggestion is the API-facing shape of one row in tag_suggestions.
type TagSuggestion struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	DocumentID          uuid.UUID
	VersionID           uuid.UUID
	TagName             string
	Source              string          // 'ner'|'classification'|'llm'|'pattern'
	SourceDetail        json.RawMessage // opaque JSON
	Confidence          float32
	Status              string // 'pending'|'accepted'|'rejected'|'auto_applied'
	ReviewedBy          *uuid.UUID
	ReviewedAt          *time.Time
	CreatedAt           time.Time
}

// AutoTagConfig mirrors auto_tag_config rows. Pointers on numeric/bool
// fields are used by UpsertConfig to express "leave unchanged."
type AutoTagConfig struct {
	TenantID             uuid.UUID
	Enabled              bool
	AutoApplyThreshold   float32
	SuggestThreshold     float32
	MaxTagsPerDocument   int32
	BlockedTags          []string
	SourceWeights        json.RawMessage
}

type AutoTagConfigPatch struct {
	Enabled              *bool
	AutoApplyThreshold   *float32
	SuggestThreshold     *float32
	MaxTagsPerDocument   *int32
	BlockedTags          *[]string
	SourceWeights        *json.RawMessage
}

// ReviewAction is one item in a batch-review request.
type ReviewAction struct {
	SuggestionID uuid.UUID
	Action       string // 'accept'|'reject'
}

// ReviewSummary is what the service layer returns to the handler after
// a batch review — used to build both the API response and the outbox
// event payload.
type ReviewSummary struct {
	Accepted []TagSuggestion
	Rejected []TagSuggestion
}

// ListPendingOpts lets the admin queue scope by confidence and paginate.
type ListPendingOpts struct {
	MinConfidence float32 // 0 == no floor
	MaxConfidence float32 // 0 == no ceiling
	Limit         int32   // capped at 200
	Offset        int32
}

type TagSuggestionRepository interface {
	ListByDocument(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) ([]TagSuggestion, error)
	ListPending(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, opts ListPendingOpts) ([]TagSuggestion, int64, error)
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*TagSuggestion, error)
	MarkReviewed(ctx context.Context, tx pgx.Tx, tenantID, id, reviewerID uuid.UUID, newStatus string) (*TagSuggestion, error)
	AppendDocumentTag(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID, tagName string) error

	GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*AutoTagConfig, error)
	UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, patch AutoTagConfigPatch) (*AutoTagConfig, error)
}

type tagSuggestionRepo struct{}

func NewTagSuggestionRepo() TagSuggestionRepository { return &tagSuggestionRepo{} }

const selectTagSuggestionSQL = `
SELECT id, tenant_id, document_id, version_id, tag_name,
       source, source_detail, confidence, status,
       reviewed_by, reviewed_at, created_at
  FROM tag_suggestions
`

func (r *tagSuggestionRepo) ListByDocument(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) ([]TagSuggestion, error) {
	rows, err := tx.Query(ctx, selectTagSuggestionSQL+`
		WHERE tenant_id = $1 AND document_id = $2
		ORDER BY confidence DESC, created_at DESC
		LIMIT 200`,
		tenantID, docID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := make([]TagSuggestion, 0, 16)
	for rows.Next() {
		s, err := scanTagSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, mapPgError(rows.Err())
}

func (r *tagSuggestionRepo) ListPending(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, opts ListPendingOpts) ([]TagSuggestion, int64, error) {
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	minConf := opts.MinConfidence
	maxConf := opts.MaxConfidence
	if maxConf <= 0 {
		maxConf = 1.0
	}

	var total int64
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM tag_suggestions
		 WHERE tenant_id = $1 AND status = 'pending'
		   AND confidence BETWEEN $2 AND $3`,
		tenantID, minConf, maxConf,
	).Scan(&total); err != nil {
		return nil, 0, mapPgError(err)
	}

	rows, err := tx.Query(ctx, selectTagSuggestionSQL+`
		WHERE tenant_id = $1 AND status = 'pending'
		  AND confidence BETWEEN $2 AND $3
		ORDER BY confidence DESC, created_at DESC
		LIMIT $4 OFFSET $5`,
		tenantID, minConf, maxConf, opts.Limit, opts.Offset,
	)
	if err != nil {
		return nil, 0, mapPgError(err)
	}
	defer rows.Close()
	out := make([]TagSuggestion, 0, opts.Limit)
	for rows.Next() {
		s, err := scanTagSuggestion(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *s)
	}
	return out, total, mapPgError(rows.Err())
}

func (r *tagSuggestionRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*TagSuggestion, error) {
	row := tx.QueryRow(ctx, selectTagSuggestionSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanTagSuggestion(row)
}

func (r *tagSuggestionRepo) MarkReviewed(ctx context.Context, tx pgx.Tx, tenantID, id, reviewerID uuid.UUID, newStatus string) (*TagSuggestion, error) {
	if newStatus != "accepted" && newStatus != "rejected" {
		return nil, vdmserr.Validation("status", "must be accepted or rejected")
	}
	row := tx.QueryRow(ctx, `
		UPDATE tag_suggestions
		   SET status = $4, reviewed_by = $3, reviewed_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND status = 'pending'
		 RETURNING id, tenant_id, document_id, version_id, tag_name,
		           source, source_detail, confidence, status,
		           reviewed_by, reviewed_at, created_at`,
		tenantID, id, reviewerID, newStatus,
	)
	s, err := scanTagSuggestion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, vdmserr.ErrNotFound) {
			return nil, vdmserr.Conflict("suggestion is not pending")
		}
		return nil, err
	}
	return s, nil
}

// AppendDocumentTag appends one tag to documents.tags, deduped, keeping
// existing order. Used for accept-side merges. The auto_tag Python task
// uses the same SQL pattern but does it in bulk.
func (r *tagSuggestionRepo) AppendDocumentTag(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID, tagName string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE documents
		   SET tags = (
		       SELECT ARRAY(
		           SELECT DISTINCT t
		             FROM unnest(documents.tags || ARRAY[$3]::text[]) AS t
		       )
		   )
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, docID, tagName,
	)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

const selectAutoTagConfigSQL = `
SELECT tenant_id, enabled, auto_apply_threshold, suggest_threshold,
       max_tags_per_document, blocked_tags, source_weights
  FROM auto_tag_config
`

func (r *tagSuggestionRepo) GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*AutoTagConfig, error) {
	row := tx.QueryRow(ctx, selectAutoTagConfigSQL+` WHERE tenant_id = $1`, tenantID)
	c, err := scanAutoTagConfig(row)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (r *tagSuggestionRepo) UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, patch AutoTagConfigPatch) (*AutoTagConfig, error) {
	cur, err := r.GetConfig(ctx, tx, tenantID)
	if err != nil && !errors.Is(err, vdmserr.ErrNotFound) {
		return nil, err
	}
	merged := AutoTagConfig{
		TenantID:           tenantID,
		Enabled:            true,
		// Keep in sync with DEFAULT_CONFIG in
		// services/intelligence/app/tasks/auto_tag.py (the authoritative
		// worker-side default).
		AutoApplyThreshold: 0.85,
		SuggestThreshold:   0.60,
		MaxTagsPerDocument: 20,
		BlockedTags:        []string{},
		SourceWeights:      json.RawMessage(`{"ner":1.0,"classification":0.8,"llm":0.9,"pattern":0.7}`),
	}
	if cur != nil {
		merged = *cur
	}
	if patch.Enabled != nil {
		merged.Enabled = *patch.Enabled
	}
	if patch.AutoApplyThreshold != nil {
		merged.AutoApplyThreshold = *patch.AutoApplyThreshold
	}
	if patch.SuggestThreshold != nil {
		merged.SuggestThreshold = *patch.SuggestThreshold
	}
	if patch.MaxTagsPerDocument != nil {
		merged.MaxTagsPerDocument = *patch.MaxTagsPerDocument
	}
	if patch.BlockedTags != nil {
		merged.BlockedTags = *patch.BlockedTags
	}
	if patch.SourceWeights != nil {
		merged.SourceWeights = *patch.SourceWeights
	}
	if merged.AutoApplyThreshold < merged.SuggestThreshold {
		return nil, vdmserr.Validation("auto_apply_threshold",
			"must be >= suggest_threshold")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO auto_tag_config
		    (tenant_id, enabled, auto_apply_threshold, suggest_threshold,
		     max_tags_per_document, blocked_tags, source_weights)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET enabled               = EXCLUDED.enabled,
		       auto_apply_threshold  = EXCLUDED.auto_apply_threshold,
		       suggest_threshold     = EXCLUDED.suggest_threshold,
		       max_tags_per_document = EXCLUDED.max_tags_per_document,
		       blocked_tags          = EXCLUDED.blocked_tags,
		       source_weights        = EXCLUDED.source_weights,
		       updated_at            = now()`,
		tenantID, merged.Enabled, merged.AutoApplyThreshold, merged.SuggestThreshold,
		merged.MaxTagsPerDocument, merged.BlockedTags, []byte(merged.SourceWeights),
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	return &merged, nil
}

func scanTagSuggestion(s rowScanner) (*TagSuggestion, error) {
	var (
		t             TagSuggestion
		sourceDetail  []byte
		reviewedBy    *uuid.UUID
		reviewedAt    *time.Time
	)
	if err := s.Scan(
		&t.ID, &t.TenantID, &t.DocumentID, &t.VersionID, &t.TagName,
		&t.Source, &sourceDetail, &t.Confidence, &t.Status,
		&reviewedBy, &reviewedAt, &t.CreatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	t.SourceDetail = json.RawMessage(sourceDetail)
	t.ReviewedBy = reviewedBy
	t.ReviewedAt = reviewedAt
	return &t, nil
}

func scanAutoTagConfig(s rowScanner) (*AutoTagConfig, error) {
	var (
		c             AutoTagConfig
		sourceWeights []byte
	)
	if err := s.Scan(
		&c.TenantID, &c.Enabled, &c.AutoApplyThreshold, &c.SuggestThreshold,
		&c.MaxTagsPerDocument, &c.BlockedTags, &sourceWeights,
	); err != nil {
		return nil, mapPgError(err)
	}
	c.SourceWeights = json.RawMessage(sourceWeights)
	return &c, nil
}
