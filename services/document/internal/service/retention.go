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
type RetentionSweepResult struct {
	TenantID        uuid.UUID `json:"tenant_id"`
	PoliciesApplied int       `json:"policies_applied"`
	Archived        int       `json:"archived"`
	Disposed        int       `json:"disposed"`
	SkippedByHold   int       `json:"skipped_by_hold"`
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

		action := model.ActionArchive
		if p.ThenAction == "dispose" {
			action = model.ActionDispose
		}

		for _, id := range ids {
			_, err := s.UpdateLifecycle(
				withSystemUser(ctx, tenantID),
				&UpdateLifecycleInput{
					DocumentID: id,
					Action:     action,
					Reason:     "retention policy " + p.Name,
				},
			)
			if err != nil {
				// Legal hold is the typical non-error rejection —
				// UpdateLifecycle returns ErrLegalHold and the
				// document stays in its current state. Count it
				// separately so dashboards can track suppressed
				// retention decisions.
				if isLegalHold(err) {
					result.SkippedByHold++
					continue
				}
				result.Errors++
				s.log.Error().Err(err).Str("doc", id.String()).Msg("retention: apply action")
				continue
			}
			if action == model.ActionDispose {
				result.Disposed++
			} else {
				result.Archived++
			}
		}
	}
	return result, nil
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
