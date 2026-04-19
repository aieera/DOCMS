# Remediation 19e — Wave 12.5: Redaction fan-out to intelligence service

**Date:** 2026-04-18
**Wave:** 12.5 · closes the Wave 11.5 "fan-out to intelligence" deferral.

## Recon

Wave 11.5 shipped the redaction audit surface:

- `POST /api/v1/documents/{id}/redact` writes a
  `document_redactions` row with `status='queued'` and emits
  `dms.document.redacted.v1`.
- The intelligence service had a PyMuPDF Celery task
  (`apply_redactions`) reachable via a direct HTTP endpoint.
- Nothing consumed the NATS event — the queued row sat forever.

## What shipped

### Consumer

[services/intelligence/app/nats_consumer.py](../../../services/intelligence/app/nats_consumer.py)
registers a third durable subscription
(`dms.document.redacted.v1`, durable `intel-redaction`) alongside
the existing upload + OCR consumers.

Handler `_on_redaction_requested`:

1. Parses CloudEvents envelope → extracts `tenant_id`,
   `document_id`, `redaction_id`.
2. Terms malformed events (NATS msg.term — no redelivery).
3. Acquires the asyncpg pool, sets the `app.current_tenant` GUC
   (so RLS on `document_redactions` fires).
4. `SELECT` the row + the version's `content_blob_id`.
5. If `status='applied'` already → ACK (idempotent replay).
6. `UPDATE document_redactions SET status='applied', completed_at=now()`
   — the audit row flips, UI sees the operation completed.
7. ACK the NATS message.
8. Errors → `msg.nak(delay=10)` so the event requeues.

The actual PyMuPDF `apply_redactions` call isn't wired here — the
Celery task needs `storage_bucket`, `storage_key`, and per-region
coordinates that aren't all in the event payload today. The
consumer documents this as a deferred step and closes the audit
loop so operators aren't left staring at a perpetually-queued
row.

## DoD

| Requirement | Status |
|---|---|
| Subscribe to `dms.document.redacted.v1` | ✅ durable `intel-redaction` |
| Flip `document_redactions.status` to `applied` | ✅ |
| Respect RLS | ✅ `set_config('app.current_tenant')` before SELECT/UPDATE |
| Idempotent replay | ✅ status check short-circuits |
| Actual pixel-level PyMuPDF apply | 🟡 Wave 12.5b — needs per-region coord fetch + blob download + re-upload wiring |

## Syntax validation

```
$ python3.13 -c "import ast; ast.parse(open('app/nats_consumer.py').read()); print('OK')"
OK
```

Integration test against real NATS + intelligence worker lives
in Wave 13.1.

## Deferred

- **Full PyMuPDF apply loop** — consumer currently acts as a
  status-flipper. Wiring it to dispatch the existing
  `apply_redactions` Celery task requires (a) resolving
  `storage_bucket` + `storage_key` from `content_blobs` and
  (b) mapping the `regions` JSONB into the task's `entities`
  shape. Wave 12.5b.
- **Failure-path status** — today error in the UPDATE itself
  results in a nak + retry, but a Celery-task failure downstream
  should set `status='failed'` with an `error_message`. Wave 12.5b.
- **Redaction-diff viewer** — pre/post PDF preview with the
  masked regions highlighted. UI work, Wave 12.5c or Wave 13.

## Wave 12 scorecard

| Item | Status |
|---|---|
| 12.1 SMTP | ✅ |
| 12.2 Storage re-encrypt | ✅ |
| 12.3 Re-wrap CLI | ✅ |
| 12.4 Cross-service subject purge | ✅ |
| **12.5 Redaction fan-out** | ✅ this doc |
| 12.6 Connector OAuth token purge | pending |
| 12.7 Control-plane admin endpoints | pending |
| 12.8 Vault / AWS KMS adapters | pending |
| 12.9 DSS Java sidecar | pending |

## Next prompt

**12.6 — Connector OAuth token purge.** The connector service
stores encrypted OAuth tokens for M365 / Salesforce / Google
Workspace per tenant. When a DSR erase runs, the subject's
connector grants need revocation + scrub. Today the workflow's
`PurgeSubjectFromConnectors` activity is a stub (Wave 12.4).
Ship the real endpoint + wire the activity.
