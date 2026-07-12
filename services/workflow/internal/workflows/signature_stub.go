// Package workflows: signature orchestration (ADR 0025 Wave 9).
//
// The SignatureWorkflow drives a signing ceremony end-to-end:
//
//	prepare  — an inbox task + notification per signer;
//	route    — sequentially (one signer at a time, in list order) or in
//	           parallel (all signers at once), per SequentialOrder;
//	collect  — each signer's sign/decline arrives as a signal;
//	seal     — once every signer approves, the SealSignatureCeremony activity
//	           produces REAL PAdES-B-LT revisions (one per signer + a final org
//	           seal) via the DSS sidecar and ingests the LTV-sealed version,
//	           with RegionPin (C.4) enforced signature-side before any signing;
//	events   — dms.signature.completed.v1 (carrying the sealed version + level)
//	           on success, dms.signature.declined.v1 on the first decline.
//
// The seal is workflow-owned: the ceremony COMPLETES only when the LTV
// artifact exists. The dms.signature.completed.v1 consumer remains a fallback,
// idempotent with this path via the signature service's per-request seal claim
// (so no double-seal). If the seal activity ultimately fails, the workflow
// still emits completed with the ORIGINAL version so the fallback consumer
// seals — the ceremony's signer decisions are never lost to a sealer hiccup.
package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SignatureInput configures the orchestration at start.
type SignatureInput struct {
	TenantID   string `json:"tenant_id"`
	InstanceID string `json:"instance_id"`
	// RequestID is the signature_requests row this workflow drives. It is the
	// key the signature service seals per-signer off (and the idempotency claim
	// key), so it MUST be carried into dms.signature.completed.v1; without it
	// the ceremony falls back to a single org seal.
	RequestID   string   `json:"request_id"`
	DocumentID  string   `json:"document_id"`
	VersionID   string   `json:"version_id"`
	InitiatedBy string   `json:"initiated_by"`
	Signers     []string `json:"signers"`
	// SequentialOrder=true means each signer must sign in list order;
	// false → any-order parallel.
	SequentialOrder bool `json:"sequential_order"`
}

// SignatureOutcome is the final state.
type SignatureOutcome struct {
	Status   string   `json:"status"` // pending | completed | declined
	Signed   []string `json:"signed"`
	Declined []string `json:"declined"`
	// SealedVersionID + Level are set when the workflow-owned seal produced the
	// LTV artifact (empty when the ceremony declined or the seal fell back to
	// the consumer).
	SealedVersionID string `json:"sealed_version_id,omitempty"`
	Level           string `json:"level,omitempty"`
}

// SignatureSignal is the per-signer action payload.
type SignatureSignal struct {
	SignerID string `json:"signer_id"`
	Action   string `json:"action"` // sign | decline
	Comment  string `json:"comment,omitempty"`
}

const SignatureDecisionSignal = "signature_decision"

// SignatureWorkflow orchestrates the ceremony (see package doc).
func SignatureWorkflow(ctx workflow.Context, in SignatureInput) (*SignatureOutcome, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	out := &SignatureOutcome{Status: "pending"}
	ch := workflow.GetSignalChannel(ctx, SignatureDecisionSignal)

	// prepare + route one signer: create the inbox task + notify.
	route := func(signer string) {
		_ = workflow.ExecuteActivity(ctx, "CreateTask",
			in.TenantID, in.InstanceID, in.DocumentID, "signature", signer).Get(ctx, nil)
		_ = workflow.ExecuteActivity(ctx, "NotifyAssignee",
			in.TenantID, signer, in.DocumentID, "Signature requested").Get(ctx, nil)
	}

	// apply records one decision; returns declined=true to short-circuit.
	apply := func(sig SignatureSignal) (declined bool) {
		_ = workflow.ExecuteActivity(ctx, "CompleteTask",
			in.TenantID, in.InstanceID, 0, sig.Action, sig.Comment).Get(ctx, nil)
		if sig.Action == "decline" {
			out.Status = "declined"
			out.Declined = append(out.Declined, sig.SignerID)
			_ = workflow.ExecuteActivity(ctx, "PublishEvent",
				in.TenantID, "dms.signature.declined.v1", map[string]string{
					"instance_id": in.InstanceID, "document_id": in.DocumentID,
					"request_id": in.RequestID, "signer_id": sig.SignerID, "comment": sig.Comment,
				}).Get(ctx, nil)
			return true
		}
		out.Signed = append(out.Signed, sig.SignerID)
		return false
	}

	if in.SequentialOrder {
		// Route + await each signer in list order.
		for _, signer := range in.Signers {
			route(signer)
			for {
				var sig SignatureSignal
				ch.Receive(ctx, &sig)
				if sig.SignerID != signer {
					continue // out-of-order / duplicate — sequential ignores it
				}
				if apply(sig) {
					return out, nil // declined
				}
				break // this signer done; advance
			}
		}
	} else {
		// Route everyone up front, then collect in any order.
		for _, signer := range in.Signers {
			route(signer)
		}
		pending := make(map[string]bool, len(in.Signers))
		for _, s := range in.Signers {
			pending[s] = true
		}
		for len(pending) > 0 {
			var sig SignatureSignal
			ch.Receive(ctx, &sig)
			if !pending[sig.SignerID] {
				continue // late/duplicate signal
			}
			delete(pending, sig.SignerID)
			if apply(sig) {
				return out, nil // declined
			}
		}
	}

	// All signers approved → seal + finalize + events.
	out.Status = "completed"
	sealAndComplete(ctx, in, out)
	return out, nil
}

// sealAndComplete runs the workflow-owned PAdES seal, then emits
// dms.signature.completed.v1. The completed event carries the SEALED version
// when the seal succeeded, else the original version so the fallback consumer
// seals (the per-request claim keeps it idempotent either way).
func sealAndComplete(ctx workflow.Context, in SignatureInput, out *SignatureOutcome) {
	versionID := in.VersionID
	level := ""
	if in.DocumentID != "" && in.VersionID != "" {
		// Sealing (B-LT DSS sign of N revisions + upload) gets a longer window.
		sealCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 5 * time.Minute,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
		})
		var res SealCeremonyResult
		err := workflow.ExecuteActivity(sealCtx, "SealSignatureCeremony",
			in.TenantID, in.DocumentID, in.VersionID, in.RequestID, in.InitiatedBy).Get(sealCtx, &res)
		if err == nil && res.NewVersionID != "" {
			versionID = res.NewVersionID
			level = res.Level
			out.SealedVersionID = res.NewVersionID
			out.Level = res.Level
		} else if err != nil {
			workflow.GetLogger(ctx).Error("signature seal activity failed; emitting completed for the fallback consumer",
				"error", err, "request_id", in.RequestID)
		}
	}
	_ = workflow.ExecuteActivity(ctx, "PublishEvent",
		in.TenantID, "dms.signature.completed.v1", map[string]string{
			"instance_id":  in.InstanceID,
			"document_id":  in.DocumentID,
			"version_id":   versionID,
			"request_id":   in.RequestID,
			"initiated_by": in.InitiatedBy,
			"level":        level,
		}).Get(ctx, nil)
}

// SealCeremonyResult mirrors the activity's return so the workflow can decode
// the sealed version. (Defined in the activities package too; workflows can't
// import activities without a determinism-safe boundary, so the shape is
// duplicated deliberately — it is part of the activity's public contract.)
type SealCeremonyResult struct {
	NewVersionID  string `json:"new_version_id"`
	Level         string `json:"level"`
	Fingerprint   string `json:"fingerprint"`
	AlreadySealed bool   `json:"already_sealed"`
}
