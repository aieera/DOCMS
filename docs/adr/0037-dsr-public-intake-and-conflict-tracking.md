# ADR 0037 — DSR public intake, structured conflicts, and SLA enforcement

**Status:** Proposed · **Date:** 2026-04-26 · **Builds on:** ADR 0024 (GDPR DSR strategy, accepted 2026-04-17), Wave 11.4 admin DSR surface (`services/document/internal/handler/privacy_handler.go`).

## Context

ADR 0024 defined the DSR contract: each request is a Temporal workflow, legal hold short-circuits erasure, the privacy ledger is append-only with 7-year retention, verification tokens redeem within 24h. Wave 11.4 shipped the admin-facing surface — `POST /api/v1/privacy/dsr/{export,erase,anonymize}`, `GET /api/v1/privacy/dsr/{id}`, and the in-app token-paste flow.

Three gaps remain that the Wave 11.4 brief explicitly deferred:

1. **No subject-initiated path.** Today every DSR is admin-created. A data subject who wants to exercise their Art. 15/16/17/20 rights must email a human, who then logs into VaultDMS and creates the request on their behalf. GDPR Art. 12(2) says controllers must *facilitate* the exercise of subject rights — a friction-free public intake is the canonical implementation.
2. **Hold conflicts are unstructured.** When `SubjectHasHeldDocuments` short-circuits an erasure, the workflow writes a free-text string into `privacy_dsr_requests.blocked_reason` and emits `dms.dsr.blocked.v1`. There's no separate row a compliance officer can iterate, no structured `conflict_type`, no audit of *who* resolved what (extend hold? release? partial erase?). The compliance UI has to grep blob text.
3. **SLA breach is silent.** Migration 000011 added `due_at` (created_at + 30 days, GDPR-default), but no monitor watches it. If a request languishes, no one is paged until the subject complains to the regulator.

We do **not** need a new `services/dsr` module. The existing `privacy_dsr_requests` row + workflows handle 80% of the spec; the gaps are additive (one schema migration, one Temporal schedule, two new HTTP routes, one frontend refactor). ADR 0035 set the precedent — augment in place when the existing scaffolding is sound.

## Decision

### Schema (migration `000017_dsr_augmentation`)

Three additions, none breaking:

```sql
ALTER TABLE privacy_dsr_requests
    ADD COLUMN requester_identity_verified_at TIMESTAMPTZ,
    ADD COLUMN status_token_hash              BYTEA, -- SHA-256 of the public status token
    ADD COLUMN intake_source                  TEXT NOT NULL DEFAULT 'admin'
        CHECK (intake_source IN ('admin', 'public_form'));

CREATE TABLE dsr_request_artifacts (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    request_id     UUID NOT NULL,
    artifact_type  TEXT NOT NULL CHECK (artifact_type IN
        ('export_zip', 'rectification_diff', 'erasure_report')),
    storage_bucket TEXT NOT NULL,
    storage_key    TEXT NOT NULL,
    size_bytes     BIGINT,
    sha256_hash    TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, request_id) REFERENCES privacy_dsr_requests(tenant_id, id)
);

CREATE TABLE dsr_conflicts (
    tenant_id            UUID NOT NULL REFERENCES organizations(id),
    id                   UUID NOT NULL DEFAULT gen_random_uuid(),
    request_id           UUID NOT NULL,
    conflict_type        TEXT NOT NULL CHECK (conflict_type IN
        ('legal_hold', 'retention_conflict', 'multi_tenant')),
    -- Free-form structured detail; the conflict_type narrows what keys the
    -- consumer expects (legal_hold → hold_id + matter_name; retention_conflict
    -- → policy_id + retain_days_remaining; multi_tenant → other tenant ids).
    conflict_details     JSONB NOT NULL,
    resolved_by          UUID,
    resolved_at          TIMESTAMPTZ,
    resolution_action    TEXT, -- e.g. 'partial_erase', 'release_hold', 'reject_request'
    resolution_notes     TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, request_id) REFERENCES privacy_dsr_requests(tenant_id, id)
);
CREATE INDEX idx_dsr_conflicts_unresolved
    ON dsr_conflicts(tenant_id, request_id) WHERE resolved_at IS NULL;
```

The existing `privacy_dsr_requests.blocked_reason` text stays — `dsr_conflicts` is structured-on-top, not a replacement. A workflow that hits a hold writes BOTH (blocked_reason for human-readable summary, dsr_conflicts row for the resolution UI).

### Public intake (`POST /api/v1/dsr/intake`)

Unauthenticated, rate-limited at the gateway (5 requests / IP / hour, plus 1 / email / hour). Body:

```json
{
  "tenant_slug": "acme",
  "requester_email": "data-subject@example.com",
  "request_type": "access" | "rectification" | "erasure" | "portability",
  "description": "free-text"
}
```

Flow:

1. Resolve `tenant_slug` → tenant_id via the existing tenant-slug index (used by SAML SSO).
2. Insert into `privacy_dsr_requests` with `intake_source='public_form'`, `status='pending'`, `requested_by=NULL` (no internal user), `requester_identity_verified_at=NULL`.
3. Mint a 32-byte status token; store SHA-256 in `status_token_hash`. The plaintext token goes only into the verification email — once.
4. Emit `dms.notify.dsr_intake.v1` with the verification link `https://{tenant_host}/dsr-status?token={plaintext}`. The notification service's existing email path delivers it.
5. Return `{ request_id, expires_at }` with a generic accepted-202. Don't echo the token; the subject must check email.

Design note: we deliberately **do not** confirm whether the email exists in the system at intake time. Doing so would leak a user-existence oracle to anyone who can hit the public endpoint. The ResolveSubject activity inside the eventual workflow handles "no such email" gracefully.

### Status + verification (`GET/POST /api/v1/dsr/status`)

`GET /api/v1/dsr/status?token={token}`:
- Hash the token, look up the matching `privacy_dsr_requests` row (tenant-scoped via the resolved status_token_hash).
- Return `{ status, request_type, created_at, due_at, completed_at, artifact_url? }`. Artifact_url is a presigned S3 GET, only populated when status='completed' and the row has a `dsr_request_artifacts(artifact_type='export_zip')` child. Expires in 24h.

`POST /api/v1/dsr/status/verify { token }`:
- Same hash lookup. Stamps `requester_identity_verified_at = now()`. Idempotent. Required before erasure/rectification proceeds (the workflow checks). Access/portability can proceed without verification (they're read-only and the link itself is already a possession proof).

### SLA monitor (Temporal schedule `DsrSlaMonitor`)

Hourly cron. For each tenant:

1. SELECT requests where `due_at < now() + interval '12 hours'` AND `status NOT IN ('completed', 'failed')`.
2. For each, emit `dms.notify.dsr_sla_warning.v1` (DeliveryPayload, recipient = compliance_officer + owner role members). Once per request per 12h window — track via a small `dsr_sla_notifications(request_id, notified_at)` table or via a `last_sla_notified_at` column on the request (we'll go with the latter; one less table).
3. Past-deadline (due_at < now()): emit `dms.notify.dsr_sla_breach.v1` and push a `severity=critical` alertmanager event via the existing prom rule wiring.

The schedule lives in `services/workflow` alongside the other tenant-scheduled workflows. ADR 0023's namespace strategy applies: one schedule per tenant, naming `dsr-sla-{tenant_id}`.

### What we did not do

- **No new `services/dsr/` module.** The existing privacy_handler + workflows + ledger handle 80% of the spec. Splitting would mean migrating `privacy_dsr_requests` rows out and rewriting the admin UI for zero behavior change.
- **No replacement of `blocked_reason`.** Free-text human summary stays for the privacy ledger and the existing admin list view. `dsr_conflicts` is the structured layer on top.
- **No "instant rectification" path.** Rectification requests still go through the admin profile-edit UI per ADR 0024. The public intake form merely files the request; an admin executes it.
- **No automatic legal-hold release on conflict.** A `dsr_conflicts.resolution_action='release_hold'` outcome requires an explicit compliance_officer action via the existing legal-hold release endpoint. The DSR resolver records *that* the resolution happened; it doesn't perform the release itself. Cross-feature coupling here would create an erasure-via-DSR backdoor around the legal hold gate.
- **No SCIM-driven rectification fan-out.** Cross-service propagation of `user.updated` is the existing ADR 0024 path (Wave 11.4 outbox). No new hooks needed for rectification.
- **No CAPTCHA on the public form.** Rate limit + per-email cap + email magic-link is enough abuse mitigation for v1. CAPTCHA is a Wave 14 follow-up if abuse appears.

## Consequences

**Positive**
- Subject-initiated requests work without a human admin in the loop, satisfying GDPR Art. 12(2) "facilitation".
- Conflicts have a resolution surface — compliance officers see structured rows, can act on them, and the audit trail records the decision.
- SLA breaches are visible *before* they breach. 12h warning + breach notification is the minimum viable for a 30-day cap.
- Zero churn for already-shipped admin-side work. Existing DSR rows, the verification token Redis flow (now optionally bypassed by the public-form magic link), and the three Temporal workflows are unchanged.

**Negative**
- Two parallel verification mechanisms for a transition period: the existing `dsr_verify` (admin/in-app paste) and the public-form magic link (`status_token_hash`). They serve different callers; documented in the runbook. Long-term we may consolidate, but doing so now means breaking the admin flow for zero gain.
- `dsr_conflicts` is currently only populated for legal-hold conflicts. The other two `conflict_type` values (`retention_conflict`, `multi_tenant`) are reserved schema; the workflows that detect them ship in a follow-up. Schema accommodates so no second migration.
- Public unauthenticated endpoint expands the attack surface. Mitigations: gateway rate limit, per-email cap, no user-existence oracle in responses, generic 202s. Threat-model review documented in the runbook.

## References

- ADR 0024 GDPR DSR strategy
- ADR 0035 legal hold §9.3 closure (augment-in-place precedent)
- Wave 11.4 admin DSR surface — [`services/document/internal/handler/privacy_handler.go`](../../services/document/internal/handler/privacy_handler.go)
- Wave 11.4 in-app verification — [`services/document/internal/handler/dsr_verify.go`](../../services/document/internal/handler/dsr_verify.go)
- Blueprint §9.2 Data Subject Rights
