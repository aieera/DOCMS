// Package service contains the workflow service business logic.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.temporal.io/sdk/client"

	"github.com/vaultdms/vaultdms/services/workflow/internal/model"
	"github.com/vaultdms/vaultdms/services/workflow/internal/repository"
	"github.com/vaultdms/vaultdms/services/workflow/internal/workflows"
)

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

// CreateDefinition saves a workflow template.
func (s *Service) CreateDefinition(ctx context.Context, tenantID, name, desc, createdBy string, steps []model.Step) (*model.WorkflowDefinition, error) {
	id, _ := uuid.NewV7()
	now := time.Now().UTC()
	d := &model.WorkflowDefinition{
		ID: id.String(), TenantID: tenantID, Name: name, Description: desc,
		Steps: steps, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.CreateDefinition(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// ListDefinitions returns all definitions for a tenant.
func (s *Service) ListDefinitions(ctx context.Context, tenantID string) ([]*model.WorkflowDefinition, error) {
	return s.repo.ListDefinitions(ctx, tenantID)
}

// StartInstance launches a Temporal workflow execution.
func (s *Service) StartInstance(ctx context.Context, tenantID, defID, docID, initiatedBy string) (*model.WorkflowInstance, error) {
	def, err := s.repo.GetDefinition(ctx, tenantID, defID)
	if err != nil || def == nil {
		return nil, fmt.Errorf("definition not found")
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

// GetDefinition returns one definition by ID.
func (s *Service) GetDefinition(ctx context.Context, tenantID, id string) (*model.WorkflowDefinition, error) {
	return s.repo.GetDefinition(ctx, tenantID, id)
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
