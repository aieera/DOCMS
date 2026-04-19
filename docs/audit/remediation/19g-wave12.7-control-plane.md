# Remediation 19g — Wave 12.7: Control-plane admin endpoints

**Date:** 2026-04-18
**Wave:** 12.7 · closes the Wave 8.4 "per-tenant region override" deferral
and the Wave 6.1 "24h CMK scheduled deletion" follow-up.

## Recon

Two operator workflows required raw SQL until now:

1. **Changing a tenant's default region** —
   `organizations.region_pin` had no mutation endpoint. Moving new
   uploads to `eu-west-1` required an engineer.
2. **CMK scheduled deletion** — AWS KMS enforces a 7–30 day
   deletion window for de-provisioned keys; our on-prem
   `LocalKeyManager` has no equivalent. Dropping a tenant's KEK
   today is immediate + irreversible. GDPR + SOC 2 both expect
   the grace window.

## What shipped

### Migration 000009

[services/document/migrations/000009_cmk_scheduled_deletion.up.sql](../../../services/document/migrations/000009_cmk_scheduled_deletion.up.sql):

```sql
ALTER TABLE tenant_keks
    ADD COLUMN scheduled_deletion_at TIMESTAMPTZ,
    ADD COLUMN scheduled_by          UUID;

CREATE INDEX idx_tenant_keks_scheduled_deletion
    ON tenant_keks (scheduled_deletion_at)
    WHERE scheduled_deletion_at IS NOT NULL;
```

Partial index keeps the reaper's daily sweep tight.

### Control-plane handler

[services/auth/internal/handler/tenant_admin.go](../../../services/auth/internal/handler/tenant_admin.go)
— new `TenantAdminHandler` with three routes, mounted under
`/api/v1/admin/tenants/{id}/*` behind `RequireRole("owner")`:

| Method | Path | Effect |
|---|---|---|
| PATCH | `/region-pin` | Update `organizations.region_pin`. Body `{region}`. |
| POST | `/schedule-cmk-deletion` | Stamp `scheduled_deletion_at = now() + grace_hours` on every live KEK row for the tenant. Body `{grace_hours}`. Clamped to `[24, 30*24]` to mirror AWS KMS bounds. |
| POST | `/cancel-cmk-deletion` | Clear the column; only effective before the grace window closes. |

Tenant isolation: `{id}` must equal the caller's tenant (owners
operate on their own tenant only). A separate platform-operator
surface for cross-tenant admin is logged as Wave 12.7b.

### Wire-up

[router.go](../../../services/auth/internal/handler/router.go) —
new optional argument `tenantAdmin *TenantAdminHandler`. Route
group wraps with `AuthMiddleware + CSRFDoubleSubmit +
RequireRole("owner")`. `cmd/server/main.go` constructs the
handler and passes it.

## Behaviour notes

- **Region-pin change is metadata-only.** Existing documents keep
  their own `region_pin` until the Wave 8.4 migrate workflow runs.
  The endpoint response explicitly calls this out so admins
  don't expect a bulk rewrite.
- **CMK schedule is idempotent.** Scheduling twice overwrites the
  timestamp (useful when operators want to extend the grace
  window; they re-POST with a larger `grace_hours`). Reaper job
  (Wave 12.7b) reads the partial index and drops KEKs where the
  timestamp has passed.
- **Cancel only works inside the grace window.** A cancel POST
  that runs after `scheduled_deletion_at < now()` updates zero
  rows → 404. The reaper runs daily at a fixed time; operators
  get a several-hour buffer after the nominal deletion timestamp
  to cancel in practice, but the API promise is strict.

## DoD

| Requirement | Status |
|---|---|
| Region-pin mutation endpoint | ✅ |
| CMK scheduled deletion (24h min, 30d max) | ✅ |
| Cancel within grace window | ✅ |
| Owner-role gated | ✅ |
| Audit log via structured logging | ✅ (CorrelationHTTP + RequestLogHTTP stack) |
| Reaper job to physically drop keys | 🟡 Wave 12.7b |
| Platform-operator cross-tenant surface | 🟡 Wave 12.7b |

## Deferred

- **Reaper cron** — daily job that reads
  `tenant_keks WHERE scheduled_deletion_at < now()`, drops the
  master material from the KMS (Vault revoke / AWS KMS
  `ScheduleKeyDeletion` / LocalKeyManager cache evict), and marks
  the row `retired_at = now()`. Wave 12.7b.
- **Front-end surface** — tenant-settings page today edits display
  name + branding; region + CMK-deletion controls aren't wired.
  UI work, Wave 12 follow-up.
- **Cross-tenant platform operator** surface — a separate role
  ("platform_owner"?) lets the DMS SaaS provider operate across
  tenants. Today all three endpoints self-scope. Wave 13 control-
  plane bundle.

## Wave 12 scorecard

| Item | Status |
|---|---|
| 12.1 SMTP | ✅ |
| 12.2 Storage re-encrypt | ✅ |
| 12.3 Re-wrap CLI | ✅ |
| 12.4 Cross-service subject purge | ✅ |
| 12.5 Redaction fan-out | ✅ |
| 12.6 Connector OAuth purge | ✅ |
| **12.7 Control-plane admin endpoints** | ✅ this doc |
| 12.8 Vault / AWS KMS adapters | pending |
| 12.9 DSS Java sidecar | pending |

## Next prompt

**12.8 — Vault / AWS KMS production adapters.** Stubs have lived
in [pkg/crypto/kms.go](../../../pkg/crypto/kms.go) since
ADR 0022 promised "dev/on-prem LocalKeyManager + prod Vault/AWS".
Prod deployments need real adapters so the master secret never
sits in process memory.
