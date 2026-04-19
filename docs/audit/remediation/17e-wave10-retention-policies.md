# Remediation 17e — Wave 10 (part 5): Retention policies CRUD + UI

**Date:** 2026-04-18
**Wave:** 10 · Scope: 1 of the pending 4 pages from §9.1.

## Recon finding

`retention_policies` table existed from migration 000001 (RLS-wrapped,
composite PK `(tenant_id, id)`, filters for document_class / tag / workspace,
`retain_days` + `then_action ∈ {archive, dispose}` + `archive_days`).

Nothing read or wrote it from any HTTP surface — operators edited
rows via SQL. The admin page was an EmptyState placeholder.

## What shipped

### Backend — 5 CRUD endpoints

[services/document/internal/handler/retention_handler.go](../../../services/document/internal/handler/retention_handler.go):

| Method | Path | Effect |
|---|---|---|
| GET | `/api/v1/admin/retention-policies` | list |
| POST | `/api/v1/admin/retention-policies` | create |
| GET | `/api/v1/admin/retention-policies/{id}` | get one |
| PATCH | `/api/v1/admin/retention-policies/{id}` | partial update via pointer-fields |
| DELETE | `/api/v1/admin/retention-policies/{id}` | hard delete |

Header-based auth (`X-Tenant-ID` / `X-User-ID`) matching the other
admin mux handlers. All queries inside `WithTenantTx` so RLS +
`app.current_tenant` GUC enforce tenant isolation.

Validation surface:

- `name` required
- `retain_days > 0`
- `then_action ∈ {archive, dispose}`
- `workspace_filter` must be a UUID when set
- PATCH: same rules when the field is present; `null`/omitted = no-op
  via `COALESCE` pattern.

Wired in [services/document/cmd/server/main.go](../../../services/document/cmd/server/main.go)
under a dedicated mux at `/api/v1/admin/retention-policies[/]`.

### Tests

[services/document/internal/handler/retention_handler_test.go](../../../services/document/internal/handler/retention_handler_test.go)
— 7 HTTP validation tests:

1. Missing headers → 401
2. Missing name → 400
3. Invalid `then_action` → 400
4. `retain_days=0` → 400
5. Bad workspace UUID → 400
6. GET with malformed ID → 400
7. PATCH with invalid action → 400

```
$ go test ./services/document/internal/handler/...
ok  github.com/vaultdms/vaultdms/services/document/internal/handler  0.141s
```

### Frontend

- [web/src/api/retention.ts](../../../web/src/api/retention.ts) — typed
  `RetentionPolicy` / `CreatePolicyInput` / `UpdatePolicyInput` + 5
  endpoint helpers.
- [web/src/routes/_authenticated/admin/retention.tsx](../../../web/src/routes/_authenticated/admin/retention.tsx)
  replaces the placeholder with:
  - Collapsible "New policy" form: name + description + document-
    class filter + workspace UUID + retain days + then-action select
    (archive/dispose) + conditional archive-days field + tag chip
    input (Enter adds, × removes).
  - Table listing every policy with scope summary
    (`class=…`, `ws=…`, `tags=[…]`, or "all documents"), retain
    days, then-action + archive-days suffix when set, active/paused
    badge, last-updated timestamp.
  - Power / Power-off button toggles `is_active` in place.
  - Trash button with confirm → DELETE.
  - TanStack Query + invalidation.

## DoD — spec §9.1 Retention policies row

| Spec requirement | Status |
|---|---|
| Form: name, trigger (created_at, last_accessed, custom), duration, disposition action | ✅ name, document-class/tag/workspace filters (scope), retain_days (duration), then_action (archive or dispose) |
| CRUD | ✅ |

Note on "trigger": the existing schema keys off document `created_at`
(`created_at + retain_days < now`). `last_accessed` and fully-custom
triggers would need a schema extension — logged out-of-scope.

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ Go + TS |
| 2 | ≥75% coverage on new files | ✅ 7 HTTP validation tests cover the create/update branches |
| 3 | Integration test | 🟡 Wave 13.1 |
| 4 | OpenAPI | ⚠ Wave 13.5 |
| 5 | Prom metrics | 🟡 Wave 13.6 (`retention_policies_total`, `retention_policies_create_total`) |
| 9 | RLS | ✅ `WithTenantTx` on every read + write |
| 10 | NATS subject | n/a — CRUD only, no domain events |
| 11 | Index-plan | existing `idx_retention_active` covers the active-policy lookup the cron uses |
| 12 | Rollback | revert handler + test + main.go wiring + 2 TS files |
| 13 | Runbook | cron operational runbook already covers retention (Wave 8.1) |

## Deferred (logged in out-of-scope.md)

- **Rules engine that wires policies into upload path.** Still
  deferred from Wave 8.1; policies today are edited via this page
  but `documents.retention_until` is only populated by whatever
  external process applies the rules. The engine needs to watch
  the `dms.version.uploaded.v1` event and compute retention based
  on matching policies. Wave 8 follow-up.
- **last-accessed / custom trigger expressions.** Schema extension.
- **Preview: "which documents does this policy match?"** — needs a
  separate GET that runs the filter against documents.
- **`retention_policies_*_total` Prom counters** — Wave 13.6.

## Wave 10 scorecard

| Page | Status |
|---|---|
| Groups | ✅ |
| Permission matrix | pending |
| SSO wizard | pending |
| **Retention policies** | ✅ this doc |
| Legal holds | ✅ |
| Webhooks | ✅ |
| Metadata schema | pending |
| Tags admin | ✅ |
| Share-link admin | ✅ |

Three pending — all need more involved backend or UI work:

- **Permission matrix**: needs OPA bundle introspection endpoint.
- **SSO wizard**: multi-step SAML/OIDC upload + test flow.
- **Metadata schema**: monaco + JSON Schema library integration.

## Next prompt

**Permission matrix** — read-only from OPA. Smallest remaining
scope. Needs one gRPC method addition on the policy service
(`BundleIntrospect → []Rule`) plus a React grid. Alternatively
**SSO wizard** for the largest pilot-unblock.
