// Package activities — SetDocumentLifecycle, the approval/review
// workflows' document state change (DEF-02).
//
// It used to be a bare `UPDATE documents SET lifecycle_state = $1`: no
// legal-hold check (a held document could be flipped to active or draft —
// the §lifecycle invariant is that legal_hold freezes every transition),
// no state-machine check (any state could be overwritten), and no event
// (search, audit and webhooks never heard about the change). It now
// follows the document service's own UpdateLifecycle
// (services/document/internal/service/documents.go) inside one tenant
// transaction: row lock, hold check, state-machine check, compare-and-set
// UPDATE, dms.document.state_changed.v1 into the outbox.
package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/temporal"

	"github.com/aieera/sedoc/pkg/database"
)

// Application error types SetDocumentLifecycle returns. All three are
// NON-retryable — a retry cannot lift a hold, legalise a transition or
// create the document — so Temporal fails the activity at once and the
// calling workflow branches on the type (workflows/lifecycle.go).
const (
	ErrTypeLegalHold                  = "LegalHold"
	ErrTypeInvalidLifecycleTransition = "InvalidLifecycleTransition"
	ErrTypeDocumentNotFound           = "DocumentNotFound"
)

// SubjectDocumentStateChanged is the event the document service emits for
// every lifecycle change (UpdateLifecycle, documents.go). A workflow-driven
// change emits the same event with the same payload so consumers cannot
// tell the two paths apart.
const SubjectDocumentStateChanged = "dms.document.state_changed.v1"

// workflowActor is recorded as state_changed.changed_by: no user drives
// the final state change of a workflow. Same deterministic system id the
// document service's retention sweep records its transitions under
// (systemUser, services/document/internal/service/retention.go).
const workflowActor = "00000000-0000-0000-0000-000000000001"

// stateChangedPayload mirrors the document service's
// model.DocumentStateChangedPayload (services/document/internal/model/
// events.go), which this module cannot import. Pinned to the real
// declaration by TestStateChangedPayloadMirror_MatchesDocumentModel.
type stateChangedPayload struct {
	DocumentID string `json:"document_id"`
	FromState  string `json:"from_state"`
	ToState    string `json:"to_state"`
	Action     string `json:"action"`
	Reason     string `json:"reason,omitempty"`
	ChangedBy  string `json:"changed_by"`
}

type lifecycleKey struct{ from, action string }

// lifecycleHop is one applied row of the state machine.
type lifecycleHop struct{ from, action, to string }

// documentLifecycleTransitions is the subset of the document service's
// state machine (lifecycleTransitions,
// services/document/internal/model/lifecycle.go:18-20) that the approval
// and review workflows drive. Pinned to the source by
// TestLifecycleTransitionMirror_MatchesDocumentStateMachine.
//
// Deliberately absent: retained --restore--> active (a records-management
// action, not an approval outcome) and every hold row (holds are placed
// and released by the compliance/document APIs, never by a workflow).
var documentLifecycleTransitions = map[lifecycleKey]string{
	{"draft", "submit_for_review"}: "in_review",
	{"in_review", "approve"}:       "active",
	{"in_review", "reject"}:        "draft",
}

// planLifecycle returns the state-machine hops that take a document from
// `from` to `target`, or ok=false when the workflows may not make that
// change. Targets are the two the workflows set: active (approved) and
// draft (rejected / recalled). Already in the target → no hops, ok.
//
// draft → active is two hops, submit_for_review then approve: the
// approval workflow is started on a draft document (StartInstance does
// not submit it for review), so the workflow itself is the review stage.
// Each hop is a row of the mirrored table and each is recorded.
func planLifecycle(from, target string) ([]lifecycleHop, bool) {
	var actions []string
	switch target {
	case "active":
		actions = []string{"approve"}
		if from == "draft" {
			actions = []string{"submit_for_review", "approve"}
		}
	case "draft":
		actions = []string{"reject"}
	default:
		return nil, false
	}
	if from == target {
		return nil, true
	}
	var hops []lifecycleHop
	state := from
	for _, a := range actions {
		to, ok := documentLifecycleTransitions[lifecycleKey{state, a}]
		if !ok {
			return nil, false
		}
		hops = append(hops, lifecycleHop{from: state, action: a, to: to})
		state = to
	}
	return hops, state == target
}

// SetDocumentLifecycle moves the document to `state` (active or draft) the
// way the document service's UpdateLifecycle would, in one tenant
// transaction:
//
//  1. row-lock the live document (a concurrent transition or hold binding
//     either committed before and is seen, or waits for this one);
//  2. refuse a held document — the in-band legal_hold state or any active
//     legal_hold_documents binding — with ErrTypeLegalHold;
//  3. refuse a change the state machine does not allow with
//     ErrTypeInvalidLifecycleTransition;
//  4. compare-and-set the state (WHERE lifecycle_state = the state the
//     decision was made from) and insert one dms.document.state_changed.v1
//     outbox row per hop.
//
// Already in the target state is a successful no-op, which also makes a
// retry after a lost completion idempotent. Signature unchanged so
// in-flight workflow histories still decode.
func (a *Activities) SetDocumentLifecycle(ctx context.Context, tenantID, documentID, state string) error {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	docUUID, err := uuid.Parse(documentID)
	if err != nil {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("document_id %q is not a uuid", documentID), ErrTypeDocumentNotFound, err)
	}

	err = database.WithTenantTx(ctx, a.Pool, tenantUUID, func(tx pgx.Tx) error {
		var current string
		var held bool
		err := tx.QueryRow(ctx, `
			SELECT d.lifecycle_state,
			       EXISTS (
			           SELECT 1
			             FROM legal_hold_documents lhd
			             JOIN legal_holds lh
			               ON lh.tenant_id = lhd.tenant_id AND lh.id = lhd.hold_id
			            WHERE lhd.tenant_id = d.tenant_id
			              AND lhd.document_id = d.id
			              AND lh.is_active
			       )
			  FROM documents d
			 WHERE d.tenant_id = $1 AND d.id = $2 AND d.deleted_at IS NULL
			   FOR UPDATE OF d`,
			tenantUUID, docUUID).Scan(&current, &held)
		if errors.Is(err, pgx.ErrNoRows) {
			return documentNotFound(documentID)
		}
		if err != nil {
			return fmt.Errorf("lock document: %w", err)
		}

		if held || current == "legal_hold" {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("document %s is under legal hold; lifecycle change to %s refused", documentID, state),
				ErrTypeLegalHold, nil)
		}

		hops, ok := planLifecycle(current, state)
		if !ok {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("invalid lifecycle transition for document %s: %s -> %s", documentID, current, state),
				ErrTypeInvalidLifecycleTransition, nil)
		}
		if len(hops) == 0 {
			return nil // already in the target state
		}

		tag, err := tx.Exec(ctx, `
			UPDATE documents
			   SET lifecycle_state = $4,
			       under_legal_hold = false,
			       updated_at = $5
			 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
			   AND lifecycle_state = $3`,
			tenantUUID, docUUID, current, state, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("update lifecycle: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return documentNotFound(documentID)
		}

		for _, h := range hops {
			payload, err := json.Marshal(stateChangedPayload{
				DocumentID: documentID,
				FromState:  h.from,
				ToState:    h.to,
				Action:     h.action,
				Reason:     "workflow outcome",
				ChangedBy:  workflowActor,
			})
			if err != nil {
				return fmt.Errorf("marshal state_changed: %w", err)
			}
			evt := database.NewOutboxEvent(tenantUUID, SubjectDocumentStateChanged, "document", docUUID, payload)
			if err := a.Outbox.Insert(ctx, tx, evt); err != nil {
				return err
			}
		}
		return nil
	})
	// Hand an application error back UNWRAPPED: the SDK's failure
	// converter type-switches on the top-level error, so one wrapped by
	// WithTx's rollback annotation would lose its type and non-retryable
	// flag and be retried as a generic failure.
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		return appErr
	}
	return err
}

func documentNotFound(documentID string) error {
	return temporal.NewNonRetryableApplicationError(
		fmt.Sprintf("document %s not found", documentID), ErrTypeDocumentNotFound, nil)
}
