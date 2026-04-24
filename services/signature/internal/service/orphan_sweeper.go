package service

// orphan_sweeper.go — T-D-4 fix. ProfileService.Delete is a split
// transaction (revoke tx1 → S3 delete → hard-delete tx2). A process
// kill between tx1 and the S3 delete leaves a row with revoked_at
// set AND image_ref still pointing at an S3 object nobody will ever
// reach for.
//
// OrphanSweep finds those rows (revoked > 24h ago, still carrying an
// image_ref), deletes the S3 object, nulls image_ref, and emits
// `dms.signature.profile.orphan_swept.v1` for the audit service.
//
// Invoked by the workflow service's SignatureProfileOrphanWorkflow
// daily at 03:00 UTC per tenant — ahead of the 09:00 acknowledgement
// sweep so dashboards reflect yesterday's cleanup.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
)

// orphanSweptSubject is the outbox subject the audit service binds
// on. Pinned — downstream audit queries and SIEM rules reference
// this exact string.
const orphanSweptSubject = "dms.signature.profile.orphan_swept.v1"

// OrphanSweepResult is the count of rows swept this invocation. Not
// cumulative — callers accumulate across runs themselves.
type OrphanSweepResult struct {
	Swept int
}

// OrphanSweep is the per-tenant entry point. Returns the number of
// rows actually cleaned (S3-deleted + image_ref nulled + audit-logged).
// S3-delete failures short-circuit the row — the row stays orphaned
// and the next schedule run will retry.
func (s *ProfileService) OrphanSweep(ctx context.Context, tenantID uuid.UUID) (OrphanSweepResult, error) {
	var out OrphanSweepResult
	cutoff := s.clock().Add(-24 * time.Hour)

	// Step 1: list orphans in a read-only tx.
	var orphans []profileOrphan
	if err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := s.repo.ListOrphans(ctx, tx, tenantID, cutoff)
		if err != nil {
			return err
		}
		orphans = make([]profileOrphan, 0, len(rows))
		for _, r := range rows {
			orphans = append(orphans, profileOrphan{
				ID:        r.ID,
				UserID:    r.UserID,
				ImageRef:  r.ImageRef,
				RevokedAt: r.RevokedAt,
			})
		}
		return nil
	}); err != nil {
		return out, fmt.Errorf("list orphans: %w", err)
	}

	// Step 2: for each orphan — S3 delete, then null image_ref + audit
	// outbox emission in one tx.
	for _, o := range orphans {
		if err := s.store.DeleteObject(ctx, ProfileBucket, o.ImageRef); err != nil {
			// Log-and-continue; next schedule run retries. Don't
			// abort the whole batch because one row is unreachable.
			s.log.Warn().
				Err(err).
				Str("tenant_id", tenantID.String()).
				Str("profile_id", o.ID.String()).
				Msg("orphan sweep: S3 delete failed; leaving row for next run")
			continue
		}
		if err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
			if err := s.repo.ClearImageRef(ctx, tx, tenantID, o.ID); err != nil {
				return err
			}
			payload, err := json.Marshal(map[string]any{
				"tenant_id":   tenantID.String(),
				"profile_id":  o.ID.String(),
				"user_id":     o.UserID.String(),
				"revoked_at":  o.RevokedAt.UTC().Format(time.RFC3339Nano),
				"swept_at":    s.clock().UTC().Format(time.RFC3339Nano),
				"image_ref":   o.ImageRef,
			})
			if err != nil {
				return fmt.Errorf("marshal orphan-swept payload: %w", err)
			}
			evt := database.NewOutboxEvent(tenantID, orphanSweptSubject, "signature", o.ID, payload)
			return s.outbox.Insert(ctx, tx, evt)
		}); err != nil {
			// Image is gone from S3 but DB cleanup failed. The row's
			// image_ref now points at a dead S3 object; next run will
			// re-sweep (S3 DeleteObject is idempotent) and re-emit.
			// Record the error but don't count this row as swept.
			s.log.Error().
				Err(err).
				Str("tenant_id", tenantID.String()).
				Str("profile_id", o.ID.String()).
				Msg("orphan sweep: DB cleanup failed after S3 delete")
			continue
		}
		out.Swept++
	}
	return out, nil
}

// profileOrphan is the copy-shape the sweep holds between its read
// and write passes; decoupled from repository.OrphanRow so a
// repository refactor doesn't leak into the sweep body.
type profileOrphan struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	ImageRef  string
	RevokedAt time.Time
}
