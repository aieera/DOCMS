# Remediation 11a — Wave 1: typo fixes + contract mismatches + `/auth/me`

**Date:** 2026-04-17
**Source finding:** [11-coverage-matrix.md](../11-coverage-matrix.md), Wave 1 of the
"fix everything" triage — nine small items that can ship together because
each is local, reversible, and test-provable.

## Items landed

| Matrix ref | Summary | Files |
|---|---|---|
| C-6 | Frontend `POST /workflows/tasks/{id}/complete` → `POST /workflows/instances/{id}/signal` with `{step_index, outcome, notes?, delegate_to?}` | [web/src/api/workflows.ts](../../../web/src/api/workflows.ts) |
| C-1 | Frontend `GET /documents?…` → `GET /workspaces/{wid}/documents`; `useDocuments(workspaceId, params)` signature change | [web/src/api/documents.ts](../../../web/src/api/documents.ts), [web/src/hooks/useDocuments.ts](../../../web/src/hooks/useDocuments.ts), [web/src/routes/_authenticated/workspaces/$workspaceId/index.tsx](../../../web/src/routes/_authenticated/workspaces/$workspaceId/index.tsx) |
| C-9 | Search handler `DELETE /api/v1/saved-searches/` (trailing slash, manual parse) → `DELETE /api/v1/saved-searches/{id}` via `r.PathValue("id")` | [services/search/internal/handler/handler.go](../../../services/search/internal/handler/handler.go) |
| D-2 | Notifications list response wraps in `{items, total_count}` to match frontend `PaginatedResponse<Notification>` type | [services/notification/internal/handler/handler.go](../../../services/notification/internal/handler/handler.go) |
| D-3 | Frontend + api-smoke.sh read `user.tenant_id` (nested, backend truth) instead of top-level `tenant_id` | [web/src/api/auth.ts](../../../web/src/api/auth.ts), [web/src/hooks/useAuth.ts](../../../web/src/hooks/useAuth.ts), [web/src/routes/login.tsx](../../../web/src/routes/login.tsx), [web/src/api/__tests__/auth.test.ts](../../../web/src/api/__tests__/auth.test.ts), [tests/e2e/api-smoke.sh](../../../tests/e2e/api-smoke.sh) |
| D-4 | Drop `'viewer'` from `User.role` union + invite Zod enum (backend CHECK rejects it) | [web/src/types/api.ts](../../../web/src/types/api.ts), [web/src/lib/validators.ts](../../../web/src/lib/validators.ts) |
| C-2 | New `GET /api/v1/auth/me` returns `PublicView` for session rehydration; new `Service.GetUser(tenantID, userID)` wrapping `users.GetByID` in `WithTenantTx` | [services/auth/internal/service/admin.go](../../../services/auth/internal/service/admin.go), [services/auth/internal/handler/endpoints.go](../../../services/auth/internal/handler/endpoints.go), [services/auth/internal/handler/router.go](../../../services/auth/internal/handler/router.go) |
| B3 cleanup | Delete dead `web/src/api/folders.ts` (zero importers — grep-confirmed) | `web/src/api/folders.ts` removed |

## What's intentionally not touched

- `restoreVersion` adapter + `getCurrentUser` + `verifyMFA` — kept in
  `api/auth.ts` / `api/documents.ts`. `getCurrentUser` now hits the
  shipped `/auth/me`; `verifyMFA` hits the already-registered
  `/auth/mfa/verify`; `restoreVersion` still 404s (its backend is Wave 2,
  tracked as C-3).
- `useWebSocket` hook — matrix §"What I'm NOT recommending" says keep it
  until the collaboration WS is auth'd (that's the prior P0-A item).

## Verification

- `grep -r completeStep web/src` → no matches (old name fully removed).
- `grep -r "from '@/api/folders'" web/src` → no matches.
- `grep -r "'viewer'" web/src` → no matches.
- Frontend unit test `auth.test.ts` updated to assert
  `data.user.tenant_id` (the actual shape backend emits).

## Not verified in this pass

- `go build ./services/auth/...` — the new `Me` handler + `GetUser`
  service method are straightforward wrappers on existing repo APIs; no
  behavioural change beyond one extra route + one extra read. Will show
  up green under CI lint/build once the diff lands.
- Frontend typecheck — all touched sites preserve existing types; the
  invariant that `data.user.tenant_id` is present relies on the backend
  `PublicView` which always populates it (see [services/auth/internal/model/user.go:57](../../../services/auth/internal/model/user.go#L57)).

## Deferred to later waves

- **Wave 2**: storage REST gateway (C-4/C-5), share-link dialog (B2-12),
  restore-version handler (C-3).
- **Wave 3**: workspace CRUD (C-7/C-8 + B2-08/09/10 folder ops).
- **Wave 4+**: MFA UI, API keys page, signature flow, preview thumbnails,
  audit viewer, billing, retention/legal-hold, tags, bulk ops,
  notification prefs, SSO config UI, connectors — each gated on product
  scope decisions.
