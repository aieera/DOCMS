# 0024 — GDPR data-subject request (DSR) strategy

- **Status:** Accepted
- **Date:** 2026-04-17
- **Supersedes:** —
- **Deciders:** core eng + compliance

## Context

GDPR Articles 15 / 17 / 20 grant data subjects (1) the right to
access their data, (2) the right to erasure, and (3) the right to
data portability. SeDoc handles personally-identifying data
(document metadata, uploader identity, audit trails) across ≥14
services and three stateful stores (Postgres, Qdrant, object
storage). We need a single contract for how a DSR is expressed,
executed, audited, and reconciled with competing obligations
(legal hold, regulatory retention).

Key forces:

- **30-day SLA.** Blueprint spec §7.3 sets a configurable 30-day
  cap (GDPR default). We need the export / erase work to run
  asynchronously — a 30-day cap means we cannot treat DSR as a
  synchronous RPC.
- **Legal hold precedes erasure.** A document under an active
  legal hold may not be deleted regardless of the subject's
  request; GDPR Art. 17(3)(b/e) acknowledges "compliance with a
  legal obligation" and "establishment, exercise or defence of
  legal claims" as overriding grounds.
- **Audit trail integrity.** We must never delete audit events
  (§7.3: "Never delete audit trail entries; redact them in
  export"). The same applies to the privacy ledger itself.
- **Cross-service reach.** Erasing "everything about Alice" means
  touching rows the document service doesn't own. Today this is
  auth.users, document authorship, search.qdrant payloads, and
  potentially notification preferences — a long tail.
- **Verification.** Spec requires a `verification_token` on
  erase. We need an out-of-band proof that the erase request came
  from the real data subject (not a malicious co-tenant).

## Decision

### 1. Each DSR is a Temporal workflow, not an RPC

`POST /privacy/dsr/{export,erase,anonymize}` enqueues a row in
`privacy_dsr_requests` and kicks off one of `ExportWorkflow`,
`EraseWorkflow`, or `AnonymizeWorkflow`. The HTTP handler returns
the request id immediately; clients poll `GET /privacy/dsr/{id}`
for status.

Why Temporal: the 30-day SLA needs durable scheduling, retries,
and the ability to survive worker restarts. A cron or single-
process goroutine would lose state on redeploy.

### 2. Legal hold is a hard short-circuit for erase / anonymize

Before any PII overwrite, the workflow calls
`SubjectHasHeldDocuments(subject_email)`. If any document
authored by or attached to the subject is under an active legal
hold, the workflow exits with status `blocked` and emits
`dms.dsr.blocked.v1`. The privacy ledger records the block and
the hold IDs. Export proceeds regardless — read-only, not
destructive.

The compliance officer may resolve the block by releasing the
holds (Wave 8.2 workflow) and re-submitting the DSR. We do not
auto-retry.

### 3. Erase vs anonymize: two shapes, one code path

- **Erase** overwrites PII columns with `erased-<uuid>`. The
  subject row in `users` stays (tombstone), but name / email /
  phone / any free-text PII are scrubbed. Documents authored by
  the subject transition to `disposed` (via the retention
  lifecycle, re-using the `RetentionTransition` activity from
  Wave 8.1).
- **Anonymize** replaces the subject's identifiers with
  HMAC-SHA256(identifier, tenant_salt). Aggregate analytics
  continue to work (counts per subject hash still group
  correctly); the identifier itself is no longer reversible
  without the tenant salt.

Both variants share the same workflow skeleton; the variant flag
controls which activity (`OverwritePII` vs `HashPII`) runs.

### 4. Export packages a signed ZIP

`ExportWorkflow` walks every row where the subject appears
(documents, versions, audit events, workflow tasks) and writes a
JSON-per-service manifest plus the document binaries (where the
subject is author) into a ZIP. Upload path: tenant-scoped bucket
`vaultdms-dsr-<tenant>/export-<request_id>.zip`. Signed URL valid
for 7 days. URL + expiry land on `privacy_dsr_requests`.

Audit events are included but redacted: actor identifiers are
left in place (needed for the completeness guarantee) but free-
text `metadata` fields are passed through the same PII scrubber
that powers erase.

### 5. The privacy ledger is append-only, 7-year retention,
   tenant-scoped

Every DSR action — `requested`, `verified`, `completed`,
`blocked`, `error` — writes one row to `privacy_ledger`. The
ledger is **not** RLS-wrapped at the table level because the
compliance officer role reads across the ledger for regulator
response; application-level tenant filtering still applies.

Retention clock: 7 years from `created_at`. Enforcement lives
outside the Postgres schema — a quarterly SQL job (operator SoP,
not yet automated). Logged out-of-scope.

### 6. Verification token: generated on request, redeemed within 24h

Erase requests require a `verification_token` in the body. The
token is a random 32-byte hex string. Generation path (Wave 8.3
follow-up): the subject hits an as-yet-unbuilt
`POST /privacy/verify/request-token { email }` that emails them a
token via the notification service. Token redeemed once, expires
24h later, stored hashed in Redis.

For the initial implementation we accept any non-empty token
string and log a warning — the full verification loop ships with
Wave 11 when the notification service has a transactional email
path.

## Consequences

- **Easier.** A single Temporal workflow per request means
  retries, visibility, cancellation, and cross-service activity
  orchestration come free. The HTTP surface stays tiny.
- **Harder.** Every new service that stores PII must register an
  activity with the erase workflow; without that, erase is
  incomplete. We mitigate by a CI guard: any table whose name
  appears in a DSR activity's `touched_tables` must be listed in
  `docs/compliance/dsr-coverage.md`.
- **Harder.** The 30-day SLA exposes us if a stuck workflow sits
  un-alerted. We need a Wave 13.6 alert on "DSR running > 25
  days."
- **Accepted compromise.** Verification token is honor-system on
  day 1. This is called out in the admin UI and remediation doc.

## Alternatives considered

- **Synchronous RPC chain.** Pros: simple. Cons: no way to
  survive worker restart across a 30-day window; fails for any
  tenant with > 30s of work.
- **Per-service DSR endpoints.** Each service exposes its own
  export / erase. Pros: no orchestrator. Cons: client has to
  compose; no atomic "all services done" signal; audit-trail
  inconsistencies are invisible until someone looks.
- **Event-driven fan-out** (emit `dsr.requested.v1`, let every
  service subscribe). Pros: loose coupling. Cons: no
  completeness guarantee; a service that hasn't subscribed yet
  silently misses the request. Rejected.

## Sources

- GDPR Art. 15 / 17 / 20.
- RFC 4122 UUIDv7 (used for ledger ids).
- Spec §7.3 (blueprint).
- Wave 8.1 remediation (15a) — retention lifecycle reused here.
- Wave 8.2 remediation (15b) — hold binding table that the
  short-circuit query joins.
