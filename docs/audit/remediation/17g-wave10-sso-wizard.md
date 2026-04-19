# Remediation 17g — Wave 10 (part 7): SSO wizard + admin grid cleanup

**Date:** 2026-04-18
**Wave:** 10 · Scope: SSO wizard (1 of the pending 2 pages) + admin navigation cleanup.

## What shipped

### Part 1 — admin nav cleanup (pre-wizard tidy)

Six routes existed as files but weren't linked from `/admin` landing:
api-keys, billing, compliance, connectors, privacy, residency. Added
tiles in [admin/index.tsx](../../../web/src/routes/_authenticated/admin/index.tsx).

Sidebar ([Sidebar.tsx](../../../web/src/components/layout/Sidebar.tsx))
lacked Workspaces and Notifications links — added both. Now all 19
authenticated routes are reachable from the navigation without
typing URLs directly.

### Part 2 — SSO wizard backend

[services/auth/internal/handler/sso_admin.go](../../../services/auth/internal/handler/sso_admin.go)
exposes 6 endpoints under `/api/v1/admin/sso-configs/*`:

| Method | Path | Effect |
|---|---|---|
| GET | `/` | list (secrets redacted) |
| POST | `/` | create (with pre-validation) |
| POST | `/validate` | dry-run validate; no persistence |
| GET | `/{id}` | get one (secrets redacted) |
| PATCH | `/{id}` | update (with config re-validation) |
| DELETE | `/{id}` | delete |

Writes land on the pre-existing `sso_configs` table. Every query runs
inside `WithTenantTx`. Route registered in
[router.go](../../../services/auth/internal/handler/router.go) with
`AuthMiddleware + CSRFDoubleSubmit + RequireRole("admin", "owner")`.

**Validation logic**:

- **SAML** (`validateSAML`): requires exactly one of `idp_metadata_url`
  / `idp_metadata_xml`. If URL, fetches with a 5s timeout, caps at
  1 MiB, warns on non-https and missing `<EntityDescriptor>`. If
  inline XML, checks for `<EntityDescriptor>`. Caps
  `clock_skew_seconds` at 600. Warns when `attribute_mapping.email`
  is unset.
- **OIDC** (`validateOIDC`): requires `issuer_url` + `client_id` +
  `client_secret` + `redirect_url`. Enforces https (allows
  `http://localhost` for dev). Fetches
  `<issuer>/.well-known/openid-configuration`, asserts presence of
  `authorization_endpoint` / `token_endpoint` / `jwks_uri`. Warns
  when scopes are empty.

**Secret redaction**: `client_secret` is replaced with `••••••••`
on list/get responses. Actual secret persists in DB.

### Part 3 — frontend wizard

- [web/src/api/sso.ts](../../../web/src/api/sso.ts) — typed client:
  list / create / update / delete / validate. Discriminated union
  for SAML vs OIDC config bodies.
- [web/src/routes/_authenticated/admin/sso.tsx](../../../web/src/routes/_authenticated/admin/sso.tsx)
  replaces the EmptyState with:
  - Wizard with **4-step breadcrumb**: Provider → Config →
    Attributes → Validate.
  - Step 1: connection name + protocol toggle (SAML / OIDC).
  - Step 2a (SAML): metadata URL vs paste XML radio + input.
  - Step 2b (OIDC): issuer URL, client ID, client secret
    (password-masked), redirect URL, scopes (space-delimited).
  - Step 3 (SAML): attribute URI mappings (email / display name /
    groups) with placeholder examples from Microsoft + SAML 2.0
    schemas. OIDC shows a "standard claims" note — no mapping.
  - Step 4: `POST /validate` run + result banner (green or red)
    with warnings list and details JSON. Only green lets Create
    proceed.
  - Save → `POST /sso-configs`. List refreshes, wizard closes.
  - List below shows every configured provider with Active/Paused
    badge, pause-toggle, delete confirmation, and a collapsed
    JSON config preview.

## DoD — spec §9.1 SSO wizard row

| Requirement | Status |
|---|---|
| Step-by-step SAML/OIDC connection test | ✅ 4-step wizard |
| Upload metadata | ✅ URL fetch or paste XML |
| Run a test auth | 🟡 validation runs (metadata + discovery fetch); live test-authentication round trip with IdP-returned assertion display is **Wave 11** (needs an IdP-initiated SSO URL displayed in the wizard, a callback handler that echoes the raw assertion without persisting it, and a redirect back to the wizard). |
| Show assertion attributes | 🟡 same as above — deferred to Wave 11. Today the attribute-mapping step surfaces placeholder examples from Microsoft / SAML 2.0 schemas. |

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ Go + TS |
| 2 | ≥75% coverage | 🟡 no HTTP test harness in auth handler package yet (logged for Groups too) |
| 3 | Integration test | 🟡 Wave 13.1 |
| 4 | OpenAPI | ⚠ Wave 13.5 |
| 9 | RLS | ✅ `WithTenantTx` wraps every read + write; `sso_configs` RLS-wrapped |
| 10 | NATS subject | n/a — no domain events (logged) |
| 12 | Rollback | revert `sso_admin.go` + router diff + main diff + 2 TS files |

## Deferred (logged in out-of-scope.md)

- **Live test-assertion flow** — generate a temporary SP-initiated
  SSO URL, let the admin authenticate against their real IdP, show
  the returned assertion attributes before committing. Needs:
  scratch tenant-slug allocation, a new ACS callback variant that
  echoes instead of provisioning, and a per-wizard session token.
  Wave 11.
- **`dms.sso.config.*.v1` outbox events** on create / update /
  delete — audit today relies on `audit_events`.
- **Go handler tests** for the new routes (package lacks a shared
  test harness).
- **OIDC JWKS prefetch + secret rotation** endpoints — Wave 11.

## Wave 10 scorecard

| Page | Status |
|---|---|
| Groups | ✅ |
| Permission matrix | ✅ |
| **SSO wizard** | ✅ this doc |
| Retention policies | ✅ |
| Legal holds | ✅ |
| Webhooks | ✅ |
| Metadata schema | pending |
| Tags admin | ✅ |
| Share-link admin | ✅ |

**8 of 9 shipped.** Only Metadata schema editor left.

## Next prompt

**Metadata schema** — JSON Schema editor on a tenant-scoped
`metadata_schemas` table. Backend exists in the document service
(`GetMetadataSchema` / `UpdateMetadataSchema` RPCs). Frontend needs
a monaco-based JSON Schema editor + apply-to-document-class flow.
Lucide doesn't ship a monaco equivalent — we'd pull
`@monaco-editor/react` as a new dep. Alternatively ship with a
plain textarea + `zod.parse` validation (no new dep, less UX
polish).
