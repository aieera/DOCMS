# ADR 0114 — DSR admin approval workflow

Status: Proposed.
Date: 2026-05-21.
Depends on: existing DSR/privacy implementation (privacy_handler.go +
the Temporal `dsr_export` / `dsr_erase` / `dsr_anonymize` workflows).

## Context

Today (per [services/document/internal/handler/privacy_handler.go](../../services/document/internal/handler/privacy_handler.go))
a GDPR data-subject request goes from `POST /privacy/dsr/{type}`
straight to the Temporal workflow that executes it. The state
machine is:

```
pending (write row, kick off workflow)
  ↓
running (workflow executing)
  ↓
completed | blocked | failed
```

There is **no human checkpoint**. An admin who fat-fingers an erase
request triggers it immediately. "Blocked" means the workflow's
legal-hold guard tripped — there's no UI to override that block
short of clearing the hold and re-submitting. "Pending" is just a
transient "about to start" state, not a "waiting for approval"
state.

GDPR best practice for irreversible actions (`erase`, `anonymize`)
calls for two-person control: one admin submits, a different one
approves, both decisions audited. We want to add that gate without
breaking the export flow (which is non-destructive and can stay
auto-execute, or opt into approval for high-sensitivity tenants).

## Decision

Add an `awaiting_approval` state and four admin endpoints. Make the
workflow pause on a Temporal signal at the start, resume on approve,
fail on reject. Audit every transition. Per-tenant policy decides
which request types require approval (default: `erase` +
`anonymize`; `export` opt-in).

### New state machine

```
                       reject
                       ┌──────────────── rejected
                       │
awaiting_approval ─────┤
                       │ approve
                       └──────────────── running ───┬── completed
                                                    ├── blocked ─── override ──→ running
                                                    └── failed
```

`pending` is removed (was a misnomer). `cancelled` is added for the
window between submit and either approve/reject — the requester or
an admin can cancel their own submission.

### New columns on `privacy_dsr_requests`

```sql
ALTER TABLE privacy_dsr_requests
  ADD COLUMN approval_required boolean NOT NULL DEFAULT false,
  ADD COLUMN approved_by       uuid    NULL,
  ADD COLUMN approved_at       timestamptz NULL,
  ADD COLUMN reject_reason     text    NULL,
  ADD COLUMN override_reason   text    NULL,
  ADD COLUMN overridden_by     uuid    NULL,
  ADD COLUMN overridden_at     timestamptz NULL;
```

Plus a new audit table for the full transition history:

```sql
CREATE TABLE privacy_dsr_transitions (
  tenant_id  uuid NOT NULL,
  request_id uuid NOT NULL,
  from_state text NOT NULL,
  to_state   text NOT NULL,
  actor      uuid NOT NULL,
  actor_role text NOT NULL,
  reason     text,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, request_id, occurred_at)
);
```

(Existing tenant RLS policy applies to both.)

### New tenant policy column

```sql
ALTER TABLE privacy_settings
  ADD COLUMN require_approval_for text[] NOT NULL
    DEFAULT ARRAY['erase','anonymize'];
```

Empty array = auto-execute everything (current behavior).
`['erase','anonymize','export']` = strictest.

### New endpoints

All require `role IN ('owner','admin','compliance_officer')`:

| Method | Path | Effect |
|---|---|---|
| `POST` | `/api/v1/privacy/dsr/{id}/approve` | `awaiting_approval` → `running`; signals workflow to resume. |
| `POST` | `/api/v1/privacy/dsr/{id}/reject` | `awaiting_approval` → `rejected`; cancels workflow; requires `reason` body field. |
| `POST` | `/api/v1/privacy/dsr/{id}/override` | `blocked` → `running`; signals workflow to bypass the blocker (legal hold etc.); requires `reason`. |
| `POST` | `/api/v1/privacy/dsr/{id}/cancel` | `awaiting_approval` → `cancelled`; cancels workflow. Callable by requester OR admin. |

**Self-approval forbidden**: an approver cannot equal the submitter.
Enforced at handler level + audit-log every attempt.

### Workflow change

The Temporal workflow gets a new first activity:

```go
// At the top of DSREraseWorkflow / DSRAnonymizeWorkflow:
if approvalRequired {
    sig := workflow.GetSignalChannel(ctx, "approval")
    var decision string  // "approve" | "reject" | "cancel"
    sig.Receive(ctx, &decision)
    if decision != "approve" {
        return nil // workflow exits; status set by the handler
    }
}
// ... existing activities continue
```

For blocked → override: the blocker is a workflow error today.
Change it to a `Wait for signal "override" OR 7d timeout` so the
admin can resume without re-submitting.

### UI

[web/src/routes/_authenticated/admin/privacy.tsx](../../web/src/routes/_authenticated/admin/privacy.tsx)
gets per-row action buttons keyed off status:

| Status | Buttons (admin) | Buttons (non-admin) |
|---|---|---|
| `awaiting_approval` | Approve · Reject · View Details · Cancel | View Details · Cancel (own only) |
| `running` | View Details · Cancel | View Details |
| `blocked` | Override · Reject · View Details | View Details |
| `completed` | View Details · Download | View Details · Download |
| `rejected`, `cancelled`, `failed` | View Details | View Details |

Approve / Reject / Override / Cancel all open confirmation dialogs.
Reject + Override require a typed reason. Approve for `erase`
requires typing the subject email to confirm.

A Detail modal (mounted via Radix Dialog) shows: subject, type,
submitter, submitter role, all timestamps, transition history (from
the new `privacy_dsr_transitions` table), blocked reason, override
reason, approver, export URL (when ready).

### Audit + notifications

Every transition writes a row to `privacy_dsr_transitions` AND
publishes `dms.privacy.dsr.<from>.<to>.v1` to the outbox, so the
notification service can alert the submitter and the compliance
officer group.

## Phases

The implementation splits into three shippable PRs so we can land
the gate quickly and iterate the UX:

**Phase 1 — Backend gate** (2-3 days):
- Migration: new columns + `privacy_dsr_transitions` + `privacy_settings.require_approval_for`
- Submit handler: insert as `awaiting_approval` when approval required, else current `pending`
- New endpoints: approve / reject / override / cancel
- Workflow: signal-driven approval gate
- Self-approval guard
- Unit + integration tests
- No UI yet — admins use API directly

**Phase 2 — Admin UI** (1-2 days):
- Per-row action buttons + state-aware visibility
- Detail modal with transition history
- Confirmation dialogs (typed reason for reject/override; typed email for approve-erase)
- Role-gated rendering (non-admins see View Details only)

**Phase 3 — Notifications + policies** (1 day):
- `dms.privacy.dsr.*.v1` events + notification templates
- Admin settings UI for `require_approval_for`
- "Self-approval blocked" toast + audit visibility

## Why not just inline buttons + hit nonexistent endpoints

Two reasons:
1. **Compliance**: GDPR audit demands a full transition log. Adding
   buttons without the `privacy_dsr_transitions` table means the
   approval is invisible to a regulator. We need to land the schema
   AND the endpoints in the same PR.
2. **Workflow correctness**: A Temporal workflow that's already
   running can't be retroactively converted to an approval-gated
   one. The gate has to be inserted at the workflow definition
   level, which means a new workflow version. The handler can
   route old in-flight workflows to legacy behavior; new submits
   use the gate.

## Open questions

- **Export approval default**: ON or OFF? GDPR doesn't strictly
  require it for export (it's not destructive), but some tenants
  may want it. Defaulting OFF preserves current behavior; tenants
  opt in via the policy column. **Decision: default OFF.**
- **Reject reasons taxonomy**: free text vs. dropdown? Compliance
  auditors prefer structured taxonomy. **Decision: free text MVP,
  add dropdown in Phase 3.**
- **Approver delegation**: if the on-call compliance officer is
  unavailable, who approves? **Decision: any user with role
  `compliance_officer` OR `owner` can approve; "delegate to" is
  out of scope.**

## Migration / rollback

Forward: migration adds columns with safe defaults; the new submit
handler is feature-flagged by the per-tenant policy column. Tenants
with empty `require_approval_for` see no change. Set the policy →
new requests gate.

Rollback: clear the policy column for all tenants; new submits
revert to `pending → running` (auto-execute). In-flight
`awaiting_approval` requests can be cleaned up by admin endpoint
or DB UPDATE.

## What changes if you do nothing

Today's behavior: irreversible erase/anonymize fires on submit,
visible only after the fact. The Privacy admin page has data but
no actions. Any "test" submission to a real subject email
permanently deletes their data. This is a real GDPR compliance gap
for tenants in regulated industries.
