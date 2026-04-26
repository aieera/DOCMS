package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/auth"
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

		// Archive path — kept direct.
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
		}
	}
	return result, nil
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
		return nil
	})
	return outcome, err
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
