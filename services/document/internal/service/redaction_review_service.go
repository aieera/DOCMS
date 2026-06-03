// Redaction review service surface (ADR 0079).
//
// The candidate-review redaction flow is intentionally separate from
// the legacy /redact endpoint:
//   - List candidates produced by NER for human review
//   - Approve / reject / unreject (state machine)
//   - "Apply all" — snapshots approved set, creates the redaction_jobs
//     row, emits dms.redaction.apply_requested.v1 so the intelligence
//     worker burns + uploads + creates the new version
//   - Gated download of the source (unredacted) version
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

// allowedRedactionReviewActions maps user-provided action strings to
// the candidate-status they should land in. Kept narrow on purpose:
// pending → approved (approve), pending → rejected (reject),
// approved/rejected → pending (unreject). `applied` only ever flips
// in the worker once burn-in completes.
var allowedRedactionReviewActions = map[string]string{
	"approve":  "approved",
	"reject":   "rejected",
	"unreject": "pending",
}

func (s *DocumentService) ListRedactionCandidates(
	ctx context.Context,
	documentID uuid.UUID,
	opts repository.ListRedactionCandidatesOpts,
) ([]repository.RedactionCandidate, int64, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, 0, err
	}
	if _, perr := s.requireDocPermission(ctx, tenantID, mustCallerUserID(ctx), documentID, "view"); perr != nil {
		return nil, 0, perr
	}
	var (
		rows  []repository.RedactionCandidate
		total int64
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		rows, total, lerr = s.repos.Redaction.ListCandidates(ctx, tx, tenantID, documentID, opts)
		return lerr
	})
	return rows, total, err
}

// ReviewRedactionCandidate transitions a single candidate through the
// state machine. Caller needs `edit` on the document; admins / owners
// pass via OPA Rule 6 regardless.
func (s *DocumentService) ReviewRedactionCandidate(
	ctx context.Context,
	documentID, candidateID uuid.UUID,
	action, note string,
) (*repository.RedactionCandidate, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	target, ok := allowedRedactionReviewActions[action]
	if !ok {
		return nil, vdmserr.Validation("action", "must be approve|reject|unreject")
	}
	if len(note) > 1000 {
		return nil, vdmserr.Validation("note", "max 1000 chars")
	}
	if _, perr := s.requireDocPermission(ctx, tenantID, userID, documentID, "edit"); perr != nil {
		return nil, perr
	}
	var out *repository.RedactionCandidate
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, gErr := s.repos.Redaction.GetCandidate(ctx, tx, tenantID, candidateID)
		if gErr != nil {
			return gErr
		}
		if cur.DocumentID != documentID {
			return vdmserr.Validation("candidate_id", "belongs to a different document")
		}
		if cur.Status == "applied" {
			return vdmserr.Conflict("candidate has already been applied; create a new redaction pass instead")
		}
		// Reject "approve" on already-approved (idempotent no-op),
		// allow flip back via unreject.
		if cur.Status == target {
			out = cur
			return nil
		}
		if uErr := s.repos.Redaction.UpdateCandidateStatus(ctx, tx, tenantID, candidateID, userID, target, note); uErr != nil {
			return uErr
		}
		updated, gErr := s.repos.Redaction.GetCandidate(ctx, tx, tenantID, candidateID)
		if gErr != nil {
			return gErr
		}
		out = updated
		return nil
	})
	return out, err
}

// ApplyRedactionInput is the validated shape coming out of the apply
// handler. document_id + version_id identify *which* approved set to
// burn; force_admin_approve trips the >50-candidate gate to a yes.
type ApplyRedactionInput struct {
	VersionID         uuid.UUID
	ForceAdminApprove bool
	BulkAdminThreshold int32 // default 50; overridable for tests
}

// ApplyRedactionResult is what the handler returns: enough info for
// the UI to redirect or poll.
type ApplyRedactionResult struct {
	JobID         uuid.UUID
	CandidateCount int32
	Status        string
}

// ApplyRedaction snapshots the approved candidate set, creates a
// redaction_jobs row in `queued`, emits the apply_requested event,
// and returns. The intelligence worker takes it from there. Atomic:
// the job row + the outbox event land in one tx.
func (s *DocumentService) ApplyRedaction(
	ctx context.Context,
	documentID uuid.UUID,
	in ApplyRedactionInput,
) (*ApplyRedactionResult, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if _, perr := s.requireDocPermission(ctx, tenantID, userID, documentID, "edit"); perr != nil {
		return nil, perr
	}
	if in.VersionID == uuid.Nil {
		return nil, vdmserr.Validation("version_id", "required")
	}
	threshold := in.BulkAdminThreshold
	if threshold <= 0 {
		threshold = 50
	}
	role := callerRole(ctx)
	isAdmin := role == "owner" || role == "admin" || role == "compliance_officer"

	var out *ApplyRedactionResult
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		approved, lErr := s.repos.Redaction.ListApprovedForVersion(ctx, tx, tenantID, in.VersionID)
		if lErr != nil {
			return lErr
		}
		if len(approved) == 0 {
			return vdmserr.Validation("approved", "no approved candidates to apply")
		}
		// Bulk-apply admin gate: >threshold candidates needs an
		// admin/compliance_officer caller OR an explicit
		// force_admin_approve flag (which only admins can set in
		// practice — the frontend hides it for non-admins).
		if int32(len(approved)) > threshold && !isAdmin && !in.ForceAdminApprove {
			return vdmserr.Forbidden(fmt.Sprintf(
				"%d candidates exceeds bulk-apply threshold of %d; requires admin approval",
				len(approved), threshold,
			))
		}
		// Look up the source blob so the worker can download it
		// without round-tripping back to the document service.
		var (
			storageBucket string
			storageKey    string
		)
		if err := tx.QueryRow(ctx, `
            SELECT split_part(b.storage_uri, '/', 3), -- bucket
                   regexp_replace(b.storage_uri, '^s3://[^/]+/', '')
              FROM document_versions v
              JOIN content_blobs    b ON b.id = v.content_blob_id
             WHERE v.tenant_id = $1 AND v.id = $2`,
			tenantID, in.VersionID,
		).Scan(&storageBucket, &storageKey); err != nil {
			return vdmserr.Wrap(vdmserr.ErrNotFound, err)
		}

		// Snapshot the approved candidates into the job row so even
		// if redaction_candidates rows get purged later the audit
		// trail survives.
		snapshot := make([]map[string]any, 0, len(approved))
		for _, c := range approved {
			var rects any
			_ = json.Unmarshal(c.Rectangles, &rects)
			snapshot = append(snapshot, map[string]any{
				"candidate_id": c.ID.String(),
				"entity_type":  c.EntityType,
				"entity_value": c.EntityValue,
				"rectangles":   rects,
			})
		}
		snapshotJSON, _ := json.Marshal(snapshot)

		jobID, _ := uuid.NewV7()
		job := &repository.RedactionJob{
			ID:                 jobID,
			TenantID:           tenantID,
			DocumentID:         documentID,
			SourceVersionID:    in.VersionID,
			Status:             "queued",
			CandidatesSnapshot: snapshotJSON,
			CandidateCount:     int32(len(approved)),
			AppliedBy:          userID,
		}
		if cErr := s.repos.Redaction.CreateJob(ctx, tx, job); cErr != nil {
			return cErr
		}

		// Emit dms.redaction.apply_requested.v1 with the full set so
		// the worker has everything it needs in one envelope.
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.redaction.apply_requested.v1", "redaction_job", jobID,
			map[string]any{
				"job_id":            jobID.String(),
				"tenant_id":         tenantID.String(),
				"document_id":       documentID.String(),
				"source_version_id": in.VersionID.String(),
				"storage_bucket":    storageBucket,
				"storage_key":       storageKey,
				"candidates":        snapshot,
				"applied_by":        userID.String(),
				"requested_at":      time.Now().UTC().Format(time.RFC3339Nano),
			})
		if oErr != nil {
			return oErr
		}
		if iErr := s.repos.Outbox.Insert(ctx, tx, evt); iErr != nil {
			return iErr
		}
		out = &ApplyRedactionResult{
			JobID:          jobID,
			CandidateCount: int32(len(approved)),
			Status:         "queued",
		}
		return nil
	})
	return out, err
}

// CanViewUnredacted returns nil if the caller is permitted to download
// the source (pre-redaction) version of a document. Wraps the OPA
// `view_unredacted` capability check so handlers can branch on the
// error directly instead of repeating the policy lookup.
func (s *DocumentService) CanViewUnredacted(
	ctx context.Context, documentID uuid.UUID,
) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	// requireDocPermission loads the doc + adds workspace/lifecycle
	// context to the OPA query. Owner/admin still pass via Rule 6.
	if _, perr := s.requireDocPermission(ctx, tenantID, userID, documentID, "view_unredacted"); perr != nil {
		return perr
	}
	return nil
}

// LookupSourceVersionFromRedacted maps a redacted version id to its
// original source version via the redaction_jobs ledger. Used by the
// /unredacted download endpoint so the gated download can resolve
// "the doc was redacted; here's the original" without the caller
// guessing version ids.
func (s *DocumentService) LookupSourceVersionFromRedacted(
	ctx context.Context, redactedVersionID uuid.UUID,
) (uuid.UUID, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	var srcID uuid.UUID
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		j, gErr := s.repos.Redaction.JobForRedactedVersion(ctx, tx, tenantID, redactedVersionID)
		if gErr != nil {
			return gErr
		}
		srcID = j.SourceVersionID
		return nil
	})
	return srcID, err
}

// callerRole pulls the user role off ctx for the bulk-apply gate.
// Returns "" when not set — falls into the non-admin branch.
func callerRole(ctx context.Context) string {
	// Light wrapper so the gate is testable without the full auth
	// context plumbing. Returns the X-User-Role header value the
	// authedContext helper stamped on the request context.
	v := ctx.Value(callerRoleKey{})
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

type callerRoleKey struct{}

// WithCallerRole stamps a role onto ctx so callerRole can read it
// during apply. Used by the handler shim; intentionally not exported
// further so tests can stub independently.
func WithCallerRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, callerRoleKey{}, strings.TrimSpace(role))
}
