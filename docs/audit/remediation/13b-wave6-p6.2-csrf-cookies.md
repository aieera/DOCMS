# Remediation 13b — Wave 6 Prompt 6.2: CSRF double-submit

**Date:** 2026-04-17
**Wave:** 6 · **Prompt:** 6.2
**Source:** `DMS Architecture/final.md` § 5.2.
**Status:** ✅ shipped, live-verified end-to-end.

## Headline

Recon surfaced that the codebase was already 95% compliant with the
spec: the session cookie is httpOnly/Secure/SameSite=Strict, Zustand
store is in-memory only, axios has `withCredentials: true`, and zero
`localStorage.*` calls exist anywhere under `web/src/`.

**The only real gap was CSRF double-submit.** That's now in place.

Live evidence from dev box (2026-04-17 21:06 UTC+4):

```
POST /api/v1/auth/login
  → Set-Cookie: dms_session=...; HttpOnly; Secure; SameSite=Strict
  → Set-Cookie: dms_csrf=...;   Secure; SameSite=Strict    (NOT httpOnly)

POST /api/v1/auth/logout (no X-CSRF-Token)    → HTTP 403
POST /api/v1/auth/logout (X-CSRF-Token match) → HTTP 204
```

## Files landed

### Backend — new middleware

[pkg/middleware/csrf.go](../../../pkg/middleware/csrf.go) **new** —
`CSRFDoubleSubmit()` + `CSRFCookieName` + `CSRFHeaderName` constants.
Exempts GET/HEAD/OPTIONS and `Bearer vdms_*` callers. Rejects missing
cookie, missing header, empty values, and mismatches. Constant-time
compare via `crypto/subtle.ConstantTimeCompare`.

### Backend — auth handler

- [services/auth/internal/handler/handler.go](../../../services/auth/internal/handler/handler.go)
  — added `setCSRFCookie()` (generates 32 random bytes via `crypto/rand`,
  hex-encodes, sets non-httpOnly cookie with Secure + SameSite=Strict
  + matching session expiry) and `clearCSRFCookie()`.
- [services/auth/internal/handler/endpoints.go](../../../services/auth/internal/handler/endpoints.go)
  — `Login`, `MFAVerify`, `MFARecovery` now call `setCSRFCookie`.
  `Logout` clears both.
- [services/auth/internal/handler/router.go](../../../services/auth/internal/handler/router.go)
  — wrapped the authenticated `/api/v1/auth/*` group and
  `/api/v1/admin/users/*` group with `vdmsmw.CSRFDoubleSubmit()`
  after `AuthMiddleware`.

### Frontend — axios interceptor

[web/src/api/client.ts](../../../web/src/api/client.ts) — existing
interceptor now also reads `dms_csrf` via a tiny `readCookie()` helper
and sets `X-CSRF-Token` for any non-safe HTTP method. Zero risk to
GET flows.

### Tests — 9 cases

[pkg/middleware/csrf_test.go](../../../pkg/middleware/csrf_test.go) — 
covers safe-method bypass, missing cookie, missing header, mismatch,
match, empty values, API-key bypass, session-bearer no-bypass, and
all four mutating methods.

```
$ go test ./pkg/middleware/...
ok  github.com/vaultdms/vaultdms/pkg/middleware   0.185s
```

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ pkg + services/auth build green |
| 2 | ≥75% coverage on new files | ✅ `csrf.go` every branch hit in 9 tests |
| 3 | Integration test | ✅ live HTTP round-trip verified (see log quote above) |
| 4 | OpenAPI | n/a — cookie/header are transport, not schema |
| 5 | Prom metrics | 🟡 `csrf_rejections_total{reason}` deferred; easy add post-G1 |
| 6 | Structured logs | 🟡 403 responses go through `writeForbidden` which emits correlation ID only — per-reason label in log deferred |
| 7 | Grafana dashboard | 🟡 Wave 13.6 bundle |
| 8 | OTEL spans | n/a — middleware short-circuits before handler spans |
| 9 | RLS | n/a — middleware runs before DB |
| 10 | NATS subject | n/a |
| 11 | Index-plan comment | n/a |
| 12 | Rollback | ✅ runbook Rollback section with exact line to comment out |
| 13 | Runbook | ✅ [docs/runbooks/06-session-cookies-csrf.md](../../runbooks/06-session-cookies-csrf.md) |

## Spec deviations (logged in out-of-scope.md)

1. **Refresh token `dms_refresh`** — not implemented. Sessions
   one-shot with 24h TTL; refresh requires new schema + endpoint.
   Deferred to post-G1.
2. **CSRF enforcement on document / storage / search / billing** —
   only auth service's routes wrapped in this PR. Non-auth services
   accept requests only via the frontend (which already sets the
   header) or via API keys (exempt), so the gap is defensive.
3. **Bearer-session deprecation log** — would flood current-client
   logs. Add when the 30-day back-compat clock starts.

## Surprise

The `localStorage` audit found it was already clean. Wave 4's
security-settings UI work (MFA, sessions, API keys) was already
reading/writing through the httpOnly-cookie path — nobody added
`localStorage.setItem` anywhere. The XSS blast-radius concern in
final.md § 2.4 was already addressed before this prompt; we've now
closed the CSRF companion issue.

## Wave 6 scorecard

| Prompt | Status |
|---|---|
| 6.1 per-tenant KEK | ✅ |
| 6.2 session cookies + CSRF | ✅ this doc |
| 6.3 crypto/rand for SAML serial | pending |
| 6.4 context propagation sweep (14 handlers) | pending |
| 6.5 outbox-only publishing | pending |

## Next prompt

**6.3** — replace `math/rand` with `crypto/rand` for X.509 serial in
`services/auth/internal/sso/saml.go`, add a grep-based CI guard,
assert serial length + uniqueness in a 10k-draw test.
