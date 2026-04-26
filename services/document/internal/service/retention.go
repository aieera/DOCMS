package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/pkg/database"
	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// systemUser is the deterministic synthetic user retention runs
// as. UpdateLifecycle's permission check is satisfied via policy
// rego rule "retention_driven" (added in the same slice).
var systemUser = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// §9.4 / G5 — retention sweep.
//
// Finds documents past their retention_until AND matching an active
// policy, applies the policy's then_action (archive | dispose), and
// emits a lifecycle-change event so the search index + audit log
// stay coherent.
//
// Execution model:
//   - Invoked from POST /internal/v1/retention/sweep by the
//     vaultdms-retention CronJob. Not a user-facing endpoint.
//   - Legal holds block every action here — the hold check already
//     runs inside UpdateLifecycle via IsLegalHoldBlocked +
//     AnyActiveHoldFor. A policy can queue a dispose but a hold
//     wins; the sweep logs the skip and moves on.
//   - Batch-capped at `batchSize` per policy per run so a million-
//     doc tenant doesn't deadlock a tx. The next run picks up the
//     remainder.

// RetentionSweepResult is returned by SweepRetention for metrics
// and the runbook log line.
//
// ADR 0036 split: archive policies still apply directly (Archived
// counter); dispose policies *enqueue* into disposition_candidates for
// human review (Queued counter). The legacy `Disposed` counter is gone
// — disposition no longer happens at sweep time. The disposition
// executor cron (see executor.go) advances Queued → Executed after
// approval + 24h soak.
type RetentionSweepResult struct {
	TenantID        uuid.UUID `json:"tenant_id"`
	PoliciesApplied int       `json:"policies_applied"`
	Archived        int       `json:"archived"`
	Queued          int       `json:"queued"` // dispose candidates enqueued for review
	SkippedByHold   int       `json:"skipped_by_hold"`
	AlreadyQueued   int       `json:"already_queued"` // ON CONFLICT DO NOTHING — doc already has an open candidate
	Errors          int       `json:"errors"`
}

// SweepRetention applies every active retention policy for `tenantID`.
// `batchSize` caps how many docs are acted on per policy per call;
// 500 is a reasonable default (1 tx per doc, ~150ms each at DB p99).
func (s *DocumentService) SweepRetention(ctx context.Context, tenantID uuid.UUID, batchSize int) (*RetentionSweepResult, error) {
	if batchSize <= 0 || batchSize > 5000 {
		batchSize = 500
	}
	result := &RetentionSweepResult{TenantID: tenantID}

	policies, err := s.listActiveRetentionPolicies(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	for _, p := range policies {
		ids, err := s.findDocsOverRetentionBudget(ctx, tenantID, p, batchSize)
		if err != nil {
			result.Errors++
			s.log.Error().Err(err).Str("policy", p.ID.String()).Msg("retention: find docs")
			continue
		}
		if len(ids) == 0 {
			continue
		}
		result.PoliciesApplied++

		// ADR 0036: archive still applies directly (cheap, reversible,
		// no destruction). Dispose enqueues into disposition_candidates
		// for human approval — the executor cron actually shreds.
		if p.ThenAction == "dispose" {
			for _, id := range ids {
				outcome, err := s.enqueueDispositionCandidate(ctx, tenantID, p.ID, id)
				if err != nil {
					if isLegalHold(err) {
						result.SkippedByHold++
						continue
					}
					result.Errors++
					s.log.Error().Err(err).Str("doc", id.String()).Msg("retention: enqueue dispose")
					continue
				}
				switch outcome {
				case enqueueOutcomeQueued:
					result.Queued++
				case enqueueOutcomeAlreadyQueued:
					result.AlreadyQueued++
				case enqueueOutcomeHeld:
					result.SkippedByHold++
				}
			}
			continue
		}

		// Archive path — kept direct (cheap, reversible state change).
		// Slice 8: after successful state transition, also move every
		// version's blob to STANDARD_IA on S3 so the archive actually
		// changes storage cost (was state-only before).
		for _, id := range ids {
			_, err := s.UpdateLifecycle(
				withSystemUser(ctx, tenantID),
				&UpdateLifecycleInput{
					DocumentID: id,
					Action:     model.ActionArchive,
					Reason:     "retention policy " + p.Name,
				},
			)
			if err != nil {
				if isLegalHold(err) {
					result.SkippedByHold++
					continue
				}
				result.Errors++
				s.log.Error().Err(err).Str("doc", id.String()).Msg("retention: archive")
				continue
			}
			result.Archived++
			// Best-effort tier transition. A failure here doesn't roll
			// back the lifecycle change — the doc state advances even if
			// the cold-storage move can't happen right now (network
			// blip, S3 outage). Next sweep retries via the executor's
			// reconciliation pass.
			if s.storage != nil {
				if err := s.transitionDocumentToIA(ctx, tenantID, id, p.ID); err != nil {
					s.log.Warn().Err(err).
						Str("doc", id.String()).
						Str("policy", p.ID.String()).
						Msg("archive tier transition failed; document remains in STANDARD")
				}
			}
		}
	}
	// Slice 9: refresh the SLI gauges at the end of the sweep. A scrape
	// failure here is non-fatal — the sweep result is the authoritative
	// audit signal, prom is best-effort observability.
	if cov, queue, err := s.computeRetentionMetrics(ctx, tenantID); err != nil {
		s.log.Warn().Err(err).Str("tenant", tenantID.String()).Msg("retention metrics refresh failed")
	} else {
		PublishRetentionMetrics(tenantID.String(), cov, queue)
	}
	return result, nil
}

// computeRetentionMetrics returns the coverage ratio and the
// disposition queue depth keyed by status. Run inside the sweep so
// the gauge update is push-driven; pull-driven /metrics handlers
// can't safely run a JOIN over millions of documents on every scrape.
func (s *DocumentService) computeRetentionMetrics(ctx context.Context, tenantID uuid.UUID) (float64, map[string]int, error) {
	var coverage float64
	queue := map[string]int{}
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Coverage: a doc is "covered" if it matches at least one
		// active retention policy. Today the sweeper only matches on
		// retain_days (no document_class / tag / workspace filters yet),
		// so the existence of any active policy = full coverage. Once
		// the filter columns activate (see findDocsOverRetentionBudget
		// follow-up), this query gets richer; for now the simple
		// "any active policy = covered" is honest about what the
		// sweeper actually does.
		var totalActive, hasPolicy int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM documents
			 WHERE tenant_id = $1
			   AND deleted_at IS NULL
			   AND lifecycle_state IN ('active', 'retained')
		`, tenantID).Scan(&totalActive); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM retention_policies
			 WHERE tenant_id = $1 AND is_active = true
		`, tenantID).Scan(&hasPolicy); err != nil {
			return err
		}
		if totalActive == 0 {
			coverage = 1.0 // vacuously covered; avoid div-by-zero alarms
		} else if hasPolicy == 0 {
			coverage = 0.0
		} else {
			coverage = 1.0
		}

		// Queue depth by status — informs the disposition dashboard.
		rows, err := tx.Query(ctx, `
			SELECT status, COUNT(*)
			  FROM disposition_candidates
			 WHERE tenant_id = $1
			 GROUP BY status
		`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var status string
			var count int
			if err := rows.Scan(&status, &count); err != nil {
				return err
			}
			queue[status] = count
		}
		// Always publish the canonical statuses even if zero so the
		// gauge has a stable cardinality and dashboards never see
		// "no data" for a healthy tenant.
		for _, st := range []string{"queued", "approved", "rejected", "executed", "superseded"} {
			if _, ok := queue[st]; !ok {
				queue[st] = 0
			}
		}
		return rows.Err()
	})
	return coverage, queue, err
}

// enqueueOutcome reports what enqueueDispositionCandidate did. Held
// and AlreadyQueued are not errors — they're expected outcomes the
// sweeper counts separately for the runbook log.
type enqueueOutcome int

const (
	enqueueOutcomeQueued        enqueueOutcome = iota // new candidate row inserted
	enqueueOutcomeAlreadyQueued                       // doc already has a queued/approved candidate
	enqueueOutcomeHeld                                // doc is on legal hold; nothing inserted
)

// enqueueDispositionCandidate inserts a row in disposition_candidates
// for the document, with proposed_action='dispose'. Held documents are
// skipped explicitly (not via the UpdateLifecycle path which checks
// holds itself). The unique partial index on (tenant_id, document_id)
// WHERE status IN ('queued', 'approved') makes the "one open candidate
// per doc" guarantee a DB invariant — concurrent sweeps over the same
// row each get a clean answer (one wins, others get AlreadyQueued).
//
// Notification (slice 6, ADR 0036): if this insert is the empty→non-
// empty transition for the tenant's review queue, fan out a
// dms.notify.disposition.review.v1 to compliance_officer + owner role
// members. Two concurrent inserts that both observe the queue empty
// would both notify (rare race; ADR accepts the duplicate over
// serialising the inserts).
func (s *DocumentService) enqueueDispositionCandidate(ctx context.Context, tenantID, policyID, docID uuid.UUID) (enqueueOutcome, error) {
	var outcome enqueueOutcome
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Hold check: the dispose path doesn't go through UpdateLifecycle,
		// so we must explicitly verify the doc isn't held. Both fast-path
		// flag and counter — either truthy = held (per Wave 17 §9.3).
		var held bool
		if err := tx.QueryRow(ctx, `
			SELECT (under_legal_hold OR hold_count > 0)
			  FROM documents WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
		`, tenantID, docID).Scan(&held); err != nil {
			return err
		}
		if held {
			outcome = enqueueOutcomeHeld
			return nil
		}

		// Snapshot the current version's blob id at proposal time. If a
		// new version is uploaded between propose and execute, the
		// candidate is auto-superseded by a future sweep so we never
		// crypto-shred a version the user wrote *after* the policy
		// decided this doc was disposable. NULL is allowed (the doc
		// may have no current_version yet — rare, but possible).
		var blobID *uuid.UUID
		if err := tx.QueryRow(ctx, `
			SELECT v.content_blob_id
			  FROM documents d
			  LEFT JOIN versions v
			    ON v.tenant_id = d.tenant_id
			   AND v.id = d.current_version_id
			 WHERE d.tenant_id = $1 AND d.id = $2
		`, tenantID, docID).Scan(&blobID); err != nil {
			return err
		}

		// Determine BEFORE the insert whether the review queue is empty —
		// after the insert, this tenant always has at least one open
		// candidate. The notify trigger is the empty→non-empty transition.
		var queueWasEmpty bool
		if err := tx.QueryRow(ctx, `
			SELECT NOT EXISTS (
				SELECT 1 FROM disposition_candidates
				 WHERE tenant_id = $1 AND status IN ('queued', 'approved')
			)
		`, tenantID).Scan(&queueWasEmpty); err != nil {
			return err
		}

		// ON CONFLICT DO NOTHING relies on the partial unique index
		// idx_disposition_candidates_active. RETURNING reports whether
		// a row was actually inserted vs ignored.
		var insertedID uuid.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO disposition_candidates (
				tenant_id, document_id, policy_id, proposed_action, proposed_blob_id
			) VALUES ($1, $2, $3, 'dispose', $4)
			ON CONFLICT (tenant_id, document_id) WHERE status IN ('queued', 'approved')
			DO NOTHING
			RETURNING id
		`, tenantID, docID, policyID, blobID).Scan(&insertedID)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if err == pgx.ErrNoRows {
			outcome = enqueueOutcomeAlreadyQueued
			return nil
		}
		outcome = enqueueOutcomeQueued

		// Notify on the empty→non-empty transition only.
		if queueWasEmpty && s.outbox != nil {
			if err := s.notifyDispositionQueueGrew(ctx, tx, tenantID, insertedID); err != nil {
				// A notify failure rolls back the entire enqueue tx —
				// reviewers must hear about new destructions or the
				// queue silently fills up. The next sweep retries.
				return fmt.Errorf("notify reviewers: %w", err)
			}
		}
		return nil
	})
	return outcome, err
}

// transitionDocumentToIA looks up every version's content_blob_id for
// the document and asks storage to move them to STANDARD_IA. The S3
// CopyObject + content_blobs.storage_class persist + audit emit all
// happen inside the storage service; we just orchestrate.
//
// Best-effort: if storage is unreachable or any blob fails, this
// returns an error and the caller logs it but does not roll back the
// archive lifecycle transition — the doc state is correct, only the
// storage cost is unchanged.
func (s *DocumentService) transitionDocumentToIA(ctx context.Context, tenantID, docID, policyID uuid.UUID) error {
	var blobIDs []uuid.UUID
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT v.content_blob_id
			  FROM versions v
			 WHERE v.tenant_id = $1
			   AND v.document_id = $2
			   AND v.content_blob_id IS NOT NULL
		`, tenantID, docID)
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
		return fmt.Errorf("collect blob ids: %w", err)
	}
	if len(blobIDs) == 0 {
		return nil
	}
	blobStrs := make([]string, len(blobIDs))
	for i, id := range blobIDs {
		blobStrs[i] = id.String()
	}
	_, err = s.storage.TransitionBlobsTier(ctx, &vaultdmsv1.TransitionBlobsTierRequest{
		TenantId:    tenantID.String(),
		BlobIds:     blobStrs,
		TargetClass: "STANDARD_IA",
		PolicyId:    policyID.String(),
	})
	return err
}

// notifyDispositionQueueGrew fans a dms.notify.disposition.review.v1
// out to compliance_officer + owner role members for the tenant. The
// payload follows the Wave 15 DeliveryPayload shape that notification-
// service's dms.notify.> consumer reads.
//
// Empty user list → no-op (a tenant with no compliance_officer and no
// owner has no one to notify; queue continues to grow until staffed).
func (s *DocumentService) notifyDispositionQueueGrew(ctx context.Context, tx pgx.Tx, tenantID, candidateID uuid.UUID) error {
	rows, err := tx.Query(ctx, `
		SELECT id::text
		  FROM users
		 WHERE tenant_id = $1
		   AND role IN ('compliance_officer', 'owner')
		   AND deleted_at IS NULL
	`, tenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var userIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		userIDs = append(userIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(userIDs) == 0 {
		// Nobody to notify; quiet skip.
		return nil
	}
	payload := map[string]any{
		"tenant_id":     tenantID.String(),
		"user_ids":      userIDs,
		"type":          "disposition.review",
		"title":         "Disposition queue needs review",
		"body":          "One or more documents are scheduled for disposition and require compliance review before destruction.",
		"resource_type": "disposition_candidate",
		"resource_id":   candidateID.String(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	evt := database.NewOutboxEvent(tenantID, "dms.notify.disposition.review.v1", "disposition_candidate", candidateID, body)
	return s.outbox.Insert(ctx, tx, evt)
}

// ---- plumbing ---------------------------------------------------

type retentionPolicy struct {
	ID         uuid.UUID
	Name       string
	ClassFilter string
	TagFilter  []string
	WorkspaceFilter *uuid.UUID
	RetainDays int
	ThenAction string
}

func (s *DocumentService) listActiveRetentionPolicies(ctx context.Context, tenantID uuid.UUID) ([]retentionPolicy, error) {
	var out []retentionPolicy
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, name, COALESCE(document_class_filter, ''),
			       COALESCE(tag_filter, '{}'::text[]),
			       workspace_filter, retain_days, then_action
			FROM retention_policies
			WHERE tenant_id = $1 AND is_active = true
		`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p retentionPolicy
			var ws *uuid.UUID
			if err := rows.Scan(&p.ID, &p.Name, &p.ClassFilter, &p.TagFilter, &ws, &p.RetainDays, &p.ThenAction); err != nil {
				return err
			}
			p.WorkspaceFilter = ws
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func (s *DocumentService) findDocsOverRetentionBudget(ctx context.Context, tenantID uuid.UUID, p retentionPolicy, batchSize int) ([]uuid.UUID, error) {
	var out []uuid.UUID
	cutoff := time.Now().UTC().AddDate(0, 0, -p.RetainDays)
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Minimum-viable matching — retain_days only. Filter columns
		// (document_class_filter, tag_filter, workspace_filter) are
		// added as AND clauses in the follow-up slice once the
		// indexes to support them land.
		rows, err := tx.Query(ctx, `
			SELECT id FROM documents
			WHERE tenant_id = $1
			  AND deleted_at IS NULL
			  AND lifecycle_state IN ('active', 'retained')
			  AND created_at <= $2
			ORDER BY created_at ASC
			LIMIT $3
		`, tenantID, cutoff, batchSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

// withSystemUser attaches the retention-system identity to ctx so
// mustCaller(ctx) inside UpdateLifecycle returns non-nil. Role
// "system" is the signal OPA's retention_driven rule keys off of.
func withSystemUser(ctx context.Context, tenantID uuid.UUID) context.Context {
	ctx = auth.SetTenantID(ctx, tenantID)
	ctx = auth.WithUser(ctx, auth.UserInfo{
		ID:       systemUser,
		TenantID: tenantID,
		Role:     "system",
	})
	return ctx
}

// isLegalHold is a sentinel matcher for vdmserr.ErrLegalHold without
// importing pkg/errors here (keeping sweep code tight).
func isLegalHold(err error) bool {
	return err != nil && (err.Error() == "legal_hold" ||
		containsString(err.Error(), "legal hold"))
}

func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
