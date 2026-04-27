# Runbook — legal hold workflow

**Audience:** compliance officers + on-call. **Triggers:**

- E-discovery request lands ("preserve everything related to matter X")
- Subpoena or litigation hold notice received from legal
- Custodian asks "am I on a hold?" → `/legal-holds/my`
- Audit anomaly alert: `legal_hold_chain_verifications_total{outcome=broken}` non-zero

## Lifecycle at a glance

```
   apply       ┌─→ custodians notify  ─→ acknowledge
hold ─────────►│
               └─→ targets bind doc/folder/workspace/saved-search
                                           │
                                           ▼
                                    delete/erase blocked (423 Locked)
                                           │
                                           ▼
release  ──────► custodians notify (released)
                 ├─→ documents.hold_count -= 1 per binding
                 └─→ legal_hold_events: chained event #N (released)
```

## Apply a hold

```
POST /api/v1/compliance/holds
{
  "name": "Smith v Acme — preservation",
  "description": "All correspondence + contracts re. project Phoenix",
  "matter_reference": "MATTER-2026-00042",
  "document_ids": ["..."]
}
```

Bind custodians:
```
POST /api/v1/compliance/holds/{id}/custodians
{ "user_ids": ["alice", "bob"] }
```

Each custodian receives `dms.notify.legalhold.applied.v1`. They appear in `/legal-holds/my` and click **Acknowledge**.

## Release a hold

Owner-role only. Reason is required.

```
POST /api/v1/compliance/holds/{id}/release
{ "reason": "Matter dismissed; legal sign-off attached", "approver_id": "..." }
```

After release: every bound document's `hold_count` decrements; when zero, `under_legal_hold=false` and erasure/disposal proceed.

## Verify the chain

```
GET /api/v1/compliance/holds/{id}/verify-chain
→ 200 { "ok": true, "event_count": 47 }
```

Broken chain:
```
{ "ok": false, "event_count": 47, "broken_at": 23, "message": "self_hash mismatch" }
```

If broken: do NOT release the hold or modify the table. Page security, capture the row at `sequence=23`, escalate to legal.

## Common questions

- **Custodian left the company.** They can't ack — SSO disabled = no session. Compliance officer documents in the external matter system and proceeds. The hold doesn't require all custodians to ack to be valid.
- **Saved-search holds + new matching docs.** Schema accepts the target type; auto-include worker is a deferred follow-up (ADR 0035). Until then, PATCH the hold periodically.
- **Active hold from years ago.** `LegalHoldReviewReminder` Temporal workflow is deferred. Run the audit query:
  ```sql
  SELECT id, name, matter_reference, applied_at FROM legal_holds
  WHERE tenant_id = $1 AND is_active = TRUE AND applied_at < now() - interval '90 days';
  ```

## Metrics

- `legal_holds_active` — should not grow unboundedly.
- `legal_hold_chain_verifications_total{outcome}` — broken count must stay zero.
- `legal_hold_custodian_ack_lag_seconds` — alerts on stale notifications.

## See also

- ADR 0035 — design rationale.
- ADR 0024 — DSR strategy; legal hold short-circuits GDPR erasure.
- `docs/runbooks/08-retention.md` — retention vs hold ordering.
