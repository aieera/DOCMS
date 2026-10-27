// Package activities — retention-specific activities for Wave 8.1.
//
// These land in their own file to keep activities.go from ballooning
// past the ~500-line threshold we logged in out-of-scope. All helpers
// here follow the same pattern as the base activities: pooled pgx
// exec, outbox emission via WithTenantTx, no direct NATS publish.
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

// ExpiredDocument is one row of the retention sweep query. Lifecycle
// and retention_until are returned so the workflow can make
// deterministic policy choices without further DB calls.
type ExpiredDocument struct {
	DocumentID     string    `json:"document_id"`
	LifecycleState string    `json:"lifecycle_state"`
	RetentionUntil time.Time `json:"retention_until"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// SweepExpiredRetentions returns documents whose retention clock has
// passed asOf and whose lifecycle is still in a state that the cron
// should touch. Documents in disposed / legal_hold are excluded —
// legal_hold is handled by the caller's explicit hold check to keep
// the audit trail distinct, disposed is terminal.
//
// Results are capped at `limit` to bound per-tick work; the spec's
// daily cadence plus typical retention tail shouldn't overflow 10k
// per run, but we pass the cap explicitly rather than leaving it
// implicit.
func (a *Activities) SweepExpiredRetentions(ctx context.Context, tenantID string, asOf time.Time, limit int) ([]ExpiredDocument, error) {
	if limit <= 0 {
		limit = 10000
	}
	var out []ExpiredDocument
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, lifecycle_state, retention_until, updated_at
			  FROM documents
			 WHERE tenant_id = $1
			   AND deleted_at IS NULL
			   AND retention_until IS NOT NULL
			   AND retention_until <= $2
			   AND lifecycle_state IN ('active', 'retained', 'archived')
			 ORDER BY retention_until ASC
			 LIMIT $3
		`, tenantID, asOf, limit)
		if err != nil {
			return fmt.Errorf("sweep: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d ExpiredDocument
			if err := rows.Scan(&d.DocumentID, &d.LifecycleState, &d.RetentionUntil, &d.UpdatedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// DocumentOnLegalHold reports whether a document is bound to any
// active legal hold. The cron must skip these — retention transitions
// on a held document would violate hold contracts.
func (a *Activities) DocumentOnLegalHold(ctx context.Context, tenantID, documentID string) (bool, error) {
	var exists bool
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1
			      FROM legal_hold_documents lhd
			      JOIN legal_holds lh
			        ON lh.tenant_id = lhd.tenant_id AND lh.id = lhd.hold_id
			     WHERE lhd.tenant_id = $1
			       AND lhd.document_id = $2
			       AND lh.is_active = true
			)`, tenantID, documentID).Scan(&exists)
	})
	return exists, err
}

// RetentionTransition moves a document to `newState` (archived or
// disposed) and emits the associated domain event in a single
// tenant-scoped transaction, so the state change and the event-bus
// notification can never diverge. This is the retention-cron variant of
// SetDocumentLifecycle and follows the same guarded pattern (I-3):
//
//  1. row-lock the live document (lockDocumentState);
//  2. THEN re-check the hold — any active legal_hold_documents binding or
//     the in-band legal_hold state — in a separate statement, so a hold
//     placed after the workflow's DocumentOnLegalHold check (or while this
//     waited for the lock) is seen: non-retryable ErrTypeLegalHold, which
//     the workflow records as dms.retention.held.v1;
//  3. walk the document state machine (planRetention): a transition it
//     does not allow is a non-retryable ErrTypeInvalidLifecycleTransition;
//  4. compare-and-set UPDATE on the state the decision was made from,
//     re-check the hold, insert the `subject` event.
//
// Already in the target state is a successful no-op (idempotent retry). A
// missing or soft-deleted document is a non-retryable ErrTypeDocumentNotFound.
func (a *Activities) RetentionTransition(ctx context.Context, tenantID, documentID, newState, subject string) error {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	docUUID, err := uuid.Parse(documentID)
	if err != nil {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("document_id %q is not a uuid", documentID), ErrTypeDocumentNotFound, err)
	}

	payload, _ := json.Marshal(map[string]any{
		"specversion": "1.0",
		"type":        subject,
		"source":      "/vaultdms/workflow/retention",
		"data": map[string]string{
			"tenant_id":   tenantID,
			"document_id": documentID,
			"new_state":   newState,
		},
	})
	evt := database.NewOutboxEvent(tenantUUID, subject, "document", docUUID, payload)

	txErr := database.WithTenantTx(ctx, a.Pool, tenantUUID, func(tx pgx.Tx) error {
		current, err := lockDocumentState(ctx, tx, tenantUUID, docUUID, documentID)
		if err != nil {
			return err
		}
		if holdErr := refuseIfHeld(ctx, tx, tenantUUID, docUUID, current, documentID, newState); holdErr != nil {
			return holdErr
		}
		hops, ok := planRetention(current, newState)
		if !ok {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("invalid retention transition for document %s: %s -> %s", documentID, current, newState),
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
			tenantUUID, docUUID, current, newState, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("update lifecycle: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return documentNotFound(documentID)
		}
		if holdErr := refuseIfHeld(ctx, tx, tenantUUID, docUUID, newState, documentID, newState); holdErr != nil {
			return holdErr
		}
		return a.Outbox.Insert(ctx, tx, evt)
	})
	// Unwrapped, as in SetDocumentLifecycle: the SDK's failure converter
	// only keeps the type and non-retryable flag of a top-level
	// ApplicationError.
	var appErr *temporal.ApplicationError
	if errors.As(txErr, &appErr) {
		return appErr
	}
	return txErr
}

// EmitRetentionEvent writes a domain event without any state change.
// Used for `dms.retention.held.v1` and `dms.retention.dispose_candidate.v1`
// signal emissions.
func (a *Activities) EmitRetentionEvent(ctx context.Context, tenantID, documentID, subject, reason string) error {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	docUUID := parseUUIDOrNew(documentID)

	payload, _ := json.Marshal(map[string]any{
		"specversion": "1.0",
		"type":        subject,
		"source":      "/vaultdms/workflow/retention",
		"data": map[string]string{
			"tenant_id":   tenantID,
			"document_id": documentID,
			"reason":      reason,
		},
	})
	evt := database.NewOutboxEvent(tenantUUID, subject, "document", docUUID, payload)
	return database.WithTenantTx(ctx, a.Pool, tenantUUID, func(tx pgx.Tx) error {
		return a.Outbox.Insert(ctx, tx, evt)
	})
}
