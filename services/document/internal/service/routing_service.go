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

// ListRouteSuggestions returns folder candidates the intelligence
// pipeline produced for a document. View permission required.
func (s *DocumentService) ListRouteSuggestions(ctx context.Context, documentID uuid.UUID) ([]repository.RouteSuggestion, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireDocPermission(ctx, tenantID, userID, documentID, "view"); err != nil {
		return nil, err
	}
	var out []repository.RouteSuggestion
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		out, lerr = s.repos.Routing.ListSuggestionsForDocument(ctx, tx, tenantID, documentID)
		return lerr
	})
	return out, err
}

// AcceptRouteSuggestion moves the document to the suggested folder
// (reusing MoveDocument so all permission, lifecycle, and audit gates
// fire), marks the suggestion accepted, and records a filing_history
// row with was_suggestion=true.
//
// Two-tx flow: the move is its own transaction (it has to be — caller
// expects MoveDocument's own gates and outbox event). The
// accept-mark + history + audit-event run in a second tx. If the
// second tx fails the document has moved but the suggestion stays
// pending; re-clicking accept finds the doc already in the target
// folder and resolves cleanly.
func (s *DocumentService) AcceptRouteSuggestion(ctx context.Context, documentID, suggestionID uuid.UUID) (*repository.RouteSuggestion, *model.Document, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, nil, err
	}

	var (
		sug         *repository.RouteSuggestion
		categoryKey string
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		sug, lerr = s.repos.Routing.GetSuggestion(ctx, tx, tenantID, suggestionID)
		if lerr != nil {
			return lerr
		}
		if sug.DocumentID != documentID {
			return vdmserr.Validation("suggestion_id", "belongs to a different document")
		}
		if sug.Status != "pending" {
			return vdmserr.Conflict("suggestion is not pending")
		}
		// Look up classification for the filing-history row's
		// category_key. Best-effort: if absent we still proceed.
		_ = tx.QueryRow(ctx, `
			SELECT category_key FROM document_classifications
			 WHERE tenant_id = $1 AND document_id = $2
			 LIMIT 1`, tenantID, documentID,
		).Scan(&categoryKey)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	doc, err := s.MoveDocument(ctx, &MoveDocumentInput{
		DocumentID:        documentID,
		TargetFolderID:    sug.SuggestedFolderID,
		TargetWorkspaceID: sug.SuggestedWorkspaceID,
		UpdatedBy:         userID,
	})
	if err != nil {
		return nil, nil, err
	}

	var updated *repository.RouteSuggestion
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		u, mErr := s.repos.Routing.MarkSuggestionStatus(ctx, tx, tenantID, suggestionID, userID, "accepted")
		if mErr != nil {
			return mErr
		}
		updated = u
		if hErr := s.repos.Routing.InsertFilingHistory(ctx, tx, repository.FilingHistoryRow{
			TenantID:         tenantID,
			DocumentID:       documentID,
			CategoryKey:      categoryKey,
			FiledFolderID:    sug.SuggestedFolderID,
			FiledWorkspaceID: sug.SuggestedWorkspaceID,
			WasSuggestion:    true,
			SuggestionRank:   nil,
			FiledBy:          userID,
		}); hErr != nil {
			return hErr
		}
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.routing.accepted.v1", "document", documentID,
			routingAcceptedPayload{
				DocumentID:        documentID.String(),
				TenantID:          tenantID.String(),
				SuggestionID:      suggestionID.String(),
				FolderID:          sug.SuggestedFolderID.String(),
				MatchSource:       sug.MatchSource,
				Confidence:        float64(sug.Confidence),
				CategoryKey:       categoryKey,
				AcceptedBy:        userID.String(),
				AcceptedAt:        time.Now().UTC().Format(time.RFC3339Nano),
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	return updated, doc, err
}

func (s *DocumentService) DismissRouteSuggestion(ctx context.Context, documentID, suggestionID uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		sug, gErr := s.repos.Routing.GetSuggestion(ctx, tx, tenantID, suggestionID)
		if gErr != nil {
			return gErr
		}
		if sug.DocumentID != documentID {
			return vdmserr.Validation("suggestion_id", "belongs to a different document")
		}
		if _, perr := s.requireDocPermission(ctx, tenantID, userID, documentID, "view"); perr != nil {
			return perr
		}
		if _, mErr := s.repos.Routing.MarkSuggestionStatus(ctx, tx, tenantID, suggestionID, userID, "dismissed"); mErr != nil {
			return mErr
		}
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.routing.dismissed.v1", "document", documentID,
			routingDismissedPayload{
				DocumentID:   documentID.String(),
				TenantID:     tenantID.String(),
				SuggestionID: suggestionID.String(),
				FolderID:     sug.SuggestedFolderID.String(),
				MatchSource:  sug.MatchSource,
				DismissedBy:  userID.String(),
				DismissedAt:  time.Now().UTC().Format(time.RFC3339Nano),
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// ---- routing rules CRUD --------------------------------------------------

func (s *DocumentService) ListRoutingRules(ctx context.Context) ([]repository.RoutingRule, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []repository.RoutingRule
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		out, lerr = s.repos.Routing.ListRules(ctx, tx, tenantID)
		return lerr
	})
	return out, err
}

func (s *DocumentService) CreateRoutingRule(ctx context.Context, in repository.RoutingRuleInput) (*repository.RoutingRule, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateRoutingRuleInput(in); err != nil {
		return nil, err
	}
	var out *repository.RoutingRule
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Confirm the target folder belongs to this tenant — composite
		// FK would catch this anyway, but a clean error beats a 500.
		if _, gErr := s.repos.Folders.GetByID(ctx, tx, tenantID, in.TargetFolderID); gErr != nil {
			return gErr
		}
		r, iErr := s.repos.Routing.InsertRule(ctx, tx, tenantID, userID, in)
		if iErr != nil {
			return iErr
		}
		out = r
		return nil
	})
	return out, err
}

func (s *DocumentService) UpdateRoutingRule(ctx context.Context, id uuid.UUID, p repository.RoutingRulePatch) (*repository.RoutingRule, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.RoutingRule
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		r, uErr := s.repos.Routing.UpdateRule(ctx, tx, tenantID, id, p)
		if uErr != nil {
			return uErr
		}
		out = r
		return nil
	})
	return out, err
}

func (s *DocumentService) DeleteRoutingRule(ctx context.Context, id uuid.UUID) error {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.Routing.DeleteRule(ctx, tx, tenantID, id)
	})
}

// ---- config + analytics -------------------------------------------------

func (s *DocumentService) GetSmartRoutingConfig(ctx context.Context) (*repository.SmartRoutingConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.SmartRoutingConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, gErr := s.repos.Routing.GetConfig(ctx, tx, tenantID)
		if gErr != nil && !isNotFound(gErr) {
			return gErr
		}
		out = c
		return nil
	})
	return out, err
}

func (s *DocumentService) UpsertSmartRoutingConfig(ctx context.Context, p repository.SmartRoutingConfigPatch) (*repository.SmartRoutingConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateSmartRoutingPatch(p); err != nil {
		return nil, err
	}
	var out *repository.SmartRoutingConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, uErr := s.repos.Routing.UpsertConfig(ctx, tx, tenantID, p)
		if uErr != nil {
			return uErr
		}
		out = c
		return nil
	})
	return out, err
}

func (s *DocumentService) FilingAnalytics(ctx context.Context) (*repository.FilingAnalytics, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.FilingAnalytics
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		a, aErr := s.repos.Routing.Analytics(ctx, tx, tenantID)
		if aErr != nil {
			return aErr
		}
		out = a
		return nil
	})
	return out, err
}

// ---- validators ---------------------------------------------------------

func validateRoutingRuleInput(in repository.RoutingRuleInput) error {
	if in.Name == "" {
		return vdmserr.Validation("name", "required")
	}
	if len(in.Name) > 200 {
		return vdmserr.Validation("name", "max 200 chars")
	}
	if in.CategoryKey == "" {
		return vdmserr.Validation("category_key", "required")
	}
	if in.TargetFolderID == uuid.Nil {
		return vdmserr.Validation("target_folder_id", "required")
	}
	if in.Priority < -1000 || in.Priority > 1000 {
		return vdmserr.Validation("priority", "must be in [-1000, 1000]")
	}
	return nil
}

func validateSmartRoutingPatch(p repository.SmartRoutingConfigPatch) error {
	if p.AutoMoveThreshold != nil && (*p.AutoMoveThreshold < 0 || *p.AutoMoveThreshold > 1) {
		return vdmserr.Validation("auto_move_threshold", "must be in [0.0, 1.0]")
	}
	if p.SuggestThreshold != nil && (*p.SuggestThreshold < 0 || *p.SuggestThreshold > 1) {
		return vdmserr.Validation("suggest_threshold", "must be in [0.0, 1.0]")
	}
	if p.MaxSuggestions != nil && (*p.MaxSuggestions < 1 || *p.MaxSuggestions > 20) {
		return vdmserr.Validation("max_suggestions", "must be in [1, 20]")
	}
	return nil
}

// ---- outbox payloads ----------------------------------------------------

type routingAcceptedPayload struct {
	DocumentID   string  `json:"document_id"`
	TenantID     string  `json:"tenant_id"`
	SuggestionID string  `json:"suggestion_id"`
	FolderID     string  `json:"folder_id"`
	MatchSource  string  `json:"match_source"`
	Confidence   float64 `json:"confidence"`
	CategoryKey  string  `json:"category_key,omitempty"`
	AcceptedBy   string  `json:"accepted_by"`
	AcceptedAt   string  `json:"accepted_at"`
}

type routingDismissedPayload struct {
	DocumentID   string `json:"document_id"`
	TenantID     string `json:"tenant_id"`
	SuggestionID string `json:"suggestion_id"`
	FolderID     string `json:"folder_id"`
	MatchSource  string `json:"match_source"`
	DismissedBy  string `json:"dismissed_by"`
	DismissedAt  string `json:"dismissed_at"`
}

