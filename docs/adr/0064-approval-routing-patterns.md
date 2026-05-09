# ADR 0064 — Approval Routing Patterns

Date: 2026-05-08
Status: Accepted

## Context

The workflow service ships sequential + parallel + conditional
approvals today ([services/workflow/internal/workflows/approval.go](../../services/workflow/internal/workflows/approval.go)).
What works:

- Sequential steps with per-step timeout + signal/timer race
- Parallel `require_all` / `require_any` modes
- Conditional branching with `OnTrue` / `OnFalse` step lists
- Delegation outcome (re-runs the step under the new assignee)
- Escalation outcome (re-runs the step under `escalate_to_id`)

§10.2 calls for promoting these to first-class patterns with shared
semantics across the rest of the system:

1. **Sequential / parallel-all / parallel-first / conditional / delegation /
   escalation / recall** — all six expressible in the workflow definition.
2. **Recall** — the initiator can cancel before any approver has acted;
   today the service has `CancelInstance` but no "before any approver
   acted" guard, so a partially-approved workflow can be cancelled
   silently. That breaks the audit story.
3. **Manager-hierarchy escalation** — today escalation needs an
   explicit `escalate_to_id`. §10.2 wants escalation up the org
   hierarchy via a new `users.manager_id` column.
4. **Tenant-wide delegation** — "delegate ALL my approvals to X
   between Mar 12 and Mar 20" should not require touching every
   in-flight task.
5. **Conditional via Rego** — today's conditional uses an
   `EvaluateCondition` activity with a string expression of unspecified
   semantics. The tenant policy story (ADR 0063 step-up scopes, ADR
   0066 permission checks) already runs OPA Rego elsewhere; a workflow
   condition should use the same engine so admins write one expression
   language across the platform.
6. **Audit invariants** — every transition (start, approve, reject,
   delegate, escalate, recall, expire) must record the actor, the
   delegate (if any), and the original-vs-effective identity pair.

## Decision

### Definition shape (canonical JSON Schema)

```jsonc
{
  "id": "uuid",
  "name": "Contract approval",
  "description": "Standard contract review",
  "version": 1,
  "steps": [
    {
      "id": "legal-review",
      "type": "approval",            // approval | parallel | conditional | notification | signature
      "assignee": {
        "type": "group",             // user | group | dynamic
        "value": "legal-team-id"
      },
      "sla_hours": 48,
      "on_expire": "escalate",       // escalate | auto-approve | auto-reject
      "escalation": {
        "strategy": "manager",       // manager | fixed | chain
        "max_steps": 2
      }
    },
    {
      "id": "vp-gate",
      "type": "conditional",
      "condition_rego": "input.document.custom_metadata.contract_value > 100000",
      "on_true":  ["vp-approval"],
      "on_false": []
    },
    {
      "id": "vp-approval",
      "type": "approval",
      "assignee": { "type": "user", "value": "vp-finance-uuid" },
      "sla_hours": 24,
      "on_expire": "auto-reject"
    }
  ]
}
```

The full schema lands in [docs/api/workflow-definition-schema.json](../api/workflow-definition-schema.json),
worked examples in [docs/api/workflow-examples/](../api/workflow-examples/).

### Six routing patterns

| Pattern | Selector |
|---|---|
| **sequential** | default — array order |
| **parallel-all** | `type: parallel`, `mode: require_all`, `approvers: [..]` |
| **parallel-first** | `type: parallel`, `mode: require_any` |
| **conditional** | `type: conditional`, `condition_rego: ".."`, `on_true / on_false` |
| **delegation** | runtime: outcome=`delegate` from any assignee, OR resolved at task creation via `workflow_delegations` |
| **escalation** | runtime: SLA expiry → `on_expire`. With `escalation.strategy=manager`, walks `users.manager_id` up to `max_steps` levels |
| **recall** | initiator-only, signaled via `recall` outcome BEFORE any step has emitted an approve/reject — gated by a server-side check |

### Recall: the gate

Today `CancelInstance` blindly cancels the Temporal workflow.
ADR 0064 introduces a recall gate:

```sql
SELECT EXISTS (
  SELECT 1 FROM workflow_tasks
   WHERE tenant_id = $1 AND instance_id = $2
     AND completed_at IS NOT NULL
     AND status IN ('approved', 'rejected')
);
```

If any approver has acted, recall is rejected with 409 + the message
"workflow already in progress; cancel instead". `CancelInstance`
remains for admin override and is audited differently
(`workflow.cancelled.v1` vs `workflow.recalled.v1`).

### Delegation: two flavors

**Per-instance** — outcome=`delegate` on a single task. Today's
behavior; no schema change. Existing `DelegateTask` activity bumps
the task and re-runs the step.

**Tenant-wide** — a new table:

```sql
CREATE TABLE workflow_delegations (
  tenant_id     UUID NOT NULL,
  id            UUID NOT NULL DEFAULT gen_random_uuid(),
  delegator_id  UUID NOT NULL,
  delegate_id   UUID NOT NULL,
  starts_at     TIMESTAMPTZ NOT NULL,
  ends_at       TIMESTAMPTZ NOT NULL,
  reason        TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
```

`ResolveAssignee` (new activity) checks: when creating a task for user
`X`, is there an active delegation `delegator_id=X` whose
`[starts_at, ends_at)` covers `now()`? If yes, the task is created
under the delegate's id, and BOTH ids are written to the audit row.

Cycles are rejected at delegation save (a → b → a).

### Escalation: manager hierarchy

New column:

```sql
ALTER TABLE users ADD COLUMN manager_id UUID REFERENCES users(id);
CREATE INDEX idx_users_manager ON users(tenant_id, manager_id) WHERE manager_id IS NOT NULL;
```

`escalation.strategy = "manager"` resolves the chain at SLA expiry:

1. Look up `users.manager_id` of the current assignee.
2. If null → fall back to `escalation.fixed_to` if provided, else
   auto-reject.
3. Else → re-create the task under the manager. Repeat up to
   `escalation.max_steps` levels (default 1; cap 5).

Multi-step escalation is opt-in to avoid surprises (a deep chain
silently bypassing the original assignee).

### Conditional: Rego

`condition_rego` is a string evaluated by the embedded OPA Rego
runtime ([pkg/policy](../../pkg/policy) — same package the access-
control ADR uses). Input is:

```jsonc
{
  "document": { /* document row + custom_metadata */ },
  "workflow": { "instance_id": "..", "step_id": "..", "context": {} },
  "user":     { "id": "..", "groups": [..] }
}
```

Activity `EvaluateCondition` already exists; this ADR replaces its
stub body with a real `rego.Eval`. Bad expressions fail the workflow
with a typed `ConditionEvalError` so admins see "your condition is
broken" rather than a stuck step.

### Audit invariants

Every step emits `dms.workflow.step_transition.v1` with:

```jsonc
{
  "instance_id":     "..",
  "step_id":         "..",
  "outcome":         "approve|reject|delegate|escalate|recall|expire",
  "actor_id":        "user_who_clicked_the_button",
  "delegator_id":    null,        // populated when actor != original assignee
  "delegation_kind": null,        // null | "per_instance" | "tenant_wide"
  "from_step":       0,
  "to_step":         1,
  "at":              "RFC3339"
}
```

Audit service consumes this and writes both ids to the row.

## Consequences

- One schema, six pattern primitives. The workflow designer (frontend)
  is a thin GUI on top of the schema; tenants without the GUI can POST
  raw JSON.
- The recall gate is the most expensive new check — one SQL row per
  call. Acceptable: recall is rare and never high-frequency.
- Tenant-wide delegations are a forwarding rule, not a permission
  grant. The audit row makes the indirection visible; an admin can
  always tell who clicked the button vs. on whose behalf.
- Manager-hierarchy escalation only works when `users.manager_id` is
  populated. Not enforced — `manager_id` is nullable. Admins who care
  about escalation populate it via SCIM (ADR 0042) or the user-import
  CSV path.
- Rego adds a runtime dependency on the OPA package; already present
  for access-control so no new vendor.
- Concurrent approve/recall: the Temporal workflow's signal channel
  serializes the two. The recall gate is checked server-side before
  the signal is emitted; a race where approver-A's signal lands at
  T+0 and recall lands at T+1ms results in approval winning. Pinned
  by a test in `services/workflow/internal/workflows/approval_test.go`.

## Out of scope

- Branching beyond conditional (split-join, loops). Future ADR.
- Workflow versioning / migration of in-flight instances on
  definition update. Today: in-flight instances pin to their version.
- Cross-document workflows (one approval routes N documents). Adds
  too much state to be worth it before there's a customer ask.
