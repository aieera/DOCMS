# Remediation 17b — Wave 10 (part 2): Groups admin page

**Date:** 2026-04-17
**Wave:** 10 · Scope: 1 of the pending 7 pages from §9.1.

## Recon finding

`groups` + `group_members` tables exist (migration 000001) with RLS.
SCIM already exposes `/scim/v2/{tenant_slug}/Groups` but uses
per-tenant bearer-token auth, not the session cookie the admin UI
uses. The placeholder page rendered an EmptyState with no network
call. No `/api/v1/admin/groups*` route existed.

## What shipped

### Backend — new `/api/v1/admin/groups/*` surface

[services/auth/internal/handler/groups.go](../../../services/auth/internal/handler/groups.go)
— new `GroupsHandler` bound to the pool. Every query runs inside
`WithTenantTx` so RLS (`app.current_tenant` GUC) is enforced on each
request.

| Method | Path | Effect |
|---|---|---|
| GET | `/api/v1/admin/groups` | list + per-group `member_count` via subquery |
| POST | `/api/v1/admin/groups` | create with `{name, description?}` |
| GET | `/api/v1/admin/groups/{id}` | detail + members (joined to users for email/display_name) |
| PATCH | `/api/v1/admin/groups/{id}` | rename / update description |
| DELETE | `/api/v1/admin/groups/{id}` | soft delete (sets `deleted_at`) |
| POST | `/api/v1/admin/groups/{id}/members` | `{user_id}` → ON CONFLICT DO NOTHING |
| DELETE | `/api/v1/admin/groups/{id}/members/{userId}` | remove member |

Wired in [router.go](../../../services/auth/internal/handler/router.go):
`r.Route("/api/v1/admin/groups", …)` with `AuthMiddleware +
CSRFDoubleSubmit + RequireRole("admin","owner")`. Instance constructed
in [cmd/server/main.go](../../../services/auth/cmd/server/main.go) —
one-line addition to the existing `h.Router(...)` call.

Deliberate choice: the admin surface bypasses SCIM rather than
bridging session-cookie auth to per-tenant bearer tokens. Both live
side by side — IdP scripts continue to use SCIM, the web UI uses
`/api/v1/admin/groups`.

### Frontend

- [web/src/api/groups.ts](../../../web/src/api/groups.ts) — typed
  `Group` / `GroupMember` / `GroupDetail` + all 7 endpoint helpers.
- [web/src/routes/_authenticated/admin/groups.tsx](../../../web/src/routes/_authenticated/admin/groups.tsx)
  — split-pane layout:
  - Left column: group list with member counts + "created …".
  - Right column: selected group's name (inline rename on blur),
    description, add-member by UUID, member list with remove.
  - Top: inline create form.
  - TanStack Query with invalidation on every mutation.
  - Confirmation on destructive delete.

Member add/remove uses raw UUIDs today — a proper user-picker combo
lands with the broader Wave 10 design-system push (logged).

### Tests

No new Go test file — the existing `services/auth/internal/handler/`
package has `no test files` per the build output. The route
registration is covered at the `go build` level (lazy wiring —
`nil` handler → route not mounted). Frontend tsc clean.

## DoD — spec §9.1 Groups row

| Requirement | Status |
|---|---|
| List | ✅ |
| Create | ✅ |
| Rename | ✅ (inline on blur) |
| Delete | ✅ (soft) |
| Add/remove members | ✅ |
| Uses existing /groups API | 🟡 we built `/admin/groups` fresh; the SCIM `/Groups` at `/scim/v2/...` needs bearer-token auth and wasn't reachable from the session-cookie UI. Both endpoints coexist. |

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ Go + TS |
| 2 | ≥75% coverage on new files | 🟡 auth handler package has no existing tests; adding a harness is deferred |
| 3 | Integration test | 🟡 Wave 13.1 |
| 4 | OpenAPI | ⚠ Wave 13.5 |
| 5 | Prom metrics | 🟡 Wave 13.6 |
| 6 | Structured logs | ✅ via CorrelationHTTP + RequestLogHTTP wrappers |
| 7 | Grafana dashboard | 🟡 Wave 13.6 |
| 8 | OTEL spans | 🟡 Wave 13.6 |
| 9 | RLS | ✅ every query inside `WithTenantTx`; groups + group_members tables are RLS-wrapped |
| 10 | NATS subject | n/a (no domain events — groups are a permission primitive, not a domain aggregate) |
| 11 | Index-plan | no schema change; existing `idx_group_members_user` / `idx_group_members_group` cover lookups |
| 12 | Rollback | revert `groups.go` + router.go diff + main.go wiring + 2 TS files |
| 13 | Runbook | no new operational surface; existing auth runbook covers role + session auth |

## Deferred (logged in out-of-scope.md)

- **User-picker combo** for member add — currently UUID input box.
- **Bulk add / bulk remove** — not in spec, easy follow-up.
- **Group-based permissions UI** — wire groups into OPA policy rules
  once Permission Matrix page lands.
- **`admin_groups_*_total` Prom counters** — Wave 13.6.
- **Go handler-level tests** for the groups routes — package needs
  a test harness (nothing currently in
  `services/auth/internal/handler/`).

## Wave 10 scorecard

| Page | Status |
|---|---|
| **Groups** | ✅ this doc |
| Permission matrix | pending |
| SSO wizard | pending |
| Retention policies | pending (needs backend first) |
| Legal holds | ✅ Wave 8.2 |
| Webhooks | ✅ Wave 10 part 1 |
| Metadata schema | pending |
| Tags admin | pending |
| Share-link admin | pending |

## Next prompt

Pick one of the pending pages. **Share-link admin** and **Tags
admin** both have existing backend endpoints — fastest remaining.
**Retention policies** needs new backend CRUD first. **Permission
matrix** needs OPA introspection endpoint. **SSO wizard** is a
larger multi-step flow. **Metadata schema** needs monaco editor +
JSON Schema library.
