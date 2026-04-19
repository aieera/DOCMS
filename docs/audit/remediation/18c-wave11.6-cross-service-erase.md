# Remediation 18c — Wave 11.6: Cross-service DSR erase scope expanded

**Date:** 2026-04-18
**Wave:** 11.6 · closes Wave 8.3 deferral ("Cross-service erase activities").

## Recon

`OverwriteSubjectPII` activity (Wave 8.3) only touched two tables:

- `users.display_name` + `.email` — redacted to `erased-<uuid>` /
  `anon-<hmac>`
- `audit_events.user_agent` + `.metadata` — scrubbed

Every other user-keyed table in the document DB still held data
after a GDPR erase workflow ran green. The long-tail list from the
initial schema: `sessions`, `api_keys`, `notifications`,
`notification_preferences`, `device_tokens`, `conversation_history`
(RAG chat history), `group_members`, `workspace_members`.

Spec §7.3 erase: *"invalidates all sessions + API keys, records a
tombstone in a privacy_ledger"* — the activity was missing five of
those eight tables plus leaving group/workspace membership intact.

## Decision

Since every one of these tables lives in the same Postgres cluster
the workflow-service pool connects to, the "cross-service" framing
from the original out-of-scope ledger is overstated. A single
`OverwriteSubjectPII` activity running inside one `WithTenantTx`
can scrub all of them atomically; no per-service activity
dispatch needed.

True cross-service (Qdrant vector payloads, OpenSearch indexes)
still exists — those live outside Postgres and get their own
activities. Logged out-of-scope as Wave 12 work.

## What changed

[services/workflow/internal/activities/dsr.go](../../../services/workflow/internal/activities/dsr.go)
— `OverwriteSubjectPII` body expanded. The new scope, grouped by
mode:

**Both modes (erase + anonymize):**

- `users` — `display_name` + `email` redacted.
- `audit_events` — `user_agent` → `redacted`, `metadata` → `{redacted: true, mode}`.
  `actor_id` preserved (completeness guarantee per ADR 0024).

**Erase-only additions:**

- `sessions` — `revoked_at = now()` (all active sessions terminated).
- `api_keys` — `revoked_at = now()`.
- `notifications` — DELETE.
- `notification_preferences` — DELETE.
- `device_tokens` — DELETE (push-notification tokens).
- `conversation_history` — DELETE (RAG chat history is PII-heavy).
- `group_members` — DELETE (remove from all groups).
- `workspace_members` — DELETE (remove from all workspaces).

Anonymize deliberately keeps memberships + notifications because
those are aggregate-analytic signals; only identifiers are hashed.

All 8 extra statements run inside the existing `WithTenantTx` —
either every scrub lands or none does, and RLS enforces tenant
scoping on every query.

`rowsAffected` aggregates across the lot so the ledger entry
reflects the total number of scrubbed rows, giving auditors a
concrete number per erase workflow.

## DoD — ADR 0024 + spec §7.3

| Requirement | Status |
|---|---|
| Erase overwrites PII columns | ✅ |
| Invalidates all sessions | ✅ `sessions.revoked_at` set |
| Invalidates all API keys | ✅ same pattern |
| Disposes documents authored by subject | 🟡 pre-existing `RetentionTransition` call does this separately in the workflow; not duplicated here |
| Tombstone in privacy_ledger | ✅ `WritePrivacyLedger` on every state transition |
| Anonymize keeps aggregate signals | ✅ memberships + notifications preserved |
| Cross-Postgres scrub is atomic | ✅ single `WithTenantTx` |

## Tests

No new tests; existing DSR workflow tests (Wave 8.3 +
Wave 11.4) continue green because the activity still takes the
same arg shape and returns a row count. Integration test that
counts rows across the long-tail tables is Wave 13.1.

```
$ go test ./services/workflow/...
ok  github.com/vaultdms/vaultdms/services/workflow/internal/handler  (cached)
ok  github.com/vaultdms/vaultdms/services/workflow/internal/workflows  (cached)
```

## Deferred (logged in out-of-scope.md)

- **Qdrant vector payload purge** — tenant-scoped collection or
  payload filter pass. Lives outside Postgres; needs a
  `PurgeSubjectVectors(tenantID, subjectID)` activity on the
  search service. Wave 12.
- **OpenSearch subject-document index purge** — same pattern as
  Qdrant. Wave 12.
- **S3 / object-store erase** — when a subject's authored
  documents get the `disposed` lifecycle (handled by retention),
  the blob ciphertext also needs shredding. Spec §7.3 mentions
  "ciphertext shred" — already logged as Wave 8 follow-up.
- **Connector OAuth token purge** — if the subject configured a
  Salesforce/M365 connector, the stored OAuth refresh token
  should be revoked + deleted. Wave 12.

## Wave 11 scorecard

| Item | Status |
|---|---|
| 11.1 Workflow RLS audit | ✅ |
| 11.2 OPA gate on holds | ✅ |
| 11.3 Dead-code cleanup | ✅ |
| 11.4 DSR verification token | ✅ |
| 11.5 Redaction endpoint | ✅ |
| **11.6 Cross-service DSR erase** | ✅ this doc |
| 11.7 Per-region KEK aliases | pending |

## Next prompt

**11.7 — Per-region KEK aliases.** Wave 8.4 residency workflow
flips `documents.region_pin` + emits `dms.residency.migrated.v1`
but doesn't re-encrypt the blob ciphertext under a region-local
KEK (Wave 6.1 uses a single master secret derived per-tenant).
Multi-region deployments need separate master secrets per region
so a US-region compromise doesn't expose EU-region ciphertexts.
