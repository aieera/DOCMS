// Package workflows: signature orchestration stub (Wave 7 Prompt 7.1;
// real PAdES signing lives in Wave 9).
//
// This workflow is registered at worker boot so the task queue
// acknowledges signature-request signals that come in during the
// Wave 8/9 development window. The real body (PAdES envelope, signer
// sequence, HSM directive, LTV emission) is Wave 9's deliverable.
package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SignatureInput configures the orchestration at start.
type SignatureInput struct {
	TenantID    string   `json:"tenant_id"`
	InstanceID  string   `json:"instance_id"`
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
	Status   string   `json:"status"` // pending | completed | cancelled
	Signed   []string `json:"signed"`
	Declined []string `json:"declined"`
}

// SignatureSignal is the per-signer action payload.
type SignatureSignal struct {
	SignerID string `json:"signer_id"`
	Action   string `json:"action"` // sign | decline
	Comment  string `json:"comment,omitempty"`
}

const SignatureDecisionSignal = "signature_decision"

// SignatureWorkflow is the stub. Wave 9 will replace it with the
// real PAdES flow. Today it:
//
//   - creates inbox tasks for each signer (so "My Tasks" is populated),
//   - waits for all signers to signal (no timer, no PDF signing),
//   - emits dms.signature.completed.v1 on success /
//     dms.signature.declined.v1 on first decline.
//
// This is enough surface to wire the frontend in Wave 10 without
// blocking on the PAdES library selection ADR (0025 — Wave 9).
func SignatureWorkflow(ctx workflow.Context, in SignatureInput) (*SignatureOutcome, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	for _, s := range in.Signers {
		_ = workflow.ExecuteActivity(ctx, "CreateTask",
			in.TenantID, in.InstanceID, in.DocumentID, "signature", s,
		).Get(ctx, nil)
		_ = workflow.ExecuteActivity(ctx, "NotifyAssignee",
			in.TenantID, s, in.DocumentID, "Signature requested",
		).Get(ctx, nil)
	}

	pending := make(map[string]bool, len(in.Signers))
	for _, s := range in.Signers {
		pending[s] = true
	}
	out := &SignatureOutcome{Status: "pending"}

	ch := workflow.GetSignalChannel(ctx, SignatureDecisionSignal)
	for len(pending) > 0 {
		var sig SignatureSignal
		ch.Receive(ctx, &sig)
		if !pending[sig.SignerID] {
			continue // late/duplicate signal
		}
		delete(pending, sig.SignerID)
		_ = workflow.ExecuteActivity(ctx, "CompleteTask",
			in.TenantID, in.InstanceID, 0, sig.Action, sig.Comment,
		).Get(ctx, nil)
		if sig.Action == "decline" {
			out.Status = "declined"
			out.Declined = append(out.Declined, sig.SignerID)
			_ = workflow.ExecuteActivity(ctx, "PublishEvent",
				in.TenantID, "dms.signature.declined.v1", map[string]string{
					"instance_id": in.InstanceID, "document_id": in.DocumentID,
					"signer_id": sig.SignerID, "comment": sig.Comment,
				},
			).Get(ctx, nil)
			return out, nil
		}
		out.Signed = append(out.Signed, sig.SignerID)
	}
	out.Status = "completed"
	_ = workflow.ExecuteActivity(ctx, "PublishEvent",
		in.TenantID, "dms.signature.completed.v1", map[string]string{
			"instance_id": in.InstanceID, "document_id": in.DocumentID,
		},
	).Get(ctx, nil)
	return out, nil
}
