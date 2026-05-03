// Classification corrections — single + bulk correction service surface
// (ADR 0059). Every accepted correction writes a row to
// classification_corrections AND emits dms.classify.corrected.v1 via
// the outbox so the active-learning collector (ADR 0060) can pick it up.
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

type CorrectClassificationInput struct {
	DocumentID         uuid.UUID
	VersionID          *uuid.UUID // nil = use current version
	CorrectedCategory  string
	OriginalCategory   string  // optional — server fetches if empty
	OriginalConfidence *float32
	Source             string  // 'manual'|'bulk'|'api'
	Note               string
}

// CorrectClassification records a user-supplied label for a document.
// Edit perm required; the act of correcting is a metadata mutation.
func (s *DocumentService) CorrectClassification(ctx context.Context, in CorrectClassificationInput) (*repository.ClassificationCorrection, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.DocumentID == uuid.Nil {
		return nil, vdmserr.Validation("document_id", "required")
	}
	if in.CorrectedCategory == "" {
		return nil, vdmserr.Validation("corrected_category", "required")
	}
	if len(in.CorrectedCategory) > 200 {
		return nil, vdmserr.Validation("corrected_category", "max 200 chars")
	}
	doc, err := s.requireDocPermission(ctx, tenantID, userID, in.DocumentID, "edit")
	if err != nil {
		return nil, err
	}

	var out *repository.ClassificationCorrection
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Resolve version_id if caller didn't supply one.
		versionID := uuid.Nil
		if in.VersionID != nil {
			versionID = *in.VersionID
		}
		if versionID == uuid.Nil {
			vid, vErr := s.repos.ClassifyCorrections.CurrentVersionID(ctx, tx, tenantID, in.DocumentID)
			if vErr != nil {
				return vErr
			}
			versionID = vid
		}
		// If caller didn't supply original category, look it up best-effort.
		original := in.OriginalCategory
		if original == "" {
			original = doc.DocumentClass
		}

		row, iErr := s.repos.ClassifyCorrections.Insert(ctx, tx, tenantID, userID, repository.ClassifyCorrectionInput{
			DocumentID:         in.DocumentID,
			VersionID:          versionID,
			OriginalCategory:   original,
			CorrectedCategory:  in.CorrectedCategory,
			OriginalConfidence: in.OriginalConfidence,
			CorrectionSource:   in.Source,
			Note:               in.Note,
		})
		if iErr != nil {
			return iErr
		}
		out = row

		// Update documents.document_class so the live view reflects
		// the human-supplied label immediately. Active-learning will
		// retrain from the ledger; this UPDATE keeps the UX honest
		// in the meantime.
		if _, uErr := tx.Exec(ctx, `
			UPDATE documents
			   SET document_class = $3,
			       classification_confidence = 1.0,
			       updated_by = $4,
			       updated_at = NOW()
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, in.DocumentID, in.CorrectedCategory, userID,
		); uErr != nil {
			return uErr
		}

		evt, oErr := model.NewOutboxEvent(tenantID, "dms.classify.corrected.v1", "document", in.DocumentID,
			classifyCorrectedPayload{
				CorrectionID:       row.ID.String(),
				TenantID:           tenantID.String(),
				DocumentID:         in.DocumentID.String(),
				VersionID:          versionID.String(),
				OriginalCategory:   original,
				CorrectedCategory:  in.CorrectedCategory,
				OriginalConfidence: ptrFloat32OrZero(in.OriginalConfidence),
				CorrectionSource:   resolveSource(in.Source),
				CorrectedBy:        userID.String(),
				CorrectedAt:        time.Now().UTC().Format(time.RFC3339Nano),
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	return out, err
}

// BulkCorrectClassification applies the same target category to a list
// of documents. Each correction is a separate row + outbox event but
// they all run in one tx — partial failures roll back the batch.
func (s *DocumentService) BulkCorrectClassification(ctx context.Context, documentIDs []uuid.UUID, correctedCategory, note string) (int, error) {
	if len(documentIDs) == 0 {
		return 0, vdmserr.Validation("document_ids", "required")
	}
	if len(documentIDs) > 500 {
		return 0, vdmserr.Validation("document_ids", "max 500 per request")
	}
	if correctedCategory == "" {
		return 0, vdmserr.Validation("corrected_category", "required")
	}
	count := 0
	for _, id := range documentIDs {
		if _, err := s.CorrectClassification(ctx, CorrectClassificationInput{
			DocumentID:        id,
			CorrectedCategory: correctedCategory,
			Source:            "bulk",
			Note:              note,
		}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *DocumentService) ListClassificationCorrections(ctx context.Context, documentID uuid.UUID) ([]repository.ClassificationCorrection, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireDocPermission(ctx, tenantID, userID, documentID, "view"); err != nil {
		return nil, err
	}
	var out []repository.ClassificationCorrection
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		out, lerr = s.repos.ClassifyCorrections.ListForDocument(ctx, tx, tenantID, documentID)
		return lerr
	})
	return out, err
}

func resolveSource(s string) string {
	switch s {
	case "manual", "bulk", "api":
		return s
	default:
		return "manual"
	}
}

func ptrFloat32OrZero(p *float32) float64 {
	if p == nil {
		return 0
	}
	return float64(*p)
}

type classifyCorrectedPayload struct {
	CorrectionID       string  `json:"correction_id"`
	TenantID           string  `json:"tenant_id"`
	DocumentID         string  `json:"document_id"`
	VersionID          string  `json:"version_id"`
	OriginalCategory   string  `json:"original_category"`
	CorrectedCategory  string  `json:"corrected_category"`
	OriginalConfidence float64 `json:"original_confidence,omitempty"`
	CorrectionSource   string  `json:"correction_source"`
	CorrectedBy        string  `json:"corrected_by"`
	CorrectedAt        string  `json:"corrected_at"`
}
