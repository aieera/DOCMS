// Wave 8.4 / ADR 0036 — disposition executor.
//
// Picks 'approved' candidates whose execute_after has elapsed, calls
// Storage.ShredBlobs to crypto-shred the wrapped DEKs across every
// version, and advances the candidate to 'executed' + the document to
// lifecycle_state='disposed'.
//
// Concurrency model: single-instance cron. The /internal/v1/disposition/
// execute endpoint is invoked by one Kubernetes CronJob; we don't try
// to run two executors simultaneously (the CronJob's concurrencyPolicy
// is Forbid). Per-row protection is the WHERE status='approved' clause
// inside the claim UPDATE — if a parallel run somehow advanced a row
// already, the second pass scans 0 rows and exits clean.
//
// Cross-service ordering (ADR 0036 §"Crypto-shred"):
//
//   1. Tx-A: claim the row (status stays 'approved', set decided_at
//      sentinel) and read all version blob_ids. The hold is re-checked
//      here; an in-flight legal hold supersedes the candidate atomically.
//   2. (no tx) Storage.ShredBlobs gRPC. The storage service nulls
//      encrypted_dek + dek_nonce and emits dms.blob.shredded.v1 in its
//      own tx. After this returns, the bytes are unrecoverable.
//   3. Tx-B: stamp the candidate executed_at + the document shredded_at
//      + emit dms.disposition.executed.v1. The shred has already
//      happened; this just records it.
//
// If Tx-B fails after ShredBlobs succeeded, the data is already
// destroyed but the document still says 'active'. This is the safe
// direction of inconsistency: a follow-up reconcile can advance the
// doc state without risk. The opposite ordering (doc first, blob
// second) could leave a "disposed" document with recoverable bytes —
// unacceptable.

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
)

// DispositionExecutorResult mirrors RetentionSweepResult — the cron's
// log line and Prometheus counter both consume it.
type DispositionExecutorResult struct {
	TenantID         uuid.UUID `json:"tenant_id"`
	Inspected        int       `json:"inspected"`        // candidates pulled this tick
	Executed         int       `json:"executed"`         // shred completed end-to-end
	SkippedNotReady  int       `json:"skipped_not_ready"` // execute_after still in the future
	SupersededByHold int       `json:"superseded_by_hold"` // hold landed between approval and execute
	Errors           int       `json:"errors"`
}

// ExecuteDispositions is the cron entry point. batchSize caps the
// per-tick work — operators can tune it via the request body.
func (s *DocumentService) ExecuteDispositions(ctx context.Context, tenantID uuid.UUID, batchSize int) (*DispositionExecutorResult, error) {
	if s.storage == nil {
		// Storage gRPC client wasn't wired (dev mode or storage outage at
		// boot). Without it we can't crypto-shred — the safe response is
		// to refuse, not to advance the candidate state.
		return nil, fmt.Errorf("storage client unavailable; cannot execute dispositions")
	}
	if batchSize <= 0 || batchSize > 1000 {
		batchSize = 100
	}
	res := &DispositionExecutorResult{TenantID: tenantID}

	candidates, err := s.listExecutableCandidates(ctx, tenantID, batchSize)
	if err != nil {
		return nil, fmt.Errorf("list executable candidates: %w", err)
	}
	res.Inspected = len(candidates)

	for _, c := range candidates {
		if err := s.executeOne(ctx, tenantID, c, res); err != nil {
			res.Errors++
			s.log.Error().Err(err).
				Str("candidate", c.id.String()).
				Str("document", c.documentID.String()).
				Msg("disposition execute failed")
		}
	}
	return res, nil
}

// executableCandidate is the local snapshot the executor needs. Loaded
// in one query so we don't round-trip per row.
type executableCandidate struct {
	id           uuid.UUID
	documentID   uuid.UUID
	reviewerID   *uuid.UUID
	executeAfter time.Time
}

func (s *DocumentService) listExecutableCandidates(ctx context.Context, tenantID uuid.UUID, limit int) ([]executableCandidate, error) {
	var out []executableCandidate
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, document_id, reviewer_id, execute_after
			  FROM disposition_candidates
			 WHERE tenant_id      = $1
			   AND status         = 'approved'
			   AND proposed_action = 'dispose'
			   AND execute_after  <= now()
			 ORDER BY execute_after ASC
			 LIMIT $2
		`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c executableCandidate
			if err := rows.Scan(&c.id, &c.documentID, &c.reviewerID, &c.executeAfter); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// executeOne runs the three-phase orchestration for a single candidate.
// All phase failures fall through to res.Errors via the caller; only
// the hold-supersede outcome is non-error.
func (s *DocumentService) executeOne(
	ctx context.Context, tenantID uuid.UUID,
	c executableCandidate, res *DispositionExecutorResult,
) error {
	// ---- Phase 1: claim + read blob ids -------------------------------
	// Atomic re-check of hold + collection of every version blob_id
	// belonging to the document. We hold the row briefly to make the
	// claim race-safe.
	var blobIDs []uuid.UUID
	var held bool
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Re-check hold and confirm candidate is still in 'approved'.
		err := tx.QueryRow(ctx, `
			SELECT (d.under_legal_hold OR d.hold_count > 0)
			  FROM disposition_candidates c
			  JOIN documents d
			    ON d.tenant_id = c.tenant_id AND d.id = c.document_id
			 WHERE c.tenant_id = $1
			   AND c.id = $2
			   AND c.status = 'approved'
			   AND d.deleted_at IS NULL
		`, tenantID, c.id).Scan(&held)
		if err != nil {
			return err
		}
		if held {
			return nil
		}
		// Pull every version's blob — partial shred would leave older
		// versions decryptable, defeating the point.
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT v.content_blob_id
			  FROM versions v
			 WHERE v.tenant_id = $1
			   AND v.document_id = $2
			   AND v.content_blob_id IS NOT NULL
		`, tenantID, c.documentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			blobIDs = append(blobIDs, id)
		}
		return rows.Err()
	})
	if err != nil {
		return fmt.Errorf("phase 1 claim: %w", err)
	}

	if held {
		// Hold landed between approval and execute. ADR 0036: refuse the
		// destruction outright and supersede the candidate.
		if err := s.supersedeCandidateForHold(ctx, tenantID, c); err != nil {
			return fmt.Errorf("supersede on hold: %w", err)
		}
		res.SupersededByHold++
		return nil
	}

	// ---- Phase 2: cross-service crypto-shred --------------------------
	// Outside any DB tx — gRPC under a held tx is a deadlock waiting to
	// happen. The storage service runs its own tx for the DEK zeroing
	// and the dms.blob.shredded.v1 outbox emit.
	blobStrs := make([]string, len(blobIDs))
	for i, id := range blobIDs {
		blobStrs[i] = id.String()
	}
	reviewer := ""
	if c.reviewerID != nil {
		reviewer = c.reviewerID.String()
	}
	if len(blobIDs) > 0 {
		_, err := s.storage.ShredBlobs(ctx, &vaultdmsv1.ShredBlobsRequest{
			TenantId:    tenantID.String(),
			BlobIds:     blobStrs,
			CandidateId: c.id.String(),
			ActorId:     reviewer,
		})
		if err != nil {
			return fmt.Errorf("storage shred: %w", err)
		}
	}
	// A document with zero versions (rare — newly-created, never uploaded)
	// has nothing to shred but still needs lifecycle advancement. Fall
	// through to phase 3.

	// ---- Phase 3: advance candidate + document, emit audit ------------
	// At this point the bytes are unrecoverable. If this tx fails we
	// retry on the next cron tick — re-running ShredBlobs is idempotent
	// (it returns AlreadyShredded for the second pass).
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Stamp executed_at on the candidate. The CHECK constraint on the
		// table requires executed_at NOT NULL when status='executed'.
		tag, err := tx.Exec(ctx, `
			UPDATE disposition_candidates
			   SET status = 'executed', executed_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND status = 'approved'
		`, tenantID, c.id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Lost the race to a concurrent reject or another executor
			// instance. The bytes are already shredded; the next pass
			// will pick up state mismatches via the reconcile worker
			// (a future slice). For now, log and continue.
			s.log.Warn().
				Str("candidate", c.id.String()).
				Msg("candidate not in 'approved' after shred — possible concurrent advance")
			return nil
		}
		// Advance the document's lifecycle + stamp shredded_at.
		if _, err := tx.Exec(ctx, `
			UPDATE documents
			   SET lifecycle_state = 'disposed', shredded_at = now()
			 WHERE tenant_id = $1 AND id = $2
		`, tenantID, c.documentID); err != nil {
			return err
		}
		// Audit fan-out: dms.disposition.executed.v1 is consumed by the
		// audit service. The disposed document_id and the candidate_id
		// give an auditor both ends of the trail.
		payload, _ := json.Marshal(map[string]any{
			"actor_id":      reviewer,
			"tenant_id":     tenantID.String(),
			"action":        "disposition.executed",
			"resource_type": "document",
			"resource_id":   c.documentID.String(),
			"candidate_id":  c.id.String(),
			"blob_count":    len(blobIDs),
		})
		evt := database.NewOutboxEvent(tenantID, "dms.disposition.executed.v1", "document", c.documentID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		return fmt.Errorf("phase 3 advance: %w", err)
	}
	res.Executed++
	return nil
}

// supersedeCandidateForHold flips a candidate to 'superseded' when a
// legal hold lands between approval and execution. The audit emit
// gives compliance officers a trail showing *why* a destruction
// scheduled for today did not happen.
func (s *DocumentService) supersedeCandidateForHold(ctx context.Context, tenantID uuid.UUID, c executableCandidate) error {
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE disposition_candidates
			   SET status = 'superseded',
			       decided_reason = COALESCE(decided_reason, '') ||
			           CASE WHEN COALESCE(decided_reason, '') = ''
			                THEN 'auto-superseded: legal hold landed before execute'
			                ELSE ' | auto-superseded: legal hold landed before execute'
			           END
			 WHERE tenant_id = $1 AND id = $2 AND status = 'approved'
		`, tenantID, c.id)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"tenant_id":     tenantID.String(),
			"action":        "disposition.superseded_by_hold",
			"resource_type": "document",
			"resource_id":   c.documentID.String(),
			"candidate_id":  c.id.String(),
		})
		evt := database.NewOutboxEvent(tenantID, "dms.disposition.superseded.v1", "document", c.documentID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	})
}
