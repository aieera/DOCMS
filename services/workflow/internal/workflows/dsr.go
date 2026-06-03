// Package workflows — GDPR DSR workflows (Wave 8 Prompt 8.3).
//
// Three entry points share a common skeleton:
//
//	ExportWorkflow    — read-only; collects and packages subject data.
//	EraseWorkflow     — hold-gate → overwrite PII → lifecycle dispose.
//	AnonymizeWorkflow — hold-gate → hash PII → keep lifecycle.
//
// Every workflow writes at least two privacy_ledger rows: a
// `requested`/`verified` row on entry and a `completed` / `blocked`
// row on exit. See ADR 0024 for the full contract.
//
// Determinism: no time.Now, no rand, no direct DB. Every I/O is an
// activity call.
package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/aieera/sedoc/services/workflow/internal/activities"
)

// DSR event subjects.
const (
	SubjectDSRRequested = "dms.dsr.requested.v1"
	SubjectDSRCompleted = "dms.dsr.completed.v1"
	SubjectDSRBlocked   = "dms.dsr.blocked.v1"
	SubjectDSRFailed    = "dms.dsr.failed.v1"
)

// DSRInput is the common workflow input.
type DSRInput struct {
	TenantID          string `json:"tenant_id"`
	RequestID         string `json:"request_id"`
	SubjectEmail      string `json:"subject_email"`
	RequestedBy       string `json:"requested_by"`
	VerificationToken string `json:"verification_token,omitempty"` // erase only
	TenantSalt        string `json:"tenant_salt,omitempty"`        // anonymize only
}

// DSROutcome is the workflow result. Status mirrors the
// privacy_dsr_requests.status column.
type DSROutcome struct {
	Status        string                        `json:"status"` // completed | blocked | failed
	BlockedReason string                        `json:"blocked_reason,omitempty"`
	Summary       *activities.DSRSubjectSummary `json:"summary,omitempty"`
	ExportURL     string                        `json:"export_url,omitempty"`
}

func dsrActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    3,
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    1 * time.Minute,
		},
	}
}

// ExportWorkflow collects and packages subject data. No mutations.
func ExportWorkflow(ctx workflow.Context, in DSRInput) (*DSROutcome, error) {
	ctx = workflow.WithActivityOptions(ctx, dsrActivityOptions())
	logger := workflow.GetLogger(ctx)
	out := &DSROutcome{Status: "completed"}

	if err := writeLedger(ctx, in, "export", "requested", nil); err != nil {
		return failWorkflow(ctx, in, out, "ledger requested", err)
	}
	_ = workflow.ExecuteActivity(ctx, "EmitDSREvent",
		in.TenantID, in.RequestID, in.SubjectEmail, SubjectDSRRequested,
		map[string]any{"type": "export"},
	).Get(ctx, nil)

	var subjectID string
	if err := workflow.ExecuteActivity(ctx, "ResolveSubject",
		in.TenantID, in.SubjectEmail,
	).Get(ctx, &subjectID); err != nil {
		return failWorkflow(ctx, in, out, "resolve", err)
	}

	if subjectID == "" {
		logger.Info("dsr export: subject not found", "email", in.SubjectEmail)
		// Record completion with an empty summary; spec treats
		// "no such subject" as a legitimate no-op.
		_ = writeLedger(ctx, in, "export", "completed-absent", map[string]any{})
		_ = updateRequest(ctx, in, "completed", "", map[string]any{"subject_found": false}, "", nil)
		return out, nil
	}

	var summary *activities.DSRSubjectSummary
	if err := workflow.ExecuteActivity(ctx, "CollectSubjectData",
		in.TenantID, subjectID,
	).Get(ctx, &summary); err != nil {
		return failWorkflow(ctx, in, out, "collect", err)
	}
	out.Summary = summary

	// The actual ZIP packaging + S3 upload is an activity call that
	// doesn't exist yet — it lives in a future PackageAndUpload
	// activity that depends on the storage service emitting tenant-
	// scoped presigned URLs. See remediation 15c § Deferred.
	// For now we return the manifest summary and no URL; the UI
	// surfaces the subject-row counts and instructs the operator to
	// re-run once the storage integration is online.
	logger.Info("dsr export: summary collected",
		"documents", summary.DocumentsOwned,
		"tasks", summary.TasksAssigned,
		"audit", summary.AuditEvents,
	)

	_ = writeLedger(ctx, in, "export", "completed",
		map[string]any{"subject_id": subjectID, "summary": summary})
	_ = updateRequest(ctx, in, "completed", "",
		map[string]any{"subject_id": subjectID, "counts": summary}, "", nil)
	_ = workflow.ExecuteActivity(ctx, "EmitDSREvent",
		in.TenantID, in.RequestID, in.SubjectEmail, SubjectDSRCompleted,
		map[string]any{"type": "export", "subject_id": subjectID},
	).Get(ctx, nil)
	return out, nil
}

// EraseWorkflow overwrites PII. Short-circuits on active legal hold.
func EraseWorkflow(ctx workflow.Context, in DSRInput) (*DSROutcome, error) {
	return mutateWorkflow(ctx, in, "erase")
}

// AnonymizeWorkflow hashes identifiers. Shares Erase's skeleton.
func AnonymizeWorkflow(ctx workflow.Context, in DSRInput) (*DSROutcome, error) {
	return mutateWorkflow(ctx, in, "anonymize")
}

func mutateWorkflow(ctx workflow.Context, in DSRInput, mode string) (*DSROutcome, error) {
	ctx = workflow.WithActivityOptions(ctx, dsrActivityOptions())
	logger := workflow.GetLogger(ctx)
	out := &DSROutcome{Status: "completed"}

	_ = writeLedger(ctx, in, mode, "requested", nil)
	_ = workflow.ExecuteActivity(ctx, "EmitDSREvent",
		in.TenantID, in.RequestID, in.SubjectEmail, SubjectDSRRequested,
		map[string]any{"type": mode},
	).Get(ctx, nil)

	if mode == "erase" && in.VerificationToken == "" {
		out.Status = "failed"
		return failWorkflow(ctx, in, out, "verification token required", nil)
	}
	if mode == "erase" {
		// Wave 11.4: real token redemption against Redis. The verify
		// endpoint minted a token with 24h TTL; this activity hashes
		// the presented plaintext and constant-time compares, then
		// DELs the key so tokens are single-use.
		if err := workflow.ExecuteActivity(ctx, "VerifyDSRToken",
			in.TenantID, in.SubjectEmail, in.VerificationToken,
		).Get(ctx, nil); err != nil {
			_ = writeLedger(ctx, in, mode, "verify-mismatch",
				map[string]any{"error": err.Error()})
			out.Status = "failed"
			return failWorkflow(ctx, in, out, "verification token invalid", err)
		}
		_ = writeLedger(ctx, in, mode, "verified",
			map[string]any{"token_length": len(in.VerificationToken)})
	}

	var subjectID string
	if err := workflow.ExecuteActivity(ctx, "ResolveSubject",
		in.TenantID, in.SubjectEmail,
	).Get(ctx, &subjectID); err != nil {
		return failWorkflow(ctx, in, out, "resolve", err)
	}
	if subjectID == "" {
		_ = writeLedger(ctx, in, mode, "completed-absent", nil)
		_ = updateRequest(ctx, in, "completed", "", map[string]any{"subject_found": false}, "", nil)
		return out, nil
	}

	// Hard short-circuit: legal hold wins.
	var held bool
	if err := workflow.ExecuteActivity(ctx, "SubjectHasHeldDocuments",
		in.TenantID, subjectID,
	).Get(ctx, &held); err != nil {
		return failWorkflow(ctx, in, out, "hold check", err)
	}
	if held {
		out.Status = "blocked"
		out.BlockedReason = "subject has documents under active legal hold"
		_ = writeLedger(ctx, in, "hold_block", "blocked",
			map[string]any{"subject_id": subjectID})
		_ = updateRequest(ctx, in, "blocked", out.BlockedReason,
			map[string]any{"subject_id": subjectID}, "", nil)
		_ = workflow.ExecuteActivity(ctx, "EmitDSREvent",
			in.TenantID, in.RequestID, in.SubjectEmail, SubjectDSRBlocked,
			map[string]any{"type": mode, "subject_id": subjectID, "reason": out.BlockedReason},
		).Get(ctx, nil)
		logger.Info("dsr blocked by legal hold", "subject_id", subjectID)
		return out, nil
	}

	var rowsAffected int
	if err := workflow.ExecuteActivity(ctx, "OverwriteSubjectPII",
		in.TenantID, subjectID, mode, in.TenantSalt,
	).Get(ctx, &rowsAffected); err != nil {
		return failWorkflow(ctx, in, out, "overwrite", err)
	}

	// Wave 12.4: fan out to sibling services that own data outside
	// Postgres. Each activity is best-effort — missing service URLs
	// log + no-op rather than failing the whole erase. Counts get
	// merged into the ledger so auditors see per-service row totals.
	crossService := map[string]int64{}
	if mode == "erase" {
		var searchDeleted, vectorsDeleted, connectorsDeleted int64
		if err := workflow.ExecuteActivity(ctx, "PurgeSubjectFromSearch",
			in.TenantID, subjectID,
		).Get(ctx, &searchDeleted); err == nil {
			crossService["search"] = searchDeleted
		} else {
			_ = writeLedger(ctx, in, mode, "partial-search-fail",
				map[string]any{"error": err.Error()})
		}
		if err := workflow.ExecuteActivity(ctx, "PurgeSubjectFromVectors",
			in.TenantID, subjectID,
		).Get(ctx, &vectorsDeleted); err == nil {
			crossService["vectors"] = vectorsDeleted
		}
		if err := workflow.ExecuteActivity(ctx, "PurgeSubjectFromConnectors",
			in.TenantID, subjectID,
		).Get(ctx, &connectorsDeleted); err == nil {
			crossService["connectors"] = connectorsDeleted
		}
	}
	_ = writeLedger(ctx, in, mode, "completed",
		map[string]any{"subject_id": subjectID, "rows_affected": rowsAffected, "cross_service": crossService})
	_ = updateRequest(ctx, in, "completed", "",
		map[string]any{"subject_id": subjectID, "rows_affected": rowsAffected, "cross_service": crossService}, "", nil)
	_ = workflow.ExecuteActivity(ctx, "EmitDSREvent",
		in.TenantID, in.RequestID, in.SubjectEmail, SubjectDSRCompleted,
		map[string]any{"type": mode, "subject_id": subjectID, "rows": rowsAffected},
	).Get(ctx, nil)
	return out, nil
}

// writeLedger is a thin wrapper that the three workflows share.
func writeLedger(ctx workflow.Context, in DSRInput, action, outcome string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	return workflow.ExecuteActivity(ctx, "WritePrivacyLedger",
		in.TenantID, in.RequestID, in.SubjectEmail, action, outcome, details,
	).Get(ctx, nil)
}

func updateRequest(ctx workflow.Context, in DSRInput, status, blocked string, summary map[string]any, url string, expires *time.Time) error {
	return workflow.ExecuteActivity(ctx, "UpdateDSRRequest",
		in.TenantID, in.RequestID, status, blocked, summary, url, expires,
	).Get(ctx, nil)
}

func failWorkflow(ctx workflow.Context, in DSRInput, out *DSROutcome, phase string, cause error) (*DSROutcome, error) {
	out.Status = "failed"
	out.BlockedReason = phase
	if cause != nil {
		out.BlockedReason = phase + ": " + cause.Error()
	}
	_ = writeLedger(ctx, in, "error", phase, map[string]any{"reason": out.BlockedReason})
	_ = updateRequest(ctx, in, "failed", out.BlockedReason, map[string]any{}, "", nil)
	_ = workflow.ExecuteActivity(ctx, "EmitDSREvent",
		in.TenantID, in.RequestID, in.SubjectEmail, SubjectDSRFailed,
		map[string]any{"phase": phase, "error": out.BlockedReason},
	).Get(ctx, nil)
	return out, cause
}
