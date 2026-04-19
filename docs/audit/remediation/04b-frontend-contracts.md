# Remediation 04b — Frontend ↔ backend contract gap

**Date:** 2026-04-17
**Scope:** Close the 10 missing endpoints and 2 path mismatches the frontend
called against.
**Source finding:** `docs/audit/05-contracts.md` §3.

---

## Before / After

| Frontend call | Before | After |
|---|---|---|
| `GET /permissions/:type/:id` | **404** — policy is gRPC-only | `policy` service REST wrapper |
| `POST /permissions/check` | **404** | `policy` service REST wrapper |
| `POST /permissions/:type/:id` | **404** | `policy` service REST wrapper (grant) |
| `DELETE /permissions/:type/:id/:principalId` | **404** | `policy` service REST wrapper (revoke-by-principal) |
| `GET /admin/users` | **404** — billing only internal | `auth` service, cursor-paginated |
| `POST /admin/users/invite` | **404** | `auth` service, outbox event `user.invited.v1` |
| `POST /admin/users/:id/suspend` | **404** | `auth`, revokes sessions + outbox `user.suspended.v1` |
| `POST /admin/users/:id/reset-mfa` | **404** | `auth`, clears MFA + outbox `user.mfa_reset.v1` |
| `GET /admin/audit-log` | mismatch | **frontend** now calls `/audit/events` (backend unchanged) |
| `GET/PUT /admin/settings` | mismatch (internal only) | `billing` service user-facing wrapper around existing features API |

---

## Decisions, one at a time

### 1. Permission REST — put it on the policy service (not the gateway)

The task gave us the choice; policy was the right home. The gRPC surface
(`CheckPermission`, `BatchCheckPermission`) stays as the service-to-service
contract. A new `services/policy/internal/handler/http.go` serves REST via
Go 1.22 `http.ServeMux`. All four endpoints re-use the existing service
methods (`ListByResource`, `Check`, `Grant`, `Revoke`). Revoke-by-principal
runs `ListByResource` → filter by principal → `Revoke` per row, because the
service's `Revoke` takes a permission id and the frontend URL only carries
the principal.

The authoritative ACL-administration check is
`svc.Check(..., action="admin", resource)` — the same rule evaluated by
every tenant write path. Org owners and admins bypass the per-resource
check (mirrors the existing Rego rules on workspace/folder).

### 2. Admin users — put it on the auth service

Auth already owns users and has session validation. New file
`services/auth/internal/handler/admin.go`, mounted under `/api/v1/admin/users`
with chi and gated by the existing `h.AuthMiddleware` + new
`middleware.RequireRole("admin","owner")`.

A new `services/auth/internal/service/admin.go` implements the business
logic:
- **ListUsers** — cursor pagination on `created_at DESC`, filters on
  status / role / free-text `q` (ILIKE over email + display_name).
- **InviteUser** — creates a placeholder row with an empty password hash
  (`bcrypt.CompareHashAndPassword` with empty hash always fails, so the
  row can't log in until the invite-accept flow sets a password). The
  invite token's SHA-256 hash + 72 h expiry live in `settings` JSONB. The
  plaintext token is returned to the HTTP caller (for the email template)
  and emitted inside a `dms.user.invited.v1` outbox event.
- **SuspendUser** — `status='suspended'` + revoke every session, outbox
  event `dms.user.suspended.v1`. Refuses self-suspend.
- **ResetUserMFA** — `ClearMFA` + `SetMFAEnabled(false)` + revoke every
  session, outbox event `dms.user.mfa_reset.v1`. User gets re-enrollment
  prompt on next login.

`model.PublicView` gained an optional `LastLoginAt` field because the
admin table needs it; existing callers of `ToPublic` are unchanged in
behavior (the field is a nullable pointer).

### 3. Admin settings — put it on the billing service

Billing already owns feature flags via `/internal/v1/tenants/{id}/features`
(X-API-Key auth for service-to-service). A parallel user-facing wrapper at
`/api/v1/admin/settings` reuses the same `service.GetFeatureFlags` /
`UpdateFeatureFlags` calls. Tenant comes from the authenticated session,
not the URL — there is no way for one tenant's owner to touch another
tenant's flags.

Session authentication is shared across services (see next section).
Billing's main.go now serves two HTTP surfaces on one listener:

```
/internal/v1/*         → coreMux (existing handler, X-API-Key)
/api/v1/admin/settings → adminMux (RequireRole("owner") ∘ SessionAuth)
```

### 4. New shared package: `pkg/middleware/sessionauth.go`

Before 04b, only the auth service validated session cookies — every other
service assumed an upstream proxy had already done it. That worked for
gRPC (tenant picked up from metadata) but left no story for new HTTP
endpoints on non-auth services.

The new `SessionAuth` middleware does a minimal direct lookup:

```sql
SELECT s.tenant_id, s.user_id, u.email, u.role, s.expires_at
FROM sessions s
JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
WHERE s.token_hash = $1
  AND s.revoked_at IS NULL
  AND s.expires_at > now()
  AND u.deleted_at IS NULL
```

`token_hash` is globally unique (`UNIQUE` constraint on `sessions`), so no
tenant GUC is needed. The middleware populates `auth.UserInfo` on the
context via the existing `auth.WithUser` helper.

`RequireRole(roles...)` also moved to `pkg/middleware`; it's a stateless
check against `auth.GetUserRole(ctx)`. The auth-service-local
`Handler.RequireRole` is retained (uses the same logic via the auth
handler's writeError formatting) — touching it would churn existing API
keys routes for no gain.

**Redis cache intentionally left out.** Auth service has a hot-path
Redis cache (Phase 02 — tenant-scoped keys). Re-implementing that in a
shared package would be a substantive refactor; the REST endpoints added
here are admin-only and not latency-critical. Hot endpoints on other
services still go through gRPC + `TenantInterceptor` as before.

### 5. Frontend fixes

- `web/src/api/admin.ts` — `getAuditLog` now calls `/audit/events` (the
  axios client already prefixes `/api/v1`). `inviteUser` gained a
  `display_name` parameter the backend requires.
- `web/src/api/permissions.ts` — aligned body field names with the
  backend (`principal_type` / `principal_id` / `capability` /
  `expires_at`). Added typed `Permission` shape + typed arg unions on
  `grantPermission`.

No frontend features were removed.

---

## Files touched

### New
- `pkg/middleware/sessionauth.go`
- `services/policy/internal/handler/http.go`
- `services/auth/internal/handler/admin.go`
- `services/auth/internal/service/admin.go`
- `services/billing/internal/handler/admin.go`
- `tests/contract/README.md` + `paths.txt` + `run.sh`
- `docs/audit/remediation/04b-frontend-contracts.md`

### Updated
- `services/auth/internal/handler/router.go` — mounts `/api/v1/admin/users`
- `services/auth/internal/model/user.go` — `PublicView.LastLoginAt`
- `services/policy/cmd/server/main.go` — starts an HTTP server wrapped in `SessionAuth`
- `services/billing/cmd/server/main.go` — splits `/internal/` vs `/api/v1/admin/settings`
- `web/src/api/admin.ts`, `web/src/api/permissions.ts`
- `docs/api/openapi.yaml` — added 10 path specs under the `Admin` tag

---

## Verification

```bash
$ go build (every workspace module)
clean

$ go test pkg/errors/... services/auth/... services/policy/...
ok

$ (cd web && npx tsc --noEmit)
clean

$ ./tests/contract/run.sh  (needs a live stack)
— deferred; same gate as remediation 09
```

`tests/contract/run.sh` reads `paths.txt` and hits every route against a
running stack. A path that returns `404` fails the run; any other status
(including `401` unauthenticated, `400` bad body) passes, because the
point is to prove route *registration*, not behavior. It's designed to
run in CI right after `make up` without depending on seeded data.

The `make setup` full-stack sanity check (log in as admin, load
`/admin/users`) is still gated on remediation 09's follow-up OCR work —
same reason as 04a and 03c. Static verification covers every path-level
contract change this remediation introduces.

---

## DO-NOTs honored

- No frontend features removed. Every path the frontend calls now has a
  backend handler.
- `/internal/v1/*` (X-API-Key) and `/api/v1/*` (session) kept separate —
  different auth models, different consumers.
- Audit events emitted on every admin mutation (`user.invited.v1`,
  `user.suspended.v1`, `user.mfa_reset.v1`) via the existing outbox
  pattern. No direct NATS publishes, no silent side-effects.
- No user-impersonation endpoint added — not in the playbook and
  explicitly called out as something not to invent.
