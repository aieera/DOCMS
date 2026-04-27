# ADR 0035 — Legal hold §9.3 closure (custodians, hash chain, multi-target)

**Status:** Accepted · **Date:** 2026-04-26 · **Builds on:** Wave 8.2 legal-hold scaffolding (`services/document/internal/compliance/holds.go`).

## Context

The Wave 8.2 scaffolding shipped a working `HoldsService` with `legal_holds` + `legal_hold_documents` tables, a Compliance handler with five HTTP routes, role-gated apply/release, `KindLegalHold→423 Locked` enforcement, and an admin UI at `/admin/legal-holds`. The Blueprint §9.3 brief asked for three additional primitives that the Wave 8.2 implementation deliberately deferred:

1. **Custodian list per hold** with notify + acknowledge timestamps.
2. **Hash-chained per-hold event log** for legal admissibility (existing events emit through the generic outbox; a court asks for "show me only the events for this matter, prove nobody tampered with the trail").
3. **Multi-target holds** beyond the document binding — folders, workspaces, and saved-search queries.

A naïve read of the brief said "build a new `services/legalhold/` module". A new module would have meant deleting working code, migrating the `legal_holds` rows, and rewriting the admin UI — all for zero new capability. We chose to **augment in place** instead.

## Decision

**Add three tables, one column, and seven HTTP endpoints to the existing compliance package. Do not introduce a new service.** Migration `000015_legal_hold_v2` is the schema delta:

- `legal_hold_custodians (tenant_id, id, hold_id, user_id, notified_at, acknowledged_at, created_at)` — explicit custodian list with acknowledgement timestamps.
- `legal_hold_targets (tenant_id, id, hold_id, target_type, target_id, search_query_json, applied_at)` — non-document targets. The `target_type='saved_search'` row carries `search_query_json` (the auto-include worker is deferred — see follow-ups).
- `legal_hold_events (tenant_id, id, hold_id, sequence, event_type, actor_id, payload, prev_hash, self_hash, occurred_at)` — append-only hash chain mirroring `acknowledgement_events` (Wave 15.1).
- `documents.hold_count INTEGER NOT NULL DEFAULT 0` — counter alongside the existing `under_legal_hold BOOLEAN`. The boolean stays as the fast-path index for delete/erase enforcement; the counter composes overlapping holds (folder-hold + doc-hold = 2; releasing one keeps the boolean true).

### Why the chain lives here, not in audit

The audit service ingests *every* tenant event via `dms.audit.>` and produces a tenant-wide hash chain. That chain answers "did anything change since timestamp T". For litigation we need a different question: "show me only the events for matter X, in order, with cryptographic proof that nothing was inserted, deleted, or reordered." The per-hold chain in `legal_hold_events` answers that without forcing the court to walk a billion-row tenant chain.

The chain is keyed by a per-tenant HMAC secret (`VAULTDMS_LEGAL_HOLD_HMAC_SECRET` envelope, derived per-tenant via SHA-256 → not exposed). Even an attacker with full DB access cannot rewrite a single event without invalidating the chain from that point forward. `GET /verify-chain` re-walks the table and surfaces the first broken `sequence` number.

### What we did not do

- **No new `services/legalhold/` module.** The cost of moving the working `HoldsService` out of `services/document/internal/compliance/` would have been days of plumbing for zero behavior change. The brief's literal-read was rejected.
- **No `documents.hold_count` trigger.** Increment/decrement happens in service code inside the tenant tx. A trigger would block PATCHes that touch unrelated columns and is harder to reason about during incident response.
- **No saved-search auto-include worker.** The schema accepts `target_type='saved_search'` rows so the API contract is stable, but the worker that fans newly-matching documents into the hold is wave-sized (touches search service + Temporal). Tracked as a follow-up.
- **No Temporal `LegalHoldReviewReminder`.** Monthly reminder workflow would re-poll active holds and ping the owner. Wave-sized; can ship without changing schema or HTTP contract.
- **No OPA gate migration.** Existing `compliance_officer | admin | owner` role check is still header-based. Migration to `legal_hold.apply / release / view_all` capability codes via `policy.Check` is tracked in the ledger as Wave 11 deferral — unchanged.

## Consequences

**Positive**
- Zero churn for already-shipped functionality. `/admin/legal-holds`, `TestDeleteDocument_BlockedByLegalHold`, the existing `dms.hold.applied/released/updated.v1` events, and the 423 Locked enforcement all keep working.
- Custodians + chain ship as additive endpoints under the same `/api/v1/compliance/holds/...` prefix. No new Kong routes, no new Vite proxy entries.
- Hash chain reuses the Wave 15.1 `acknowledgement_events` pattern verbatim — same HMAC algorithm, same verify-chain-walks-the-table approach. Operators who learned one understand the other.

**Negative**
- Two columns track the same logical state: `under_legal_hold` (boolean, fast index) and `hold_count` (integer, composes overlapping holds). Keeping them in sync is a service-code responsibility. A future cleanup could drop the boolean once every read path migrates to `hold_count > 0`.
- The chain's HMAC secret lives in env, not Vault. Production rollout will need a per-tenant KEK-derived secret + rotation runbook (mirror what Wave 11.7 did for KEKs).
- Custodian-only acknowledgement (the handler refuses if `caller_id != target_user_id`) means there's no "ack on behalf of" path for incapacitated custodians. Legal teams handling that case today fall back to compliance-officer release + new hold; documented in the runbook.

## References

- Blueprint §9.3 — legal hold lifecycle
- ADR 0024 — DSR strategy (held-document short-circuit)
- ADR 0027 — acknowledgement campaigns (hash-chain pattern)
- `services/document/internal/compliance/{holds,custodians,events}.go`
- `services/document/migrations/000015_legal_hold_v2.up.sql`
- `docs/runbooks/legal-hold-workflow.md`
