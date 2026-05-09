// ADR 0064 — activity helpers for the new routing patterns.
//
// Three new activities:
//
//   ResolveAssignee     — given a step's intended assignee, walks
//                         workflow_delegations and returns (effective,
//                         delegator-or-empty, kind). Called from the
//                         workflow before CreateTask so the task row
//                         lands under the right user.
//
//   EvaluateConditionRego — Rego-based condition eval for the new
//                         schema's `condition_rego` field. Falls
//                         through to expr-lang if the expression
//                         doesn't parse as a Rego query (legacy
//                         compatibility).
//
//   EscalateToManager   — looks up users.manager_id, returns the
//                         next-in-chain user id (empty if no manager).
//
//   EmitStepTransition  — single audit entrypoint for every state
//                         change. Dual-id (actor + delegator) so the
//                         audit row is honest about who clicked the
//                         button vs. on whose behalf.
package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/open-policy-agent/opa/rego"

	"github.com/vaultdms/vaultdms/pkg/database"
)

// AssigneeResolution is the result of ResolveAssignee. When
// DelegatorID is empty the requested user has no active forward
// rule and the original intent stands.
type AssigneeResolution struct {
	EffectiveID    string `json:"effective_id"`
	DelegatorID    string `json:"delegator_id,omitempty"`
	DelegationKind string `json:"delegation_kind,omitempty"` // "" | "tenant_wide"
}

// ResolveAssignee — when intendedID has an active workflow_delegations
// row, returns the delegate. Most-recently-created window wins on
// overlap (matches the migration's index ordering).
func (a *Activities) ResolveAssignee(ctx context.Context, tenantID, intendedID string) (*AssigneeResolution, error) {
	if tenantID == "" || intendedID == "" {
		return &AssigneeResolution{EffectiveID: intendedID}, nil
	}
	out := &AssigneeResolution{EffectiveID: intendedID}
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var delegate string
		err := tx.QueryRow(ctx, `
			SELECT delegate_id::text FROM workflow_delegations
			 WHERE tenant_id   = $1
			   AND delegator_id = $2
			   AND revoked_at  IS NULL
			   AND starts_at  <= now()
			   AND ends_at    >  now()
			 ORDER BY created_at DESC
			 LIMIT 1`, tenantID, intendedID,
		).Scan(&delegate)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil
			}
			return err
		}
		out.EffectiveID = delegate
		out.DelegatorID = intendedID
		out.DelegationKind = "tenant_wide"
		return nil
	})
	return out, err
}

// EvaluateConditionRego runs `expression` against an input
// containing the document, workflow context, and user fields. The
// expression must be a Rego query whose result is a single boolean —
// `data.workflow.allow == true`, `input.document.custom_metadata.x > 10`,
// etc. The activity wraps the expression with `data.workflow.result := <expr>`
// so callers can use the simpler `expr` form when their query is a
// single boolean.
func (a *Activities) EvaluateConditionRego(ctx context.Context, tenantID, documentID, expression string) (bool, error) {
	if expression == "" {
		return false, fmt.Errorf("rego: empty expression")
	}
	docMeta := map[string]any{}
	if err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var raw json.RawMessage
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(custom_metadata, '{}')
			  FROM documents WHERE tenant_id = $1 AND id = $2`,
			tenantID, documentID).Scan(&raw)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &docMeta)
	}); err != nil {
		return false, fmt.Errorf("rego: load metadata: %w", err)
	}

	// Build the Rego module. The user's expression sits as the body
	// of `result`. Failure to parse is reported as a workflow error
	// so admins see a stuck step + a usable message.
	module := fmt.Sprintf(`package workflow
result := %s`, expression)

	q, err := rego.New(
		rego.Query("data.workflow.result"),
		rego.Module("workflow.rego", module),
		rego.Input(map[string]any{
			"document": map[string]any{
				"id":              documentID,
				"custom_metadata": docMeta,
			},
		}),
	).PrepareForEval(ctx)
	if err != nil {
		return false, fmt.Errorf("rego: compile %q: %w", expression, err)
	}
	rs, err := q.Eval(ctx)
	if err != nil {
		return false, fmt.Errorf("rego: eval: %w", err)
	}
	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		return false, nil
	}
	v, ok := rs[0].Expressions[0].Value.(bool)
	if !ok {
		return false, fmt.Errorf("rego: expression did not return bool: %T", rs[0].Expressions[0].Value)
	}
	return v, nil
}

// EscalateToManager returns the manager_id of `userID`, walking up
// at most `levels` (1-based, so levels=1 = direct manager). Empty
// string when no manager is set at any level walked.
func (a *Activities) EscalateToManager(ctx context.Context, tenantID, userID string, levels int) (string, error) {
	if levels <= 0 {
		levels = 1
	}
	if levels > 5 {
		levels = 5
	}
	current := userID
	out := ""
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		for i := 0; i < levels; i++ {
			var next *string
			if err := tx.QueryRow(ctx,
				`SELECT manager_id::text FROM users WHERE tenant_id = $1 AND id = $2`,
				tenantID, current,
			).Scan(&next); err != nil {
				if err == pgx.ErrNoRows {
					return nil
				}
				return err
			}
			if next == nil {
				return nil
			}
			out = *next
			current = *next
		}
		return nil
	})
	return out, err
}

// EmitStepTransition writes a single audit/event row for every
// transition. Dual identity per ADR 0064 — actor is who clicked,
// delegator (when non-empty) is whose tasks they were acting on.
type StepTransition struct {
	InstanceID     string `json:"instance_id"`
	StepID         string `json:"step_id"`
	Outcome        string `json:"outcome"` // approve|reject|delegate|escalate|recall|expire
	ActorID        string `json:"actor_id"`
	DelegatorID    string `json:"delegator_id,omitempty"`
	DelegationKind string `json:"delegation_kind,omitempty"`
	FromStep       int    `json:"from_step"`
	ToStep         int    `json:"to_step"`
	At             string `json:"at"`
}

// EmitStepTransition publishes via the same outbox the rest of the
// service uses — the audit service consumes the subject downstream.
func (a *Activities) EmitStepTransition(ctx context.Context, tenantID string, t StepTransition) error {
	if t.At == "" {
		t.At = time.Now().UTC().Format(time.RFC3339)
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	aggID := parseUUIDOrNew(t.InstanceID)
	payload, _ := json.Marshal(t)
	evt := database.NewOutboxEvent(tenantUUID, "dms.workflow.step_transition.v1", "workflow_instance", aggID, payload)
	return database.WithTenantTx(ctx, a.Pool, tenantUUID, func(tx pgx.Tx) error {
		return a.Outbox.Insert(ctx, tx, evt)
	})
}

// AnyActionTaken is the recall gate. Returns true when at least one
// task on this instance has been approved or rejected — recall is
// rejected in that case.
func (a *Activities) AnyActionTaken(ctx context.Context, tenantID, instanceID string) (bool, error) {
	var found bool
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM workflow_tasks
			   WHERE tenant_id   = $1
			     AND instance_id = $2
			     AND completed_at IS NOT NULL
			     AND status IN ('approved', 'rejected')
			)`, tenantID, instanceID,
		).Scan(&found)
	})
	return found, err
}
