package ingestroute

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// Activities holds the dependencies the IngestAndRoute activities need. Methods
// are registered by name via worker.RegisterActivity(acts); the workflow calls
// them by those names ("ExtractKey", "MatchExisting", "Decide", "Commit",
// "NeedsReview").
type Activities struct {
	Svc *service.DocumentService
	Log zerolog.Logger
}

// NewActivities constructs the activity set.
func NewActivities(svc *service.DocumentService, log zerolog.Logger) *Activities {
	return &Activities{Svc: svc, Log: log}
}

// ---- result types (JSON-serializable across the Temporal boundary) ---------

type ExtractKeyResult struct {
	ExternalKey string  `json:"external_key"`
	Confidence  float64 `json:"confidence"`
	Terminal    bool    `json:"terminal"`
}

type DecideResult struct {
	Action string `json:"action"` // commit | review
	Reason string `json:"reason"` // review reason when Action=review
}

type CommitResult struct {
	DocumentID  string `json:"document_id"`
	VersionID   string `json:"version_id"`
	NewDocument bool   `json:"new_document"`
}

// ---- activities ------------------------------------------------------------

// ExtractKey reads the worker-written external key + confidence off the staging
// row, and reports whether the item is already terminal (idempotency short-circuit).
func (a *Activities) ExtractKey(ctx context.Context, tenantID, itemID string) (ExtractKeyResult, error) {
	tid, iid, err := parseIDs(tenantID, itemID)
	if err != nil {
		return ExtractKeyResult{}, err
	}
	it, err := a.Svc.RouteLoad(ctx, tid, iid)
	if err != nil {
		return ExtractKeyResult{}, err
	}
	return ExtractKeyResult{
		ExternalKey: it.ExtractedExternalKey,
		Confidence:  it.Confidence,
		Terminal:    it.Status.IsTerminal(),
	}, nil
}

// MatchExisting returns the id of a live document carrying the extracted key, or
// "" when there's no match.
func (a *Activities) MatchExisting(ctx context.Context, tenantID, itemID string) (string, error) {
	tid, iid, err := parseIDs(tenantID, itemID)
	if err != nil {
		return "", err
	}
	m, err := a.Svc.RouteMatch(ctx, tid, iid)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", nil
	}
	return m.String(), nil
}

// Decide gates the read on the configured confidence threshold. Kept as an
// activity so the threshold read stays out of the deterministic workflow path.
func (a *Activities) Decide(ctx context.Context, externalKey string, confidence float64, matchDocID string) (DecideResult, error) {
	if strings.TrimSpace(externalKey) == "" {
		return DecideResult{Action: "review", Reason: string(model.ReasonNoExternalKey)}, nil
	}
	if confidence < a.Svc.MatchThreshold() {
		return DecideResult{Action: "review", Reason: string(model.ReasonBelowThreshold)}, nil
	}
	return DecideResult{Action: "commit"}, nil
}

// Commit routes the staged item to a document/version via the Workstream-1 upsert.
func (a *Activities) Commit(ctx context.Context, tenantID, itemID string) (CommitResult, error) {
	tid, iid, err := parseIDs(tenantID, itemID)
	if err != nil {
		return CommitResult{}, err
	}
	res, err := a.Svc.RouteCommit(ctx, tid, iid)
	if err != nil {
		return CommitResult{}, err
	}
	return CommitResult{
		DocumentID:  res.DocumentID.String(),
		VersionID:   res.VersionID.String(),
		NewDocument: res.NewDocument,
	}, nil
}

// NeedsReview parks the staged item in the review queue (no version written).
func (a *Activities) NeedsReview(ctx context.Context, tenantID, itemID, candidateDocID, reason string) (string, error) {
	tid, iid, err := parseIDs(tenantID, itemID)
	if err != nil {
		return "", err
	}
	var cand *uuid.UUID
	if candidateDocID != "" {
		if c, perr := uuid.Parse(candidateDocID); perr == nil && c != uuid.Nil {
			cand = &c
		}
	}
	r := model.ReviewReason(reason)
	if r == "" {
		r = model.ReasonBelowThreshold
	}
	ri, err := a.Svc.RouteNeedsReview(ctx, tid, iid, cand, r)
	if err != nil {
		return "", err
	}
	if ri == nil {
		return "", nil
	}
	return ri.ID.String(), nil
}

func parseIDs(tenantID, itemID string) (uuid.UUID, uuid.UUID, error) {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("tenant_id: %w", err)
	}
	iid, err := uuid.Parse(itemID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("item_id: %w", err)
	}
	return tid, iid, nil
}
