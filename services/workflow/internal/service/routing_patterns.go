// ADR 0064 — service-layer entrypoints for the new routing
// patterns: tenant-wide delegations CRUD, recall (with the
// approver-acted gate), and a small helper for the audit emitter
// the workflow uses on every transition.
package service

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/aieera/sedoc/services/workflow/internal/model"
	"github.com/aieera/sedoc/services/workflow/internal/repository"
	"github.com/aieera/sedoc/services/workflow/internal/workflows"
)

// ErrRecallTooLate — recall called after at least one approver has
// already approved or rejected. Caller maps to 409.
var ErrRecallTooLate = errors.New("workflow: recall rejected — at least one approver has already acted")

// ErrDelegationCycle — refused to insert a → b because b → a (or a
// longer cycle) already exists.
var ErrDelegationCycle = errors.New("delegation: would create a cycle")

// ErrDelegationAuthority — caller is neither the delegator nor an
// admin. Per-instance delegations are owner-only; tenant-wide
// delegations may be created by an admin on behalf of a user.
var ErrDelegationAuthority = errors.New("delegation: only the delegator (or an admin) can create or revoke this rule")

// CreateDelegation persists a tenant-wide forward rule. Authority
// + cycle checks live here so the repo stays a thin SQL wrapper.
func (s *Service) CreateDelegation(ctx context.Context, tenantID, actorID, actorRole string, in repository.DelegationRow) (*repository.DelegationRow, error) {
	if in.DelegatorID != actorID && !isAdmin(actorRole) {
		return nil, ErrDelegationAuthority
	}
	if !in.EndsAt.After(in.StartsAt) {
		return nil, errors.New("ends_at must be after starts_at")
	}
	cycle, err := s.repo.HasDelegationCycle(ctx, tenantID, in.DelegatorID, in.DelegateID)
	if err != nil {
		return nil, err
	}
	if cycle {
		return nil, ErrDelegationCycle
	}
	in.TenantID = tenantID
	if err := s.repo.CreateDelegation(ctx, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// RevokeDelegation marks a forward rule as revoked.
func (s *Service) RevokeDelegation(ctx context.Context, tenantID, actorID, actorRole, id string) error {
	// Authority: only the delegator or an admin can revoke.
	rows, err := s.repo.ListDelegationsForUser(ctx, tenantID, actorID)
	if err != nil {
		return err
	}
	owns := isAdmin(actorRole)
	for _, r := range rows {
		if r.ID == id {
			owns = true
			break
		}
	}
	if !owns {
		return ErrDelegationAuthority
	}
	return s.repo.RevokeDelegation(ctx, tenantID, id)
}

// ListMyDelegations is what the settings page shows.
func (s *Service) ListMyDelegations(ctx context.Context, tenantID, userID string) ([]repository.DelegationRow, error) {
	return s.repo.ListDelegationsForUser(ctx, tenantID, userID)
}

// RecallInstance is the initiator-only "kill it before anyone acts"
// path. Differs from CancelInstance:
//   - RecallInstance is gated by the approver-acted check.
//   - Audit row uses outcome="recall" (vs cancel).
//   - Returns ErrRecallTooLate when one or more steps already settled.
//
// Cancel stays as the admin override and audits as outcome="cancel".
func (s *Service) RecallInstance(ctx context.Context, tenantID, actorID, instanceID string) error {
	inst, err := s.repo.GetInstance(ctx, tenantID, instanceID)
	if err != nil || inst == nil {
		return ErrNotFound
	}
	if inst.InitiatedBy != actorID {
		return ErrDelegationAuthority
	}

	// Gate: any task already approved or rejected? If yes, refuse.
	acted, err := s.anyActionTaken(ctx, tenantID, instanceID)
	if err != nil {
		return err
	}
	if acted {
		return ErrRecallTooLate
	}

	// Cancel the Temporal run + signal the workflow with a recall
	// outcome so it can write the audit row before exiting.
	if err := s.temporal.SignalWorkflow(ctx, "wf-"+inst.ID, inst.TemporalRunID,
		workflows.StepCompletedSignal, model.StepSignal{
			Outcome: "recall", ActorID: actorID,
		}); err != nil {
		// Best-effort: even if the signal didn't land, force-cancel
		// so the run doesn't keep firing timers.
		_ = s.temporal.CancelWorkflow(ctx, "wf-"+inst.ID, inst.TemporalRunID)
	}
	return nil
}

// anyActionTaken is the recall gate: has any approver of this instance
// already decided? Reads the instance's own tasks (M-12: it used to call
// ListTasks with an empty assignee, binding "" to a UUID column, which
// failed on every call).
func (s *Service) anyActionTaken(ctx context.Context, tenantID, instanceID string) (bool, error) {
	tasks, err := s.repo.ListTasksByInstance(ctx, tenantID, instanceID)
	if err != nil {
		return false, err
	}
	return anyDecision(tasks), nil
}

// anyDecision reports whether one of tasks records an approver's decision.
// workflow_tasks stores an approval as 'completed' and a rejection as
// 'rejected' (activities.taskStatusFor maps every raw outcome onto the
// CHECK-legal set). The gate used to look for 'approved', which is never
// stored. pending / in_progress / delegated / escalated are not decisions;
// skipped is what a recall or cancel itself writes.
func anyDecision(tasks []*model.Task) bool {
	for _, t := range tasks {
		if t.Status == "completed" || t.Status == "rejected" {
			return true
		}
	}
	return false
}

func isAdmin(role string) bool { return role == "admin" || role == "owner" }

var _ = client.Client(nil) // import kept for future Temporal-side helpers
var _ = time.Now           // ditto
