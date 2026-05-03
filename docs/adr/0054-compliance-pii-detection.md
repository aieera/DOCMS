# 0054 — Compliance scanning: PII/PHI detection

- **Status:** Accepted
- **Date:** 2026-05-03
- **Supersedes:** —
- **Deciders:** core eng + intelligence + compliance

## Context

Existing pipeline already extracts entities via `app.tasks.ner` —
ORG, PERSON, LOCATION, DATE, etc. None of those are flagged as
*sensitive*. But the same pipeline run, plus a small regex pass over
the raw OCR text, can identify SSN / credit-card / DOB / medical-
record-number with strong precision. The compliance team needs this
as a daily-driver dashboard, not a quarterly audit script.

Two design pressures:

1. **PII values themselves are the most-sensitive data on the
   platform.** Logging them in plaintext during a scan would be a
   self-inflicted breach. Storing them at rest demands envelope
   encryption identical to `content_blobs`.
2. **PHI is jurisdiction-gated** — HIPAA in the US has very specific
   "what counts as PHI" rules. Surfacing PHI for a tenant without a
   BAA in place is a compliance violation in itself. Default-off,
   per-tenant opt-in.

## Decision

A new Celery task `app.tasks.compliance_scan`, chained from
`dms.ner.completed.v1` with its own NATS durable. Two persistence
shapes plus one config table:

| Table                | Purpose                                                                     |
|----------------------|-----------------------------------------------------------------------------|
| `compliance_findings`| One row per (document, version, entity_type, source). Pending rows unique.  |
| `compliance_summary` | One row per document (UPSERT on every scan). Drives the badge + dashboard.  |
| `compliance_config`  | Per-tenant: enable, auto_hold, notification roles, risk overrides, custom patterns, **phi_enabled**. |

### Risk classification

Static maps in the task body:

```
PII_RISK_MAP    SSN, CREDIT_CARD, BANK_ACCOUNT, PASSPORT  → critical
                TAX_ID, DRIVER_LICENSE, DOB               → high
                EMAIL, PHONE, ADDRESS                     → medium
                IP_ADDRESS                                → low

PHI_RISK_MAP    MEDICAL_RECORD, DIAGNOSIS, LAB_RESULT,
                BIOMETRIC                                 → critical
                MEDICATION, PROCEDURE, INSURANCE_ID       → high
                HEALTH_PLAN                               → medium
```

Tenants can override per-entity risk via
`compliance_config.pii_entity_risk_overrides` (JSON map of
`{entity_type: risk_level}`). Custom regex patterns add new entity
types entirely — written as JSON array of `{type, regex, risk}`
objects.

### Detection sources

  * **NER** — entities from `entities` table; map by `entity_type`.
  * **pattern** — built-in regex pass over OCR text for entities NER
    is bad at (SSN, credit card, IP). Position-overlap dedup against
    NER findings to avoid double-counting.
  * **custom** — tenant-defined regex patterns from
    `custom_patterns`.

`detection_source` is recorded on each finding; the partial unique
index `(tenant, version, entity_type, detection_source) WHERE open`
collapses re-deliveries within a source without losing
multi-source evidence.

### Sample context, not values

`sample_context` stores ~80 chars of surrounding text **with the
matched value redacted to ▓** (e.g. `"Employee ▓▓▓-▓▓-▓▓▓▓ was
processed on..."`). This gives reviewers the context to verify a
finding is genuine without exposing the value in plaintext anywhere
the row is read.

### Encrypted values column

`compliance_findings.encrypted_values BYTEA` is the cipher store
for the actual matched values list. **Stays NULL in v1.** The
intelligence service today doesn't have access to the per-tenant
KEK (`pkg/crypto` is Go-only). v2 lands when we expose a thin
internal `POST /internal/v1/crypto/wrap` on the storage service.
Until then, finding consumers (admin dashboard, compliance officer
review) see the redacted sample but not the original value, which
is operationally fine — the document itself is the source of truth.

### Auto-hold flag

When `compliance_config.auto_hold_on_critical = true` and a critical
finding is recorded, the summary row sets `auto_held = true`. **The
intelligence task does not actually place the legal hold** — it
records the recommendation. Placing a hold is a domain transition
owned by the document service (see ADR — `LegalHolds.Place` is
gated by `requirePermission(admin)`, emits audit, etc.). The admin
dashboard surfaces auto_held rows so a compliance officer can
explicitly apply the hold via the existing legal-hold UI.

This is the same conservative pattern as ADR 0053 (smart routing
never auto-moves). Auto-anything that mutates document state is
deferred until we have a vetted internal API.

### Notifications

When `notify_on_high = true` and the scan produces a critical or
high finding, the task emits `dms.notification.send.v1` to the
outbox carrying `notify_roles` from config. The notification
service already subscribes to that subject; no new infra.

### Tenant isolation

  * RLS + FORCE on all three tables.
  * Every connection sets `set_config('app.current_tenant', $1)`
    before the first read or write.
  * Composite FKs prevent cross-tenant document/version references.
  * No PII value ever travels through a log line in this codebase
    — the structured logger emits `entity_type`, `count`,
    `risk_level` only.

## Consequences

  * The dashboard's "Documents with critical findings" count is the
    operational signal compliance officers will live in. Bandwidth
    on the admin endpoint matters; the partial unique index on
    `compliance_findings WHERE open` keeps it under control.
  * Compliance state is per-version: re-uploading a redacted version
    triggers a fresh scan; the previous version's findings remain
    in the table for audit but `compliance_summary.version_id`
    advances.
  * Disabling PHI scanning deletes nothing — a tenant that flips
    `phi_enabled = false` after a scan sees existing PHI findings
    in their table. UI hides PHI rows when the flag is off.
  * Custom regex patterns run on every scan; a tenant with N
    patterns adds O(N) regex passes per document. We cap at 32
    patterns server-side.

## Alternatives considered

  * **LLM-based PII detection** — rejected for v1. Cost/latency
    not justified when regex + NER already cover ~95% of common
    types with high precision. LLM is the right tool for context-
    dependent PII (e.g. "the patient's name") and lands later as
    a fourth `detection_source = 'llm'`.
  * **Auto-place legal hold from intelligence** — rejected. Bypasses
    the document-service permission/audit chain. Hold placement is
    a strictly domain action; intelligence recommends, document
    decides.
  * **Single `compliance_results` table without summary** — rejected.
    The doc list view must render a badge per row; aggregating
    findings per document on every list call would be O(documents
    × findings).
