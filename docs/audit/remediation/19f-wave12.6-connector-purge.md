# Remediation 19f — Wave 12.6: Connector OAuth subject purge endpoint

**Date:** 2026-04-18
**Wave:** 12.6 · upgrades the Wave 12.4 connector-purge stub to a real call.

## Recon finding

The `connector_configs` schema is keyed by `(tenant_id, provider)`.
OAuth tokens (client_secret, access, refresh) live encrypted in
the `config` JSONB column. There is **no `granted_by` or user-
identifier column** — connector grants are modelled tenant-scoped,
not subject-scoped.

A Wave 12.4 DSR erase activity already fired the "purge subject
from connectors" step, but with the schema as-is there's nothing
user-specific to target. The activity was a soft-no-op stub.

Wave 12.6's job: ship the endpoint + wire the real HTTP call so
the audit trail reflects the call happened, even when the honest
answer is "zero rows purged — schema not yet subject-scoped." When
Wave 12.6b adds `granted_by` + a revocation path, the endpoint
body becomes non-trivial without touching the workflow side.

## What shipped

### Connector service endpoint

[services/connector/internal/handler/handler.go](../../../services/connector/internal/handler/handler.go)
— new route `POST /internal/v1/connectors/purge-subject`
accepting `{tenant_id, subject_id}`.

Current body: logs the invocation, returns `{purged: 0, note:
"connector_configs is tenant-scoped; per-user OAuth grants not
yet modeled (Wave 12.6b)"}`. The structured log entry is the
audit artifact — operators can confirm the DSR workflow did
reach this service, even when there was no per-user row to
scrub.

### Workflow activity

[services/workflow/internal/activities/dsr_crossservice.go](../../../services/workflow/internal/activities/dsr_crossservice.go)
— `PurgeSubjectFromConnectors` upgraded from stub to real HTTP
POST against `ServiceURLs["connector"]`. 30s timeout. Parses
`{purged}` from the response and returns the count. Empty
service URL → soft no-op (same pattern as Wave 12.4).

## Why zero is the correct answer today

The GDPR connector-erase question breaks down as:

- *"Revoke OAuth grants the subject personally authorised."* —
  not modelled; requires `granted_by UUID` on `connector_configs`
  and an "active grant owner" notion. Wave 12.6b.
- *"Clear tokens the subject has accessed."* — not modelled
  either; we store one config per (tenant, provider), not per
  session or per user.
- *"Delete rows about the subject."* — there are none.

A silent no-op would mislead auditors into thinking the fan-out
hit real rows; this endpoint surfaces the truthful answer in
both the log and the response body.

## DoD

| Requirement | Status |
|---|---|
| Real HTTP endpoint on connector service | ✅ |
| Workflow activity calls it | ✅ |
| Audit trail when called | ✅ structured log + response body |
| Honest `purged: 0` vs. silent-no-op | ✅ |
| Per-user grant model + real token revoke | 🟡 Wave 12.6b (schema change needed) |

## Wave 12 scorecard

| Item | Status |
|---|---|
| 12.1 SMTP | ✅ |
| 12.2 Storage re-encrypt | ✅ |
| 12.3 Re-wrap CLI | ✅ |
| 12.4 Cross-service subject purge | ✅ |
| 12.5 Redaction fan-out | ✅ |
| **12.6 Connector OAuth purge endpoint** | ✅ this doc |
| 12.7 Control-plane admin endpoints | pending |
| 12.8 Vault / AWS KMS adapters | pending |
| 12.9 DSS Java sidecar | pending |

## Next prompt

**12.7 — Control-plane admin endpoints.** Spec §11 calls out the
per-tenant region override (operators need to change a tenant's
default `organizations.region_pin` without SQL) and the CMK
scheduled-deletion workflow (de-provisioned tenants should have
their KEKs marked for scheduled deletion with a 24-hour grace).
