package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

// ListTagSuggestions returns all suggestions for a document. View
// permission required (the suggestion list is metadata about the doc).
func (s *DocumentService) ListTagSuggestions(ctx context.Context, documentID uuid.UUID) ([]repository.TagSuggestion, *repository.AutoTagConfig, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.requireDocPermission(ctx, tenantID, userID, documentID, "view"); err != nil {
		return nil, nil, err
	}
	var (
		out  []repository.TagSuggestion
		conf *repository.AutoTagConfig
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		out, lerr = s.repos.TagSuggestions.ListByDocument(ctx, tx, tenantID, documentID)
		if lerr != nil {
			return lerr
		}
		c, cerr := s.repos.TagSuggestions.GetConfig(ctx, tx, tenantID)
		if cerr != nil && !isNotFound(cerr) {
			return cerr
		}
		conf = c
		return nil
	})
	return out, conf, err
}

// BatchReviewTagSuggestions accepts/rejects multiple suggestions for one
// document in a single tx. Each accept also appends the tag to
// documents.tags. One outbox event covers the whole batch.
func (s *DocumentService) BatchReviewTagSuggestions(ctx context.Context, documentID uuid.UUID, actions []repository.ReviewAction) (*repository.ReviewSummary, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if len(actions) == 0 {
		return nil, vdmserr.Validation("actions", "required")
	}
	if len(actions) > 100 {
		return nil, vdmserr.Validation("actions", "max 100 per request")
	}
	for _, a := range actions {
		if a.Action != "accept" && a.Action != "reject" {
			return nil, vdmserr.Validation("action", "must be accept or reject")
		}
	}
	if _, err := s.requireDocPermission(ctx, tenantID, userID, documentID, "edit"); err != nil {
		return nil, err
	}

	summary := &repository.ReviewSummary{}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Confirm each suggestion belongs to this document — we don't
		// want a single accept call mutating tags on a different doc.
		for _, a := range actions {
			cur, gerr := s.repos.TagSuggestions.GetByID(ctx, tx, tenantID, a.SuggestionID)
			if gerr != nil {
				return gerr
			}
			if cur.DocumentID != documentID {
				return vdmserr.Validation("suggestion_id",
					"belongs to a different document")
			}
		}

		acceptedNames := []string{}
		rejectedNames := []string{}
		for _, a := range actions {
			newStatus := "rejected"
			if a.Action == "accept" {
				newStatus = "accepted"
			}
			updated, uerr := s.repos.TagSuggestions.MarkReviewed(ctx, tx, tenantID, a.SuggestionID, userID, newStatus)
			if uerr != nil {
				return uerr
			}
			if a.Action == "accept" {
				if aerr := s.repos.TagSuggestions.AppendDocumentTag(ctx, tx, tenantID, documentID, updated.TagName); aerr != nil {
					return aerr
				}
				summary.Accepted = append(summary.Accepted, *updated)
				acceptedNames = append(acceptedNames, updated.TagName)
			} else {
				summary.Rejected = append(summary.Rejected, *updated)
				rejectedNames = append(rejectedNames, updated.TagName)
			}
		}

		evt, oerr := model.NewOutboxEvent(tenantID, "dms.autotag.reviewed.v1", "document", documentID,
			autotagReviewedPayload{
				DocumentID:    documentID.String(),
				TenantID:      tenantID.String(),
				ReviewedBy:    userID.String(),
				ReviewedAt:    time.Now().UTC().Format(time.RFC3339Nano),
				AcceptedTags:  acceptedNames,
				RejectedTags:  rejectedNames,
				ActionCount:   int32(len(actions)),
			})
		if oerr != nil {
			return oerr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return summary, nil
}

// ListPendingTagSuggestions is the admin queue across all documents.
// Caller must be tenant admin (handler enforces).
func (s *DocumentService) ListPendingTagSuggestions(ctx context.Context, opts repository.ListPendingOpts) ([]repository.TagSuggestion, int64, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, 0, err
	}
	var (
		rows  []repository.TagSuggestion
		total int64
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		rows, total, lerr = s.repos.TagSuggestions.ListPending(ctx, tx, tenantID, opts)
		return lerr
	})
	return rows, total, err
}

func (s *DocumentService) GetAutoTagConfig(ctx context.Context) (*repository.AutoTagConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.AutoTagConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, gerr := s.repos.TagSuggestions.GetConfig(ctx, tx, tenantID)
		if gerr != nil && !isNotFound(gerr) {
			return gerr
		}
		out = c
		return nil
	})
	return out, err
}

func (s *DocumentService) UpsertAutoTagConfig(ctx context.Context, patch repository.AutoTagConfigPatch) (*repository.AutoTagConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateAutoTagPatch(patch); err != nil {
		return nil, err
	}
	var out *repository.AutoTagConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, uerr := s.repos.TagSuggestions.UpsertConfig(ctx, tx, tenantID, patch)
		if uerr != nil {
			return uerr
		}
		out = c
		return nil
	})
	return out, err
}

func validateAutoTagPatch(p repository.AutoTagConfigPatch) error {
	if p.AutoApplyThreshold != nil && (*p.AutoApplyThreshold < 0 || *p.AutoApplyThreshold > 1) {
		return vdmserr.Validation("auto_apply_threshold", "must be in [0.0, 1.0]")
	}
	if p.SuggestThreshold != nil && (*p.SuggestThreshold < 0 || *p.SuggestThreshold > 1) {
		return vdmserr.Validation("suggest_threshold", "must be in [0.0, 1.0]")
	}
	if p.MaxTagsPerDocument != nil && (*p.MaxTagsPerDocument < 1 || *p.MaxTagsPerDocument > 100) {
		return vdmserr.Validation("max_tags_per_document", "must be in [1, 100]")
	}
	if p.SourceWeights != nil {
		var m map[string]float64
		if err := json.Unmarshal(*p.SourceWeights, &m); err != nil {
			return vdmserr.Validation("source_weights", "must be a JSON object")
		}
		for k, v := range m {
			if v < 0 || v > 1 {
				return vdmserr.Validation("source_weights", "weight for "+k+" must be in [0.0, 1.0]")
			}
		}
	}
	return nil
}

func isNotFound(err error) bool {
	return vdmserr.KindOf(err) == vdmserr.KindNotFound
}

type autotagReviewedPayload struct {
	DocumentID   string   `json:"document_id"`
	TenantID     string   `json:"tenant_id"`
	ReviewedBy   string   `json:"reviewed_by"`
	ReviewedAt   string   `json:"reviewed_at"`
	AcceptedTags []string `json:"accepted_tags"`
	RejectedTags []string `json:"rejected_tags"`
	ActionCount  int32    `json:"action_count"`
}
