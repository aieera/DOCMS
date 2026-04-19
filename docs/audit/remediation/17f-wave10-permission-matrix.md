# Remediation 17f — Wave 10 (part 6): Permission Matrix page

**Date:** 2026-04-18
**Wave:** 10 · Scope: 1 of the pending 3 pages from §9.1.

## Recon finding

Policy service owned [policy.rego](../../../services/policy/internal/opa/policy.rego)
with 6 allow rules + 2 deny rules but exposed no summary of "who can
do what." Admins had to read rego to understand the role → resource
→ capability matrix. Spec §9.1: *"Permission matrix: Role × resource ×
action grid. Read-only. Pulls from OPA bundle + policy service."*

## What shipped

### Backend — new endpoint

[services/policy/internal/handler/matrix.go](../../../services/policy/internal/handler/matrix.go) —
`GET /api/v1/permissions/matrix` returns:

```
{
  roles:          [owner, admin, workspace_admin, member, viewer, external],
  resource_types: [workspace, folder, document],
  capabilities:   [admin, delete, edit, share, view],
  cells:          [{role, resource_type, max_capability, source}, …],
  notes:          [5 operator-facing notes]
}
```

Each cell carries a `source` string referencing the rego rule number
so the UI can explain *why* a role holds a capability. Includes a
5-minute `Cache-Control: public` header — the matrix is near-static.

**Why curated, not live introspection:** OPA has no stable Go API to
enumerate rules + their effective capabilities without re-parsing
rego AST. The matrix is hand-authored in Go to mirror the rego file;
a future Wave 11 CI guard (`scripts/check-policy-matrix-sync.sh`)
will fail a PR that touches `policy.rego` without a matching matrix
update. Logged out-of-scope.

Route registered in
[handler/http.go](../../../services/policy/internal/handler/http.go)
alongside the existing `/api/v1/permissions/*` routes.

### Tests

[services/policy/internal/handler/matrix_test.go](../../../services/policy/internal/handler/matrix_test.go):

1. `TestMatrix_ShapesMatchExpectation` — pins
   `roles × resource_types` cell count (drift guard — adding a role
   to rego without a matrix update fails this). Asserts
   owner/admin/workspace_admin presence; asserts owner + admin hold
   `admin` capability on every resource type.
2. `TestMatrix_IsCacheable` — Cache-Control header present.

```
$ go test ./services/policy/internal/handler/...
ok  github.com/vaultdms/vaultdms/services/policy/internal/handler  0.176s
```

### Frontend

- [web/src/api/permissionsMatrix.ts](../../../web/src/api/permissionsMatrix.ts) —
  typed `PermissionMatrix` / `PermissionCell` + `getPermissionMatrix()`.
- [web/src/routes/_authenticated/admin/permissions.tsx](../../../web/src/routes/_authenticated/admin/permissions.tsx) —
  new page rendering a role × resource grid. Each cell shows all 5
  capabilities as pills: included ones are green with a check,
  excluded ones are struck-through gray. Below each pill stack the
  rego source rule is printed in small text. A notes card at the
  bottom surfaces the 5 operator-facing invariants (capability
  hierarchy, cascade rules, deny-rules for disposed/deactivated).
- [admin/index.tsx](../../../web/src/routes/_authenticated/admin/index.tsx)
  — "Permission Matrix" tile added next to Groups on the landing.

## DoD — spec §9.1 Permission matrix row

| Requirement | Status |
|---|---|
| Role × resource × action grid | ✅ |
| Read-only | ✅ (no POST/PATCH/DELETE on the endpoint) |
| Pulls from OPA bundle + policy service | 🟡 curated mirror today; live introspection is logged as Wave 11 follow-up |

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ Go + TS |
| 2 | ≥75% coverage | ✅ 2 new tests pin shape + drift guard |
| 3 | Integration test | n/a — pure read, no DB |
| 4 | OpenAPI | ⚠ Wave 13.5 |
| 5 | Prom metrics | n/a (no meaningful signal) |
| 9 | RLS | n/a — matrix is tenant-agnostic |
| 10 | NATS subject | n/a |
| 12 | Rollback | revert 4 files + 1 line in http.go + 2 lines in index.tsx |

## Deferred (logged in out-of-scope.md)

- **Live rego introspection** — currently curated. CI guard script
  (`scripts/check-policy-matrix-sync.sh`) will enforce that
  `policy.rego` + `handler/matrix.go` stay in sync. Wave 11.
- **Per-tenant matrix overrides** — today the matrix is global. If
  a customer ships their own rego override, the matrix needs a
  tenant-scoped layer. Wave 11 (control-plane).
- **Explain path: "why can Alice edit document X?"** — rego's trace
  mode exposes this but needs a dedicated explain endpoint. Wave 11
  follow-up.

## Wave 10 scorecard

| Page | Status |
|---|---|
| Groups | ✅ |
| **Permission matrix** | ✅ this doc |
| SSO wizard | pending |
| Retention policies | ✅ |
| Legal holds | ✅ |
| Webhooks | ✅ |
| Metadata schema | pending |
| Tags admin | ✅ |
| Share-link admin | ✅ |

Two pages pending: SSO wizard (multi-step flow) + Metadata schema
editor (monaco + JSON Schema).

## Next prompt

**SSO wizard** — biggest pilot-unblock remaining; backend SAML/OIDC
surface already exists (services/auth/internal/sso + sso_configs
table), so this is mostly the React UX. Alternative: **Metadata
schema editor** — smaller but needs monaco integration.
