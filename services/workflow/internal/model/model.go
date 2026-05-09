// Package model holds domain types for the workflow service.
package model

import "time"

// WorkflowDefinition is a saved workflow template.
type WorkflowDefinition struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Steps       []Step    `json:"steps"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Step is one stage in a workflow. ADR 0064 widened the shape; the
// legacy fields below stay for back-compat with definitions written
// before that ADR.
type Step struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"` // approval | review | notification | condition | signature | parallel | conditional
	AssigneeID    string   `json:"assignee_id,omitempty"`
	AssigneeGroup string   `json:"assignee_group,omitempty"`
	TimeoutHours  int      `json:"timeout_hours,omitempty"`
	EscalateToID  string   `json:"escalate_to_id,omitempty"`
	Mode          string   `json:"mode,omitempty"` // require_all | require_any
	Approvers     []string `json:"approvers,omitempty"`
	Condition     string   `json:"condition,omitempty"` // legacy expr-lang
	OnTrue        []Step   `json:"on_true,omitempty"`
	OnFalse       []Step   `json:"on_false,omitempty"`

	// ADR 0064 — wider schema fields. Optional; nil/empty falls back
	// to legacy semantics so old definitions still execute.
	ID             string      `json:"id,omitempty"`
	ConditionRego  string      `json:"condition_rego,omitempty"`
	SLAHours       int         `json:"sla_hours,omitempty"`
	OnExpire       string      `json:"on_expire,omitempty"` // escalate | auto_approve | auto_reject
	Escalation     *Escalation `json:"escalation,omitempty"`
	AllowDelegate  *bool       `json:"allow_delegate,omitempty"`
}

// Escalation models the escalation strategy from ADR 0064.
type Escalation struct {
	Strategy string   `json:"strategy,omitempty"` // manager | fixed | chain
	FixedTo  string   `json:"fixed_to,omitempty"`
	Chain    []string `json:"chain,omitempty"`
	MaxSteps int      `json:"max_steps,omitempty"`
}

// WorkflowInstance is a running workflow execution.
type WorkflowInstance struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenant_id"`
	DefinitionID  string     `json:"definition_id"`
	DocumentID    string     `json:"document_id"`
	InitiatedBy   string     `json:"initiated_by"`
	Status        string     `json:"status"` // running | completed | rejected | cancelled
	CurrentStep   int        `json:"current_step"`
	TemporalRunID string     `json:"temporal_run_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// Task is a pending action item for an assignee.
type Task struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenant_id"`
	InstanceID    string     `json:"instance_id"`
	DocumentID    string     `json:"document_id"`
	DocumentTitle string     `json:"document_title,omitempty"`
	StepName      string     `json:"step_name"`
	AssigneeID    string     `json:"assignee_id"`
	Status        string     `json:"status"` // pending | approved | rejected | delegated | escalated
	Notes         string     `json:"notes,omitempty"`
	DueAt         *time.Time `json:"due_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// ApprovalInput is the Temporal workflow input.
type ApprovalInput struct {
	TenantID    string `json:"tenant_id"`
	InstanceID  string `json:"instance_id"`
	DocumentID  string `json:"document_id"`
	InitiatedBy string `json:"initiated_by"`
	Steps       []Step `json:"steps"`
}

// StepSignal is sent to advance a workflow step.
type StepSignal struct {
	StepIndex  int    `json:"step_index"`
	Outcome    string `json:"outcome"` // approve | reject | delegate | escalate
	ActorID    string `json:"actor_id"`
	Notes      string `json:"notes,omitempty"`
	DelegateTo string `json:"delegate_to,omitempty"`
}
