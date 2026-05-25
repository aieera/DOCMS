// OCR quality service surface (ADR 0057). Wraps the repo with permission
// checks and outbox emission.
package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

// GetOCRQualityForDocument bundles the doc summary + per-page scores.
// View permission required.
func (s *DocumentService) GetOCRQualityForDocument(ctx context.Context, documentID uuid.UUID) (*repository.OCRQualitySummary, []repository.OCRQualityScore, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.requireDocPermission(ctx, tenantID, userID, documentID, "view"); err != nil {
		return nil, nil, err
	}
	var (
		summary *repository.OCRQualitySummary
		scores  []repository.OCRQualityScore
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		sum, sErr := s.repos.OCRQuality.GetSummary(ctx, tx, tenantID, documentID)
		if sErr != nil && !isNotFound(sErr) {
			return sErr
		}
		summary = sum
		ss, lErr := s.repos.OCRQuality.ListScores(ctx, tx, tenantID, documentID)
		if lErr != nil {
			return lErr
		}
		scores = ss
		return nil
	})
	return summary, scores, err
}

// ReviewOCRQualityPage marks a single page as reviewed. Edit perm
// required (review state mutates document metadata).
func (s *DocumentService) ReviewOCRQualityPage(ctx context.Context, documentID, versionID uuid.UUID, page int32, note string) (*repository.OCRQualityScore, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if page <= 0 {
		return nil, vdmserr.Validation("page", "must be > 0")
	}
	if _, err := s.requireDocPermission(ctx, tenantID, userID, documentID, "edit"); err != nil {
		return nil, err
	}
	var out *repository.OCRQualityScore
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		updated, uErr := s.repos.OCRQuality.MarkPageReviewed(ctx, tx, tenantID, versionID, page, userID, note)
		if uErr != nil {
			return uErr
		}
		// Defence-in-depth: the row's document_id MUST match the URL's.
		if updated.DocumentID != documentID {
			return vdmserr.Validation("page", "belongs to a different document")
		}
		out = updated

		evt, oErr := model.NewOutboxEvent(tenantID, "dms.ocr.quality.reviewed.v1", "document", documentID,
			ocrQualityReviewedPayload{
				DocumentID: documentID.String(),
				TenantID:   tenantID.String(),
				VersionID:  versionID.String(),
				Page:       page,
				ReviewedBy: userID.String(),
				ReviewedAt: time.Now().UTC().Format(time.RFC3339Nano),
				Note:       note,
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	return out, err
}

// ListOCRQualityReviewQueue — admin queue for pages flagged for review.
func (s *DocumentService) ListOCRQualityReviewQueue(ctx context.Context, opts repository.ListReviewQueueOpts) ([]repository.ReviewQueueItem, int64, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, 0, err
	}
	var (
		rows  []repository.ReviewQueueItem
		total int64
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		rows, total, lerr = s.repos.OCRQuality.ReviewQueue(ctx, tx, tenantID, opts)
		return lerr
	})
	return rows, total, err
}

func (s *DocumentService) OCRQualityStats(ctx context.Context) (*repository.OCRQualityStats, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.OCRQualityStats
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		st, sErr := s.repos.OCRQuality.Stats(ctx, tx, tenantID)
		if sErr != nil {
			return sErr
		}
		out = st
		return nil
	})
	return out, err
}

func (s *DocumentService) GetOCRQualityConfig(ctx context.Context) (*repository.OCRQualityConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.OCRQualityConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, gErr := s.repos.OCRQuality.GetConfig(ctx, tx, tenantID)
		if gErr != nil && !isNotFound(gErr) {
			return gErr
		}
		out = c
		return nil
	})
	return out, err
}

func (s *DocumentService) UpsertOCRQualityConfig(ctx context.Context, p repository.OCRQualityConfigPatch) (*repository.OCRQualityConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateOCRQualityPatch(p); err != nil {
		return nil, err
	}
	var out *repository.OCRQualityConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, uErr := s.repos.OCRQuality.UpsertConfig(ctx, tx, tenantID, p)
		if uErr != nil {
			return uErr
		}
		out = c
		return nil
	})
	return out, err
}

// ---- validators ---------------------------------------------------------

func validateOCRQualityPatch(p repository.OCRQualityConfigPatch) error {
	for name, v := range map[string]*float32{
		"review_threshold":    p.ReviewThreshold,
		"excellent_threshold": p.ExcellentThreshold,
		"good_threshold":      p.GoodThreshold,
		"fair_threshold":      p.FairThreshold,
		"auto_retry_below":    p.AutoRetryBelow,
	} {
		if v != nil && (*v < 0 || *v > 1) {
			return vdmserr.Validation(name, "must be in [0.0, 1.0]")
		}
	}
	return nil
}

// ---- outbox payload -----------------------------------------------------

type ocrQualityReviewedPayload struct {
	DocumentID string `json:"document_id"`
	TenantID   string `json:"tenant_id"`
	VersionID  string `json:"version_id"`
	Page       int32  `json:"page"`
	ReviewedBy string `json:"reviewed_by"`
	ReviewedAt string `json:"reviewed_at"`
	Note       string `json:"note,omitempty"`
}
