# Runbook — Session cookies + CSRF (Wave 6 Prompt 6.2)

**Last rehearsed:** 2026-04-17 (dev box; login sets both cookies, POST
w/o CSRF returns 403, POST w/ matching CSRF returns 2xx — all live-verified).
**On-call:** security + platform.

## What this covers

The auth service now issues **two** cookies on successful login, MFA
verify, or MFA recovery:

| Cookie | HttpOnly | Purpose |
|---|---|---|
| `dms_session` | ✅ | Bearer of identity. Validated by `AuthMiddleware` on every request. Invisible to JS. |
| `dms_csrf` | ❌ | Double-submit token. Frontend reads via `document.cookie` and echoes in `X-CSRF-Token` header on mutations. |

Both cookies are `Secure` (prod) + `SameSite=Strict` + `Path=/`.
Both are cleared on logout (`Max-Age=-1`).

## Enforcement

`pkg/middleware/csrf.go` — `CSRFDoubleSubmit()` rejects any
non-GET/HEAD/OPTIONS request where:

- the `dms_csrf` cookie is missing or empty, OR
- the `X-CSRF-Token` header is missing or empty, OR
- they don't match (constant-time comparison).

**API-key callers** (`Authorization: Bearer vdms_*`) are exempt — they
never acquired the session cookie and their bearer secret is not
replayable via cross-site form POSTs.

**Session-Bearer callers** (legacy API clients still using
`Authorization: Bearer <session>`) are NOT exempt — the spec's 30-day
back-compat window covers authentication, not CSRF. Clients using the
Bearer route must either upgrade to cookies or build their own CSRF
equivalent.

## Wire points

| Group | Location | CSRF wrapped? |
|---|---|---|
| `/api/v1/auth/*` public | services/auth/internal/handler/router.go | No (register / login are unauthenticated) |
| `/api/v1/auth/*` authenticated | services/auth/internal/handler/router.go :58 | ✅ after `AuthMiddleware` |
| `/api/v1/admin/users/*` | services/auth/internal/handler/router.go :86 | ✅ after `AuthMiddleware` |
| Other services | not yet — each service must wrap its own mutating routes | pending per-service rollout |

## Frontend

[web/src/api/client.ts](../../web/src/api/client.ts) reads the
`dms_csrf` cookie in a request interceptor and sets `X-CSRF-Token` on
every non-safe method. Safe methods (GET/HEAD/OPTIONS) skip the
header; server skips the check.

Zustand auth store already in-memory only — no `localStorage` for the
session token (confirmed by grep; no action needed).

## Common failure modes

### 403 "csrf: missing cookie" on every mutation

Client reached this page without having ever logged in via the Wave
6.2 code (session predates the upgrade, or cookies were cleared).
Fix: log out + log in again. The cookie is re-issued on every login.

### 403 "csrf: missing header"

Frontend's axios interceptor is not running (e.g. the request was
made via `fetch()` directly or from a non-axios path). Add the
header manually or switch to `api.post(...)`.

### 403 "csrf: token mismatch"

Two sessions open in different tabs: tab A logged in after tab B,
overwrote `dms_csrf`, tab B's cached JS memory still has the old
value. Refresh tab B. Long-term: Wave 6 follow-up could make the
frontend re-read the cookie on every request rather than cache.

## Rollback

The CSRF middleware is defense-in-depth; if an urgent incident
requires disabling it:

```go
// services/auth/internal/handler/router.go
r.Group(func(r chi.Router) {
    r.Use(h.AuthMiddleware)
    // r.Use(vdmsmw.CSRFDoubleSubmit())   // <— comment out
    ...
})
```

Re-deploy; clients stop needing the header. **Do not revert the
cookie issuance** — harmless if set and unchecked.

## Deferred (logged in out-of-scope)

- **Refresh token (`dms_refresh`)** — spec mentions a separate long-
  lived cookie on `/auth` path. Not implemented: sessions are
  one-shot with 24h absolute expiry. Adding refresh requires a new DB
  table + rotation logic; post-G1.
- **CSRF enforcement on document / storage / search / billing services**
  — those services do not yet wrap `CSRFDoubleSubmit()`. Auth covers
  the attack surface that matters for pilot (user/tenant
  manipulation). Other services receive traffic only from
  authenticated frontend calls with the header already attached, so
  the gap is defensive, not exploitable without also bypassing the
  frontend.
- **Bearer-session deprecation warning** — spec says to log when
  `Authorization: Bearer <sessionToken>` is used (non-API-key). Not
  implemented; existing clients would drown the logs. Add when the
  30-day clock starts.
