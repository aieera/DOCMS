package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
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
	ID              uuid.UUID
	Name            string
	ClassFilter     string
	TagFilter       []string
	WorkspaceFilter *uuid.UUID
	FolderFilter    *uuid.UUID
	RetainDays      int
	ThenAction      string
}

func (s *DocumentService) listActiveRetentionPolicies(ctx context.Context, tenantID uuid.UUID) ([]retentionPolicy, error) {
	var out []retentionPolicy
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, name, COALESCE(document_class_filter, ''),
			       COALESCE(tag_filter, '{}'::text[]),
			       workspace_filter, folder_filter, retain_days, then_action
			FROM retention_policies
			WHERE tenant_id = $1 AND is_active = true
		`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p retentionPolicy
			var ws, fld *uuid.UUID
			if err := rows.Scan(&p.ID, &p.Name, &p.ClassFilter, &p.TagFilter, &ws, &fld, &p.RetainDays, &p.ThenAction); err != nil {
				return err
			}
			p.WorkspaceFilter = ws
			p.FolderFilter = fld
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
		sql, args := buildRetentionMatchSQL(buildRetentionMatchInput{
			tenantID:    tenantID,
			classFilter: p.ClassFilter,
			tagFilter:   p.TagFilter,
			workspace:   p.WorkspaceFilter,
			folder:      p.FolderFilter,
			cutoff:      &cutoff,
			limit:       batchSize,
			selectIDOnly: true,
		})
		rows, err := tx.Query(ctx, sql, args...)
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

// ---- Preview & exemption ----------------------------------------------------

// PreviewRetentionPolicyInput names a DRAFT policy that the caller wants
// to evaluate before activating it. Mirrors the persisted-policy shape
// for filters, but never writes to retention_policies.
type PreviewRetentionPolicyInput struct {
	ClassFilter     string
	TagFilter       []string
	WorkspaceFilter *uuid.UUID
	FolderFilter    *uuid.UUID
	RetainDays      int
	SampleSize      int // cap on the returned sample (default 25, max 100)
}

// PreviewRetentionPolicyResult is the count + sample shown to the admin
// in the UI before they hit "Create policy".
type PreviewRetentionPolicyResult struct {
	Count  int                       `json:"count"`
	Sample []RetentionPreviewSample `json:"sample"`
}

// RetentionPreviewSample is a thin doc projection — just what the
// admin needs to recognize the document in the preview table.
type RetentionPreviewSample struct {
	ID             uuid.UUID `json:"id"`
	Title          string    `json:"title"`
	WorkspaceID    uuid.UUID `json:"workspace_id"`
	FolderID       uuid.UUID `json:"folder_id"`
	DocumentClass  string    `json:"document_class"`
	LifecycleState string    `json:"lifecycle_state"`
	CreatedAt      time.Time `json:"created_at"`
}

// PreviewRetentionPolicy returns the count + sample of documents a
// draft policy WOULD affect, given today's data. Read-only — never
// touches retention_policies. Respects retention_exempt: exempt docs
// are excluded the same way the sweep excludes them.
func (s *DocumentService) PreviewRetentionPolicy(ctx context.Context, in *PreviewRetentionPolicyInput) (*PreviewRetentionPolicyResult, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in == nil || in.RetainDays <= 0 {
		return nil, errInvalidInput("retain_days", "must be > 0")
	}
	sample := in.SampleSize
	if sample <= 0 {
		sample = 25
	}
	if sample > 100 {
		sample = 100
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -in.RetainDays)
	result := &PreviewRetentionPolicyResult{Sample: []RetentionPreviewSample{}}

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Count first (no LIMIT) — the sample query is separate so a
		// big match doesn't accidentally cap the count at SampleSize.
		countSQL, countArgs := buildRetentionMatchSQL(buildRetentionMatchInput{
			tenantID:    tenantID,
			classFilter: in.ClassFilter,
			tagFilter:   in.TagFilter,
			workspace:   in.WorkspaceFilter,
			folder:      in.FolderFilter,
			cutoff:      &cutoff,
			countOnly:   true,
		})
		if err := tx.QueryRow(ctx, countSQL, countArgs...).Scan(&result.Count); err != nil {
			return err
		}
		sampleSQL, sampleArgs := buildRetentionMatchSQL(buildRetentionMatchInput{
			tenantID:    tenantID,
			classFilter: in.ClassFilter,
			tagFilter:   in.TagFilter,
			workspace:   in.WorkspaceFilter,
			folder:      in.FolderFilter,
			cutoff:      &cutoff,
			limit:       sample,
			selectFull:  true,
		})
		rows, err := tx.Query(ctx, sampleSQL, sampleArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RetentionPreviewSample
			if err := rows.Scan(&r.ID, &r.Title, &r.WorkspaceID, &r.FolderID,
				&r.DocumentClass, &r.LifecycleState, &r.CreatedAt); err != nil {
				return err
			}
			result.Sample = append(result.Sample, r)
		}
		return rows.Err()
	})
	return result, err
}

// SetDocumentRetentionExemptInput names the exemption toggle target +
// human-readable reason (logged in the audit event so a future FRCP
// review can reconstruct the business justification).
type SetDocumentRetentionExemptInput struct {
	DocumentID uuid.UUID
	Exempt     bool
	Reason     string
}

// SetDocumentRetentionExempt flips the documents.retention_exempt flag.
// Distinct from legal hold — see migration 000054 comment. Caller must
// hold admin capability on the document (callers without it get
// vdmserr.ErrForbidden via the policy service).
func (s *DocumentService) SetDocumentRetentionExempt(ctx context.Context, in *SetDocumentRetentionExemptInput) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if in == nil || in.DocumentID == uuid.Nil {
		return errInvalidInput("document_id", "required")
	}
	if in.Exempt && len(in.Reason) == 0 {
		return errInvalidInput("reason", "required when setting exempt=true")
	}
	if len(in.Reason) > 1000 {
		return errInvalidInput("reason", "too long (max 1000 chars)")
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := s.requirePermission(ctx, userID, "admin", "document", in.DocumentID, map[string]any{}); err != nil {
			return err
		}
		var reason any
		var setBy any
		var setAt any
		if in.Exempt {
			reason = in.Reason
			setBy = userID
			setAt = time.Now().UTC()
		} else {
			// Clear all three together so the columns stay coherent.
			reason = nil
			setBy = nil
			setAt = nil
		}
		tag, err := tx.Exec(ctx, `
			UPDATE documents
			   SET retention_exempt = $3,
			       retention_exempt_reason = $4,
			       retention_exempt_set_by = $5,
			       retention_exempt_set_at = $6,
			       updated_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
		`, tenantID, in.DocumentID, in.Exempt, reason, setBy, setAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserrNotFound("document not found")
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.document.retention_exempt_set.v1", "document", in.DocumentID, map[string]any{
			"document_id": in.DocumentID.String(),
			"exempt":      in.Exempt,
			"reason":      in.Reason,
			"set_by":      userID.String(),
		})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// ---- internal: SQL builder ------------------------------------------------

type buildRetentionMatchInput struct {
	tenantID     uuid.UUID
	classFilter  string
	tagFilter    []string
	workspace    *uuid.UUID
	folder       *uuid.UUID
	cutoff       *time.Time
	limit        int
	selectIDOnly bool // sweep path
	selectFull   bool // preview-sample path
	countOnly    bool // preview-count path
}

// buildRetentionMatchSQL produces the query the sweep + preview share.
// It AND-stacks every active filter (class / any-tag / workspace /
// folder-subtree) plus the lifecycle + retention_exempt gates that
// keep terminal docs and exempt docs out of the result.
//
// One source of truth so the count, the sample, and the sweep can't
// drift — the most common "but my preview said N!" bug in legacy
// retention systems is a count query that doesn't match the apply
// query. Sharing the builder eliminates the drift.
func buildRetentionMatchSQL(in buildRetentionMatchInput) (string, []any) {
	var (
		selectClause string
		joinClause   string
	)
	switch {
	case in.countOnly:
		selectClause = "SELECT count(*) FROM documents d"
	case in.selectFull:
		selectClause = `SELECT d.id, d.title, d.workspace_id, d.folder_id,
		       COALESCE(d.document_class, ''), d.lifecycle_state, d.created_at
		FROM documents d`
	default:
		// sweep path
		selectClause = "SELECT d.id FROM documents d"
	}

	// Folder filter expands to "doc's folder is == filter OR a
	// descendant of filter" via the ltree path. We resolve the
	// filter's path once via subquery so we don't have to join the
	// folders table for non-folder-filtered policies.
	args := []any{in.tenantID}
	where := []string{
		"d.tenant_id = $1",
		"d.deleted_at IS NULL",
		"d.lifecycle_state IN ('active', 'retained')",
		"d.retention_exempt = false",
	}
	idx := 2
	if in.classFilter != "" {
		where = append(where, fmt.Sprintf("d.document_class = $%d", idx))
		args = append(args, in.classFilter)
		idx++
	}
	if len(in.tagFilter) > 0 {
		// Any-match: doc's tags overlap with the filter set.
		where = append(where, fmt.Sprintf("d.tags && $%d::text[]", idx))
		args = append(args, in.tagFilter)
		idx++
	}
	if in.workspace != nil {
		where = append(where, fmt.Sprintf("d.workspace_id = $%d", idx))
		args = append(args, *in.workspace)
		idx++
	}
	if in.folder != nil {
		// Subtree match via ltree: f.path <@ (the picked folder's path).
		// We resolve the picked folder's path in a subquery rather than
		// joining the folders table directly so the WHERE clause stays
		// short for the non-folder case (no JOIN at all).
		joinClause = fmt.Sprintf(`
			JOIN folders pf
			  ON pf.tenant_id = d.tenant_id
			 AND pf.id        = $%d
			JOIN folders f
			  ON f.tenant_id  = d.tenant_id
			 AND f.id         = d.folder_id
			 AND f.path       OPERATOR(public.<@) pf.path`, idx)
		args = append(args, *in.folder)
		idx++
	}
	if in.cutoff != nil {
		where = append(where, fmt.Sprintf("d.created_at <= $%d", idx))
		args = append(args, *in.cutoff)
		idx++
	}
	sql := selectClause + joinClause + "\n WHERE " + strings.Join(where, " AND ")
	if !in.countOnly {
		sql += "\n ORDER BY d.created_at ASC"
	}
	if in.limit > 0 && !in.countOnly {
		sql += fmt.Sprintf("\n LIMIT %d", in.limit)
	}
	return sql, args
}

// vdmserrNotFound — local wrapper so retention.go's mutation paths can
// surface "document not found" without re-importing pkg/errors at
// every call site.
func vdmserrNotFound(msg string) error { return vdmserr.NotFound(msg) }

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
