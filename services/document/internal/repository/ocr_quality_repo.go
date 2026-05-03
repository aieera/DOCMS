// Repository for ADR 0057 — OCR quality scores + summary + per-tenant
// thresholds config. Single file because the three concerns share lookup
// paths (the dashboard joins all three).
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// ---- types --------------------------------------------------------------

type OCRQualityScore struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	DocumentID      uuid.UUID
	VersionID       uuid.UUID
	PageNumber      int32
	OverallScore    float32
	CharConfidence  *float32
	WordDensity     *float32
	LineRegularity  *float32
	NoiseRatio      *float32
	LanguageScore   *float32
	Issues          []string
	WordCount       int32
	CharCount       int32
	NeedsReview     bool
	Reviewed        bool
	ReviewedBy      *uuid.UUID
	ReviewedAt      *time.Time
	ReviewNote      string
	CreatedAt       time.Time
}

type OCRQualitySummary struct {
	TenantID            uuid.UUID
	DocumentID          uuid.UUID
	VersionID           uuid.UUID
	AvgScore            float32
	MinScore            float32
	MaxScore            float32
	TotalPages          int32
	PagesNeedingReview  int32
	QualityGrade        string // 'excellent'|'good'|'fair'|'poor'
	AutoRetried         bool
	ScoredAt            time.Time
}

type OCRQualityConfig struct {
	TenantID            uuid.UUID
	Enabled             bool
	ReviewThreshold     float32
	ExcellentThreshold  float32
	GoodThreshold       float32
	FairThreshold       float32
	AutoRetryBelow      float32
	NotifyOnPoor        bool
}

type OCRQualityConfigPatch struct {
	Enabled             *bool
	ReviewThreshold     *float32
	ExcellentThreshold  *float32
	GoodThreshold       *float32
	FairThreshold       *float32
	AutoRetryBelow      *float32
	NotifyOnPoor        *bool
}

type ListReviewQueueOpts struct {
	GradeFilter string // empty = all
	Limit       int32
	Offset      int32
}

type ReviewQueueItem struct {
	DocumentID         uuid.UUID
	VersionID          uuid.UUID
	AvgScore           float32
	QualityGrade       string
	PagesNeedingReview int32
	TotalPages         int32
	ScoredAt           time.Time
}

type OCRQualityStats struct {
	TotalDocuments     int64
	DocumentsByGrade   map[string]int64
	OpenReviewPages    int64
	AutoRetriedDocuments int64
}

// ---- interface ----------------------------------------------------------

type OCRQualityRepository interface {
	GetSummary(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (*OCRQualitySummary, error)
	ListScores(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]OCRQualityScore, error)
	GetScore(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*OCRQualityScore, error)
	MarkPageReviewed(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, page int32, reviewer uuid.UUID, note string) (*OCRQualityScore, error)

	ReviewQueue(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, opts ListReviewQueueOpts) ([]ReviewQueueItem, int64, error)
	Stats(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*OCRQualityStats, error)

	GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*OCRQualityConfig, error)
	UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p OCRQualityConfigPatch) (*OCRQualityConfig, error)
}

type ocrQualityRepo struct{}

func NewOCRQualityRepo() OCRQualityRepository { return &ocrQualityRepo{} }

// ---- summary + scores ---------------------------------------------------

const selectOCRSummarySQL = `
SELECT tenant_id, document_id, version_id, avg_score, min_score, max_score,
       total_pages, pages_needing_review, quality_grade, auto_retried, scored_at
  FROM ocr_quality_summary
`

func (r *ocrQualityRepo) GetSummary(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (*OCRQualitySummary, error) {
	row := tx.QueryRow(ctx, selectOCRSummarySQL+` WHERE tenant_id = $1 AND document_id = $2`, tenantID, documentID)
	return scanOCRSummary(row)
}

const selectOCRScoreSQL = `
SELECT id, tenant_id, document_id, version_id, page_number, overall_score,
       char_confidence, word_density, line_regularity, noise_ratio, language_score,
       issues, word_count, char_count, needs_review, reviewed,
       reviewed_by, reviewed_at, COALESCE(review_note, ''), created_at
  FROM ocr_quality_scores
`

func (r *ocrQualityRepo) ListScores(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]OCRQualityScore, error) {
	rows, err := tx.Query(ctx, selectOCRScoreSQL+`
		WHERE tenant_id = $1 AND document_id = $2
		ORDER BY page_number
		LIMIT 500`,
		tenantID, documentID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := make([]OCRQualityScore, 0, 16)
	for rows.Next() {
		s, err := scanOCRScore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, mapPgError(rows.Err())
}

func (r *ocrQualityRepo) GetScore(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*OCRQualityScore, error) {
	row := tx.QueryRow(ctx, selectOCRScoreSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanOCRScore(row)
}

func (r *ocrQualityRepo) MarkPageReviewed(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, page int32, reviewer uuid.UUID, note string) (*OCRQualityScore, error) {
	row := tx.QueryRow(ctx, `
		UPDATE ocr_quality_scores
		   SET reviewed     = true,
		       reviewed_by  = $3,
		       reviewed_at  = now(),
		       review_note  = NULLIF($4, ''),
		       updated_at   = now()
		 WHERE tenant_id = $1 AND version_id = $2 AND page_number = $5
		   AND reviewed = false
		 RETURNING id, tenant_id, document_id, version_id, page_number, overall_score,
		           char_confidence, word_density, line_regularity, noise_ratio, language_score,
		           issues, word_count, char_count, needs_review, reviewed,
		           reviewed_by, reviewed_at, COALESCE(review_note, ''), created_at`,
		tenantID, versionID, reviewer, note, page,
	)
	s, err := scanOCRScore(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, vdmserr.ErrNotFound) {
			return nil, vdmserr.Conflict("page already reviewed or not found")
		}
		return nil, err
	}
	return s, nil
}

// ---- review queue + stats ----------------------------------------------

func (r *ocrQualityRepo) ReviewQueue(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, opts ListReviewQueueOpts) ([]ReviewQueueItem, int64, error) {
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	grade := opts.GradeFilter

	var total int64
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM ocr_quality_summary
		 WHERE tenant_id = $1
		   AND pages_needing_review > 0
		   AND ($2 = '' OR quality_grade = $2)`,
		tenantID, grade,
	).Scan(&total); err != nil {
		return nil, 0, mapPgError(err)
	}
	rows, err := tx.Query(ctx, `
		SELECT document_id, version_id, avg_score, quality_grade,
		       pages_needing_review, total_pages, scored_at
		  FROM ocr_quality_summary
		 WHERE tenant_id = $1
		   AND pages_needing_review > 0
		   AND ($2 = '' OR quality_grade = $2)
		 ORDER BY avg_score ASC, scored_at DESC
		 LIMIT $3 OFFSET $4`,
		tenantID, grade, opts.Limit, opts.Offset,
	)
	if err != nil {
		return nil, 0, mapPgError(err)
	}
	defer rows.Close()
	out := make([]ReviewQueueItem, 0, opts.Limit)
	for rows.Next() {
		var it ReviewQueueItem
		if err := rows.Scan(
			&it.DocumentID, &it.VersionID, &it.AvgScore, &it.QualityGrade,
			&it.PagesNeedingReview, &it.TotalPages, &it.ScoredAt,
		); err != nil {
			return nil, 0, mapPgError(err)
		}
		out = append(out, it)
	}
	return out, total, mapPgError(rows.Err())
}

func (r *ocrQualityRepo) Stats(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*OCRQualityStats, error) {
	out := &OCRQualityStats{
		DocumentsByGrade: map[string]int64{
			"excellent": 0, "good": 0, "fair": 0, "poor": 0,
		},
	}

	if err := tx.QueryRow(ctx, `
		SELECT
		  COUNT(*),
		  COUNT(*) FILTER (WHERE auto_retried)
		FROM ocr_quality_summary
		WHERE tenant_id = $1`, tenantID,
	).Scan(&out.TotalDocuments, &out.AutoRetriedDocuments); err != nil {
		return nil, mapPgError(err)
	}

	rows, err := tx.Query(ctx, `
		SELECT quality_grade, COUNT(*) FROM ocr_quality_summary
		 WHERE tenant_id = $1 GROUP BY quality_grade`, tenantID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	for rows.Next() {
		var grade string
		var cnt int64
		if err := rows.Scan(&grade, &cnt); err != nil {
			rows.Close()
			return nil, mapPgError(err)
		}
		out.DocumentsByGrade[grade] = cnt
	}
	rows.Close()

	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM ocr_quality_scores
		 WHERE tenant_id = $1 AND needs_review = true AND reviewed = false`,
		tenantID,
	).Scan(&out.OpenReviewPages); err != nil {
		return nil, mapPgError(err)
	}
	return out, nil
}

// ---- config -------------------------------------------------------------

const selectOCRConfigSQL = `
SELECT tenant_id, enabled, review_threshold, excellent_threshold,
       good_threshold, fair_threshold, auto_retry_below, notify_on_poor
  FROM ocr_quality_config
`

func (r *ocrQualityRepo) GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*OCRQualityConfig, error) {
	row := tx.QueryRow(ctx, selectOCRConfigSQL+` WHERE tenant_id = $1`, tenantID)
	return scanOCRConfig(row)
}

func (r *ocrQualityRepo) UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p OCRQualityConfigPatch) (*OCRQualityConfig, error) {
	cur, err := r.GetConfig(ctx, tx, tenantID)
	if err != nil && !errors.Is(err, vdmserr.ErrNotFound) {
		return nil, err
	}
	merged := OCRQualityConfig{
		TenantID:           tenantID,
		Enabled:            true,
		ReviewThreshold:    0.60,
		ExcellentThreshold: 0.90,
		GoodThreshold:      0.75,
		FairThreshold:      0.60,
		AutoRetryBelow:     0.40,
		NotifyOnPoor:       false,
	}
	if cur != nil {
		merged = *cur
	}
	if p.Enabled != nil {
		merged.Enabled = *p.Enabled
	}
	if p.ReviewThreshold != nil {
		merged.ReviewThreshold = *p.ReviewThreshold
	}
	if p.ExcellentThreshold != nil {
		merged.ExcellentThreshold = *p.ExcellentThreshold
	}
	if p.GoodThreshold != nil {
		merged.GoodThreshold = *p.GoodThreshold
	}
	if p.FairThreshold != nil {
		merged.FairThreshold = *p.FairThreshold
	}
	if p.AutoRetryBelow != nil {
		merged.AutoRetryBelow = *p.AutoRetryBelow
	}
	if p.NotifyOnPoor != nil {
		merged.NotifyOnPoor = *p.NotifyOnPoor
	}
	if !(merged.ExcellentThreshold >= merged.GoodThreshold &&
		merged.GoodThreshold >= merged.FairThreshold) {
		return nil, vdmserr.Validation("thresholds",
			"must satisfy excellent >= good >= fair")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO ocr_quality_config
		    (tenant_id, enabled, review_threshold, excellent_threshold,
		     good_threshold, fair_threshold, auto_retry_below, notify_on_poor)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET enabled              = EXCLUDED.enabled,
		       review_threshold     = EXCLUDED.review_threshold,
		       excellent_threshold  = EXCLUDED.excellent_threshold,
		       good_threshold       = EXCLUDED.good_threshold,
		       fair_threshold       = EXCLUDED.fair_threshold,
		       auto_retry_below     = EXCLUDED.auto_retry_below,
		       notify_on_poor       = EXCLUDED.notify_on_poor,
		       updated_at           = now()`,
		tenantID, merged.Enabled, merged.ReviewThreshold, merged.ExcellentThreshold,
		merged.GoodThreshold, merged.FairThreshold, merged.AutoRetryBelow,
		merged.NotifyOnPoor,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	return &merged, nil
}

// ---- scanners -----------------------------------------------------------

func scanOCRSummary(s rowScanner) (*OCRQualitySummary, error) {
	var out OCRQualitySummary
	if err := s.Scan(
		&out.TenantID, &out.DocumentID, &out.VersionID,
		&out.AvgScore, &out.MinScore, &out.MaxScore,
		&out.TotalPages, &out.PagesNeedingReview,
		&out.QualityGrade, &out.AutoRetried, &out.ScoredAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	return &out, nil
}

func scanOCRScore(s rowScanner) (*OCRQualityScore, error) {
	var (
		out        OCRQualityScore
		reviewedBy *uuid.UUID
		reviewedAt *time.Time
	)
	if err := s.Scan(
		&out.ID, &out.TenantID, &out.DocumentID, &out.VersionID, &out.PageNumber,
		&out.OverallScore, &out.CharConfidence, &out.WordDensity,
		&out.LineRegularity, &out.NoiseRatio, &out.LanguageScore,
		&out.Issues, &out.WordCount, &out.CharCount,
		&out.NeedsReview, &out.Reviewed,
		&reviewedBy, &reviewedAt, &out.ReviewNote, &out.CreatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	out.ReviewedBy = reviewedBy
	out.ReviewedAt = reviewedAt
	return &out, nil
}

func scanOCRConfig(s rowScanner) (*OCRQualityConfig, error) {
	var c OCRQualityConfig
	if err := s.Scan(
		&c.TenantID, &c.Enabled, &c.ReviewThreshold, &c.ExcellentThreshold,
		&c.GoodThreshold, &c.FairThreshold, &c.AutoRetryBelow, &c.NotifyOnPoor,
	); err != nil {
		return nil, mapPgError(err)
	}
	return &c, nil
}
