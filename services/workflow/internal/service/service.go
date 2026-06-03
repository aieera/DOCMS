// Package service contains the workflow service business logic.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"go.temporal.io/sdk/client"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/workflow/internal/model"
	"github.com/aieera/sedoc/services/workflow/internal/repository"
	"github.com/aieera/sedoc/services/workflow/internal/workflows"
)

// ErrNotFound is the access-denial / missing-row sentinel exposed to
// the handler layer. We deliberately do NOT differentiate between
// "row absent" and "row exists but caller cannot see it" — that's
// the existence-leak guard mirrored from the folder pattern.
var ErrNotFound = errors.New("not found")

const taskQueue = "vaultdms-workflow"

// Service is the workflow facade.
type Service struct {
	repo     *repository.Repository
	temporal client.Client
	log      zerolog.Logger
}

// Config is DI.
type Config struct {
	Repo     *repository.Repository
	Temporal client.Client
	Logger   zerolog.Logger
}

// New creates a Service.
func New(cfg Config) *Service {
	return &Service{repo: cfg.Repo, temporal: cfg.Temporal, log: cfg.Logger}
}

// CreateDefinition saves a workflow template. Visibility defaults to
// 'shared'; pass 'private' to stamp the caller as owner.
func (s *Service) CreateDefinition(ctx context.Context, tenantID, name, desc, createdBy, visibility string, steps []model.Step) (*model.WorkflowDefinition, error) {
	id, _ := uuid.NewV7()
	now := time.Now().UTC()
	if visibility == "" {
		visibility = model.VisibilityShared
	}
	d := &model.WorkflowDefinition{
		ID: id.String(), TenantID: tenantID, Name: name, Description: desc,
		Steps: steps, CreatedBy: createdBy,
		Visibility: visibility,
		CreatedAt:  now, UpdatedAt: now,
	}
	// Stamp owner=creator at create-time only when going straight to
	// private; shared definitions leave owner_id NULL by design.
	if visibility == model.VisibilityPrivate {
		d.OwnerID = createdBy
	}
	if err := s.repo.CreateDefinition(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// ListDefinitions returns the tenant's definitions filtered by what
// the caller can see. Admins see all; everyone else gets a post-filter
// pass via CanAccessWorkflow. The per-row gate cost is a single
// query each (no JOIN) so this stays under the existing API budget
// for typical N < 100 templates per tenant.
func (s *Service) ListDefinitions(ctx context.Context, tenantID string) ([]*model.WorkflowDefinition, error) {
	all, err := s.repo.ListDefinitions(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if s.callerIsTenantAdmin(ctx) {
		return all, nil
	}
	userID, groups := s.callerCreds(ctx)
	out := make([]*model.WorkflowDefinition, 0, len(all))
	for _, d := range all {
		// Cheap shortcuts so we only hit DB for private rows.
		if d.Visibility == model.VisibilityShared {
			out = append(out, d)
			continue
		}
		if d.OwnerID == userID && userID != "" {
			out = append(out, d)
			continue
		}
		if d.CreatedBy == userID && userID != "" {
			out = append(out, d)
			continue
		}
		ok, _ := s.repo.CanAccessWorkflow(ctx, tenantID, d.ID, userID, groups)
		if ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// StartInstance launches a Temporal workflow execution. Caller must
// pass the visibility check (mirrors GetDefinition) — execution is
// gated alongside reads so private templates can't be launched by
// holders of the id who lack a grant. System / Temporal contexts (no
// auth.User on ctx) are not callers here; this is reached from REST
// only, so a missing User is treated as no access except for the
// classic admin short-circuit.
func (s *Service) StartInstance(ctx context.Context, tenantID, defID, docID, initiatedBy string) (*model.WorkflowInstance, error) {
	def, err := s.repo.GetDefinition(ctx, tenantID, defID)
	if err != nil || def == nil {
		return nil, fmt.Errorf("definition not found")
	}
	if err := s.checkDefinitionAccess(ctx, def); err != nil {
		// Same existence-leak guard as GetDefinition.
		return nil, ErrNotFound
	}
	instID, _ := uuid.NewV7()
	inst := &model.WorkflowInstance{
		ID: instID.String(), TenantID: tenantID, DefinitionID: defID,
		DocumentID: docID, InitiatedBy: initiatedBy, Status: "running",
		CurrentStep: 0, CreatedAt: time.Now().UTC(),
	}
	input := model.ApprovalInput{
		TenantID: tenantID, InstanceID: inst.ID,
		DocumentID: docID, InitiatedBy: initiatedBy, Steps: def.Steps,
	}
	opts := client.StartWorkflowOptions{
		ID:        "wf-" + inst.ID,
		TaskQueue: taskQueue,
	}
	run, err := s.temporal.ExecuteWorkflow(ctx, opts, workflows.ApprovalWorkflow, input)
	if err != nil {
		return nil, fmt.Errorf("start workflow: %w", err)
	}
	inst.TemporalRunID = run.GetRunID()
	if err := s.repo.CreateInstance(ctx, inst); err != nil {
		return nil, err
	}
	return inst, nil
}

// GetInstance returns an instance by ID.
func (s *Service) GetInstance(ctx context.Context, tenantID, id string) (*model.WorkflowInstance, error) {
	return s.repo.GetInstance(ctx, tenantID, id)
}

// CompleteStep sends a signal to the Temporal workflow.
func (s *Service) CompleteStep(ctx context.Context, tenantID, instanceID string, signal model.StepSignal) error {
	inst, err := s.repo.GetInstance(ctx, tenantID, instanceID)
	if err != nil || inst == nil {
		return fmt.Errorf("instance not found")
	}
	return s.temporal.SignalWorkflow(ctx, "wf-"+inst.ID, inst.TemporalRunID, workflows.StepCompletedSignal, signal)
}

// ListTasks returns tasks for the given assignee filtered by status.
// An empty status returns every task regardless of state.
func (s *Service) ListTasks(ctx context.Context, tenantID, assigneeID, status string) ([]*model.Task, error) {
	return s.repo.ListTasks(ctx, tenantID, assigneeID, status)
}

// GetInstanceTimeline returns the ordered task history for an instance.
// Each task represents a step transition; the page renders these as
// timeline events.
func (s *Service) GetInstanceTimeline(ctx context.Context, tenantID, instanceID string) ([]*model.Task, error) {
	return s.repo.ListTasksByInstance(ctx, tenantID, instanceID)
}

// CancelInstance cancels a running workflow.
//
// We write the DB row to cancelled FIRST, then best-effort signal
// Temporal. The previous implementation only signaled Temporal and
// relied on the Temporal callback to update Postgres — if Temporal
// was unreachable or the workflow had already exited, the row stayed
// in 'running' forever and the UI showed a stuck workflow. The DB
// write is the user-visible source of truth; the Temporal signal is
// cleanup.
func (s *Service) CancelInstance(ctx context.Context, tenantID, instanceID string) error {
	inst, err := s.repo.GetInstance(ctx, tenantID, instanceID)
	if err != nil || inst == nil {
		return fmt.Errorf("instance not found")
	}
	if err := s.repo.MarkInstanceCancelled(ctx, tenantID, instanceID); err != nil {
		return fmt.Errorf("mark cancelled: %w", err)
	}
	// Best-effort temporal cleanup. Already-completed workflows
	// return a benign error which we swallow.
	if inst.TemporalRunID != "" {
		_ = s.temporal.CancelWorkflow(ctx, "wf-"+inst.ID, inst.TemporalRunID)
	}
	return nil
}

// GetDefinition returns one definition by ID, gated by visibility.
// Returns ErrNotFound on denial (existence-leak guard).
func (s *Service) GetDefinition(ctx context.Context, tenantID, id string) (*model.WorkflowDefinition, error) {
	def, err := s.repo.GetDefinition(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, ErrNotFound
	}
	if err := s.checkDefinitionAccess(ctx, def); err != nil {
		return nil, err
	}
	return def, nil
}

// UpdateDefinition replaces an existing template's name / description
// / steps. Active instances using this template keep executing the
// version they were started with; only future starts use the new
// definition.
func (s *Service) UpdateDefinition(ctx context.Context, tenantID, id, name, desc string, steps []model.Step) (*model.WorkflowDefinition, error) {
	cur, err := s.repo.GetDefinition(ctx, tenantID, id)
	if err != nil || cur == nil {
		return nil, fmt.Errorf("definition not found")
	}
	cur.Name = name
	cur.Description = desc
	cur.Steps = steps
	if err := s.repo.UpdateDefinition(ctx, cur); err != nil {
		return nil, err
	}
	cur.UpdatedAt = time.Now().UTC()
	return cur, nil
}

// DeleteDefinition soft-deletes a template.
func (s *Service) DeleteDefinition(ctx context.Context, tenantID, id string) error {
	return s.repo.DeleteDefinition(ctx, tenantID, id)
}

// GetActiveInstanceByDocument returns the in-flight workflow instance
// for a document, plus its full task timeline, in a single round
// trip. Used by the document detail Workflow tab.
func (s *Service) GetActiveInstanceByDocument(ctx context.Context, tenantID, documentID string) (*model.WorkflowInstance, []*model.Task, error) {
	inst, err := s.repo.GetActiveInstanceByDocument(ctx, tenantID, documentID)
	if err != nil || inst == nil {
		return nil, nil, err
	}
	tasks, terr := s.repo.ListTasksByInstance(ctx, tenantID, inst.ID)
	if terr != nil {
		return inst, nil, terr
	}
	return inst, tasks, nil
}

// ListActiveInstances returns every non-terminal instance for the
// tenant. Used by the admin Active instances table.
func (s *Service) ListActiveInstances(ctx context.Context, tenantID string) ([]*model.WorkflowInstance, error) {
	return s.repo.ListActiveInstances(ctx, tenantID)
}

// ---- Visibility + grants (migration 000062, recommended defaults) --------
//
// Caller credentials are read from auth.User on ctx. Temporal-driven
// activities run without auth.User and are NOT expected to reach
// these methods — visibility-management is an interactive surface,
// not a system-actor one. The read/execute gates (GetDefinition,
// StartInstance) ARE reached from Temporal in retry paths; for those
// `callerIsSystem` (empty UserID + non-empty tenantID set via
// auth.SetTenantID) short-circuits to true so scheduled retention /
// residency / DSR flows on private templates keep firing.

// callerCreds returns (userID, groupIDs) as strings. UserID is "" for
// system contexts (Temporal activities).
func (s *Service) callerCreds(ctx context.Context) (string, []string) {
	u, err := auth.User(ctx)
	if err != nil {
		return "", nil
	}
	groups := make([]string, 0, len(u.Groups))
	for _, g := range u.Groups {
		groups = append(groups, g.String())
	}
	return u.ID.String(), groups
}

// callerIsTenantAdmin returns true for owner|admin roles. Treats a
// system context (no auth.User) as admin so Temporal activities can
// still resolve definitions through the gated APIs without a grant.
func (s *Service) callerIsTenantAdmin(ctx context.Context) bool {
	u, err := auth.User(ctx)
	if err != nil {
		return s.callerIsSystem(ctx)
	}
	switch u.Role {
	case "owner", "admin":
		return true
	}
	return false
}

// callerIsSystem detects a Temporal-driven invocation: a tenant id is
// stamped on ctx (so RLS can fire) but no UserInfo was attached. The
// REST middleware always sets both; activities set only tenantID.
func (s *Service) callerIsSystem(ctx context.Context) bool {
	if _, err := auth.User(ctx); err == nil {
		return false
	}
	if _, err := auth.GetTenantID(ctx); err == nil {
		return true
	}
	return false
}

// canManageDefinition gates write paths (SetVisibility, AddGrant,
// RemoveGrant). Owner or admin; falls back to created_by when owner
// is unset (legacy shared rows promoted to private by the creator).
func (s *Service) canManageDefinition(ctx context.Context, def *model.WorkflowDefinition) bool {
	if s.callerIsTenantAdmin(ctx) {
		return true
	}
	userID, _ := s.callerCreds(ctx)
	if userID == "" {
		return false
	}
	if def.OwnerID == userID {
		return true
	}
	if def.OwnerID == "" && def.CreatedBy == userID {
		return true
	}
	return false
}

// checkDefinitionAccess returns ErrNotFound on denial (not Forbidden)
// so we don't leak the existence of private templates the caller
// can't see.
func (s *Service) checkDefinitionAccess(ctx context.Context, def *model.WorkflowDefinition) error {
	if def.Visibility == "" || def.Visibility == model.VisibilityShared {
		return nil
	}
	if s.callerIsTenantAdmin(ctx) {
		return nil
	}
	userID, groups := s.callerCreds(ctx)
	if userID == "" {
		// No user identity AND not a system actor → deny.
		return ErrNotFound
	}
	if def.OwnerID == userID || (def.OwnerID == "" && def.CreatedBy == userID) {
		return nil
	}
	ok, err := s.repo.CanAccessWorkflow(ctx, def.TenantID, def.ID, userID, groups)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// SetDefinitionVisibility flips a workflow definition between shared
// and private. Promoting to private stamps owner=caller when no
// owner exists yet (i.e. legacy / shared rows being locked down by
// their creator). Demoting to shared clears owner_id.
func (s *Service) SetDefinitionVisibility(ctx context.Context, tenantID, id, visibility string) (*model.WorkflowDefinition, error) {
	if visibility != model.VisibilityShared && visibility != model.VisibilityPrivate {
		return nil, fmt.Errorf("visibility must be 'shared' or 'private'")
	}
	def, err := s.repo.GetDefinition(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, ErrNotFound
	}
	if !s.canManageDefinition(ctx, def) {
		return nil, ErrNotFound
	}
	owner := ""
	if visibility == model.VisibilityPrivate {
		// Keep an existing owner; otherwise stamp the caller.
		if def.OwnerID != "" {
			owner = def.OwnerID
		} else if uid, _ := s.callerCreds(ctx); uid != "" {
			owner = uid
		}
	}
	if err := s.repo.UpdateDefinitionVisibility(ctx, tenantID, id, visibility, owner); err != nil {
		return nil, err
	}
	def.Visibility = visibility
	def.OwnerID = owner
	def.UpdatedAt = time.Now().UTC()
	return def, nil
}

// ListGrants returns the ACL on a definition. Caller must be able to
// manage the row (owner or admin); if they can't, we return
// ErrNotFound to avoid leaking the row.
func (s *Service) ListGrants(ctx context.Context, tenantID, workflowID string) ([]*model.WorkflowGrant, error) {
	def, err := s.repo.GetDefinition(ctx, tenantID, workflowID)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, ErrNotFound
	}
	if !s.canManageDefinition(ctx, def) {
		return nil, ErrNotFound
	}
	return s.repo.ListGrants(ctx, tenantID, workflowID)
}

// AddGrant idempotently adds (granteeType, granteeID) to a definition.
func (s *Service) AddGrant(ctx context.Context, tenantID, workflowID, granteeType, granteeID string) (*model.WorkflowGrant, error) {
	if granteeType != "user" && granteeType != "group" {
		return nil, fmt.Errorf("grantee_type must be 'user' or 'group'")
	}
	if _, err := uuid.Parse(granteeID); err != nil {
		return nil, fmt.Errorf("grantee_id: %w", err)
	}
	def, err := s.repo.GetDefinition(ctx, tenantID, workflowID)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, ErrNotFound
	}
	if !s.canManageDefinition(ctx, def) {
		return nil, ErrNotFound
	}
	grantedBy, _ := s.callerCreds(ctx)
	return s.repo.AddGrant(ctx, &model.WorkflowGrant{
		TenantID: tenantID, WorkflowID: workflowID,
		GranteeType: granteeType, GranteeID: granteeID,
		GrantedBy: grantedBy,
	})
}

// RemoveGrant revokes a grant.
func (s *Service) RemoveGrant(ctx context.Context, tenantID, workflowID, granteeType, granteeID string) error {
	def, err := s.repo.GetDefinition(ctx, tenantID, workflowID)
	if err != nil {
		return err
	}
	if def == nil {
		return ErrNotFound
	}
	if !s.canManageDefinition(ctx, def) {
		return ErrNotFound
	}
	if err := s.repo.RemoveGrant(ctx, tenantID, workflowID, granteeType, granteeID); err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	return nil
}
