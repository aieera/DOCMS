# DSR Response Runbook

**Owners:** compliance officer (primary), platform on-call (escalation).
**SLA:** 30 days from receipt to completion. Warning at 12h pre-deadline; breach alert at deadline+0.

This runbook covers the GDPR Data Subject Request flow: how a request enters the system, how to triage it, how to resolve conflicts, and what to do when the SLA is at risk.

## Architecture overview

Two intake paths land in the same `privacy_dsr_requests` table:

```
Subject               Public form              Admin
                  (/dsr-request)          (/admin/privacy)
                          │                      │
                          ▼                      ▼
                  POST /dsr/intake        POST /privacy/dsr/{type}
                  (token via email)       (token via in-app paste)
                          │                      │
                          └──────────┬───────────┘
                                     ▼
                       privacy_dsr_requests row
                                     │
                                     ▼
                       Temporal: ExportWorkflow |
                                 EraseWorkflow  |
                                 AnonymizeWorkflow
                                     │
                          ┌──────────┼─────────────┐
                          ▼          ▼             ▼
                     completed    blocked         failed
                                     │
                                     ▼
                         dsr_conflicts rows (one per active hold)
                                     │
                                     ▼
                       Compliance officer resolves in
                            /admin/privacy detail panel
```

## Where to look

| Need | Location |
|---|---|
| All DSRs for the tenant | `/admin/privacy` (kanban) |
| Single DSR detail + conflicts | Click the card |
| Full audit trail | `privacy_ledger` table (append-only, 7y retention) |
| Cross-service erasure events | `dms.dsr.requested.v1` / `dms.dsr.completed.v1` / `dms.dsr.blocked.v1` / `dms.dsr.failed.v1` in audit |
| SLA notifications | `dms.notify.dsr_sla_warning.v1` / `dms.notify.dsr_sla_breach.v1` |
| Public-form intake notifications | `dms.notify.dsr_intake.v1` (carries plaintext token in payload) |

## Triage flow

### 1. New request lands in "Pending verification"

**Public-form requests:** the kanban card shows an `unverified` badge until the subject clicks the link in their verification email. They have 7 days. After verification stamps `requester_identity_verified_at`, the card loses the badge but stays in "Pending verification" until the workflow picks it up.

**Admin-created requests:** the admin must request a token via the "Request token" button, paste it back into the form, and submit. Token is single-use and 24h.

**What to do:** nothing. Wait for verification, then the workflow auto-advances.

**When to intervene:** subject says they didn't get the email. Check `notification_deliveries` for the request_id; if delivery failed, regenerate by canceling + re-creating the request (the original token is invalidated).

### 2. Card moves to "In progress"

Workflow is running. Read-only — no action.

### 3. Card moves to "Completed" / "Conflict / rejected"

**Completed:** the artifact link is in the subject's email, valid 24h. If they need a fresh link, regenerate by re-running the workflow (export is idempotent).

**Conflict:** the kanban card shows a red conflict badge with the count. Click into the detail; the conflict panel lists each `dsr_conflicts` row.

## Conflict resolution

When erasure / anonymisation hits an active legal hold, `EraseWorkflow` writes:

1. `privacy_dsr_requests.status = 'blocked'` with a free-text `blocked_reason`.
2. One `dsr_conflicts` row per matching hold, with `conflict_type='legal_hold'` and structured details: `{hold_id, matter_name, document_count, subject_id}`.

The compliance officer iterates rows in the kanban detail panel. Each row has four resolution actions:

| Action | What it records | What you must do separately |
|---|---|---|
| `partial_erase` | Officer accepts that some data stays under hold; rest will be erased on a follow-up request | Submit a new erase request with hold-excluded scope (manual until partial-scope DSRs ship) |
| `release_hold` | Officer believes the hold is no longer required | Go to `/admin/legal-holds`, release the hold there. Resolution_action is logged-only — does NOT release the hold. |
| `reject_request` | Hold takes precedence; the subject's right is overridden by the legal obligation | Notify the subject within the SLA window with the GDPR Art. 17(3) basis cited |
| `other` | Any other resolution path; use `notes` to describe | As needed |

**Important:** the `release_hold` value in DSR conflict resolution is *audit-only*. It does not actually release any legal hold. The release must go through the legal-hold release endpoint (`/api/v1/compliance/holds/{id}/release`), which has its own role gate and audit chain. This split is deliberate — coupling them would create a DSR-driven backdoor around the legal hold gate.

After resolution, the request stays in "Conflict / rejected" — the workflow does not auto-resume. To complete the request:

- For `partial_erase`: re-submit the erase request (manual today; partial-scope DSRs are a roadmap item).
- For `release_hold`: after releasing the hold, re-submit the erase request. The next run will not hit the conflict.
- For `reject_request` / `other`: the request is terminal; respond to the subject within SLA.

## SLA enforcement

The `dsr-sla` CronJob runs hourly and emits two notification classes:

- `dms.notify.dsr_sla_warning.v1` — request within 12h of `due_at`, not in completed/failed/blocked. Fires once per request per 12h window. Recipients: compliance_officer + owner role members.
- `dms.notify.dsr_sla_breach.v1` — `due_at < now()`. Same recipients + alertmanager pages on-call.

**Why both:** GDPR's 30-day cap is hard. A breach without warning is a regulatory event; a warning at 12h gives compliance one shift to escalate.

**If a tenant has no compliance_officer or owner:** the cron stamps `last_sla_notified_at` anyway to avoid hot-looping the scan, but no one is notified. Audit log records the silent stamp; this is a tenant-staffing problem, not a system bug.

### When the breach alert fires

1. Open the kanban; filter or eyeball "Pending verification" + "In progress" for cards in red.
2. For each: open the detail.
   - **Pending verification, public form, unverified:** subject never clicked the link. Either wait (the request will auto-expire and roll off) or contact them via secondary channel if you have one.
   - **In progress:** check the workflow run via the workflow service `/admin/platform/workflows`. Workflow stuck → file an incident; workflow advancing slowly → wait or extend the deadline manually (`UPDATE privacy_dsr_requests SET due_at = now() + interval 'N days'` — recorded in the audit ledger via the privacy_ledger row written at next state transition).
   - **Conflict / rejected:** if conflict is unresolved, escalate to the compliance officer immediately. If terminal, the breach is a notification mismatch — respond to the subject and close.

## Identifying the subject

Every workflow runs `ResolveSubject(email) → user_id`. If the subject doesn't exist, the workflow records `completed-absent` and exits. **No data leaves our system in that case** — the export is empty, no erasure runs.

If the subject's email differs from what's stored (typo, married name, etc.), the request will be `completed-absent` despite the subject having data. The compliance officer must:

1. Manually verify identity via secondary channel.
2. Find the matching email in the `users` table.
3. Submit a new admin-created DSR with the correct email.

This is rare but reasonable for old accounts; documented for clarity.

## Common failure modes

| Symptom | Likely cause | Fix |
|---|---|---|
| Request stays "Pending verification" forever | Notification service can't reach SMTP / subject email bounced | Check `notification_deliveries` for the row; if `failed`, fix SMTP and ask subject to re-submit |
| `dms.dsr.failed.v1` with phase=`verify` | Token expired (24h) or mistyped | Subject re-submits, gets a fresh token |
| Workflow runs but exports nothing | Subject email doesn't match any user | Identity-verification path above |
| Conflict count growing on a request | New hold landed mid-flight (e.g., M&A diligence) | Wait for hold review; resolve each conflict row in detail panel |
| SLA breach notification didn't fire | `last_sla_notified_at` was stamped but no users in tenant had `compliance_officer` / `owner` role | Add at least one user to one of those roles; future scans will resume |
| Public form returns 202 but no email sent | Tenant slug doesn't exist OR subject email truly absent | Cannot disambiguate intentionally — see ADR 0037 §"What we did not do" oracle defense |

## Manual operations

### Cancel a pending request

```sql
UPDATE privacy_dsr_requests
   SET status = 'failed', blocked_reason = 'manual cancellation: <reason>',
       completed_at = now()
 WHERE tenant_id = $1 AND id = $2 AND status IN ('pending', 'running');
```

Also write a `privacy_ledger` row recording the cancellation:

```sql
INSERT INTO privacy_ledger (tenant_id, request_id, subject_email, action, outcome, details, actor_id)
SELECT tenant_id, id, subject_email, 'manual', 'cancelled',
       jsonb_build_object('reason', '<reason>'), '<your_user_id>'
  FROM privacy_dsr_requests WHERE tenant_id = $1 AND id = $2;
```

### Extend the SLA deadline

Document the reason. Auditors may question this.

```sql
UPDATE privacy_dsr_requests
   SET due_at = $3, last_sla_notified_at = NULL
 WHERE tenant_id = $1 AND id = $2;
```

Resetting `last_sla_notified_at` lets the SLA monitor re-evaluate against the new deadline.

### Re-trigger a stuck workflow

Workflow IDs follow `dsr-{request_id}`. Use the Temporal UI or `dms-admin dsr redispatch <request_id>` (Wave 12+).

## References

- ADR 0024 — GDPR DSR strategy (foundational)
- ADR 0037 — DSR public intake + conflicts + SLA (this work)
- Migration 000006 — `privacy_dsr_requests` + `privacy_ledger` schema
- Migration 000011 — `due_at` SLA column
- Migration 000017 — `dsr_conflicts` + artifacts + public intake columns
