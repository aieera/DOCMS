# Remediation 02 — Security-Critical Fixes

**Date:** 2026-04-17
**Scope:** 5 findings from `docs/audit/04-antipatterns.md` — Fix 1–5.

---

## Summary

| Fix | Severity | File | Status |
|-----|----------|------|--------|
| 1. `math/rand` → `crypto/rand` for X.509 serial | **CRITICAL** | `services/auth/internal/sso/saml.go` | ✓ FIXED |
| 2. Session token out of localStorage | **HIGH** | `web/src/store/authStore.ts` + 3 related | ✓ FIXED |
| 3. MFA recovery codes bcrypt 10 → 12 | MEDIUM | `services/auth/internal/service/mfa.go:368` | ✓ FIXED |
| 4. Tenant-prefix Redis session/MFA keys | MEDIUM | `session.go`, `mfa.go` | ✓ FIXED |
| 5. Per-tenant KEK | MEDIUM | `services/storage/cmd/server/main.go:137` | DOCUMENTED (deferred) |

---

## Fix 1 — SAML cert serial: `math/rand` → `crypto/rand`

### Before (saml.go)

```go
import (
    ...
    "math/big"
    mrand "math/rand"
    ...
)

func NewSelfSignedSP() (*SPKeyMaterial, error) {
    key, err := rsa.GenerateKey(rand.Reader, 2048)
    ...
    tmpl := &x509.Certificate{
        SerialNumber:          big.NewInt(mrand.Int63()),
        ...
```

### After

```go
import (
    ...
    "math/big"
    // mrand import removed
    ...
)

func NewSelfSignedSP() (*SPKeyMaterial, error) {
    key, err := rsa.GenerateKey(rand.Reader, 2048)
    ...
    // X.509 serial numbers must be unpredictable (RFC 5280 §4.1.2.2).
    // Use crypto/rand with 128 bits of entropy.
    serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
    if err != nil {
        return nil, fmt.Errorf("generate serial: %w", err)
    }
    tmpl := &x509.Certificate{
        SerialNumber:          serial,
        ...
```

`rand.Reader` was already imported (line 6) from `crypto/rand`; only `math/rand` needed removal.

**Verification:** `grep -rn "math/rand" services/auth/` returns no matches.

---

## Fix 2 — Session token out of localStorage

### Before (web/src/store/authStore.ts)

```ts
import { persist } from 'zustand/middleware'

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      user: null,
      sessionToken: null,
      tenantId: null,
      isAuthenticated: false,
      login: (user, token, tenantId) =>
        set({ user, sessionToken: token, tenantId, isAuthenticated: true }),
      ...
    }),
    { name: 'vaultdms-auth' },
  ),
)
```

### After

```ts
// No persist middleware. Session token lives in an HttpOnly cookie
// set by the auth service on login; auth state is rehydrated on app
// mount by GET /api/v1/auth/me.
export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  tenantId: null,
  isAuthenticated: false,
  login: (user, tenantId) =>
    set({ user, tenantId, isAuthenticated: true }),
  ...
}))
```

### Supporting changes

- `web/src/api/client.ts` — added `withCredentials: true` so the browser sends the cookie. Removed the `Authorization: Bearer ${sessionToken}` interceptor (no longer has a token).
- `web/src/hooks/useWebSocket.ts` — removed `?token=${sessionToken}` query param; WebSocket handshake sends the cookie automatically. Gate is now `isAuthenticated` instead of `sessionToken`.
- `web/src/hooks/useAuth.ts` — `authLogin(data.user, data.tenant_id)` (no token arg).
- `web/src/routes/login.tsx` — same call-site update.

### Backend cookie (already correct)

`services/auth/internal/handler/handler.go:117-128` `setSessionCookie`:
```go
http.SetCookie(w, &http.Cookie{
    Name:     h.cookieName,
    Path:     "/",
    HttpOnly: true,
    Secure:   h.cookieSecure,  // true in prod
    SameSite: http.SameSiteStrictMode,
})
```

All four flags present. No backend changes required.

**Verification:** `grep -rn "sessionToken" web/src/` returns no matches. `grep -rn "persist(" web/src/store/` shows only `uiStore.ts` (theme + sidebar state — non-security).

---

## Fix 3 — MFA recovery bcrypt cost 10 → 12

### Before (mfa.go:368)

```go
h, err := bcrypt.GenerateFromPassword([]byte(plain[i]), 10)
```

### After

```go
h, err := bcrypt.GenerateFromPassword([]byte(plain[i]), 12)
```

### Audit of all bcrypt calls in auth

```
$ grep -rn "bcrypt.GenerateFromPassword" services/auth/
services/auth/internal/service/mfa.go:368:   ... 12)          ← fixed
services/auth/internal/service/service.go:162: ... BCryptCost)  ← BCryptCost = 12
```

All auth bcrypt calls now use cost 12.

---

## Fix 4 — Tenant-prefixed Redis session/MFA keys

### Session keys

**Problem:** `ValidateSession` hot-path receives only the token hash; it does not know the tenant until after the cache lookup, so the cache key cannot contain `tenantID` directly.

**Solution:** Two-step lookup. The token-hash → tenant indirection is global (needed for routing the read); the actual session payload is stored under the tenant-prefixed key.

#### Before (session.go)

```go
func sessionKey(hash string) string { return "session:" + hash }

func (s *Service) cacheSession(ctx context.Context, hash string, c *model.CachedSession) {
    b, _ := json.Marshal(c)
    ttl := time.Until(c.ExpiresAt)
    _ = s.rdb.Set(ctx, sessionKey(hash), b, ttl).Err()
}

func (s *Service) readCachedSession(ctx context.Context, hash string) (*model.CachedSession, error) {
    b, err := s.rdb.Get(ctx, sessionKey(hash)).Bytes()
    ...
}

// At cleanup sites:
_ = s.rdb.Del(ctx, sessionKey(hash)).Err()
```

#### After

```go
// Keys:
//   session:{tenantID}:{hash}   → JSON CachedSession
//   session_tenant:{hash}       → tenantID (hot-path indirection)
func sessionKey(tenantID uuid.UUID, hash string) string {
    return "session:" + tenantID.String() + ":" + hash
}
func sessionTenantKey(hash string) string { return "session_tenant:" + hash }

func (s *Service) cacheSession(ctx context.Context, hash string, c *model.CachedSession) {
    b, _ := json.Marshal(c)
    ttl := time.Until(c.ExpiresAt)
    pipe := s.rdb.Pipeline()
    pipe.Set(ctx, sessionKey(c.TenantID, hash), b, ttl)
    pipe.Set(ctx, sessionTenantKey(hash), c.TenantID.String(), ttl)
    _, _ = pipe.Exec(ctx)
}

func (s *Service) readCachedSession(ctx context.Context, hash string) (*model.CachedSession, error) {
    tenantStr, err := s.rdb.Get(ctx, sessionTenantKey(hash)).Result()
    ...
    tenantID, _ := uuid.Parse(tenantStr)
    b, err := s.rdb.Get(ctx, sessionKey(tenantID, hash)).Bytes()
    ...
}

// deleteCachedSession removes both keys via the indirection.
func (s *Service) deleteCachedSession(ctx context.Context, hash string) { ... }
```

All four `rdb.Del(ctx, sessionKey(hash))` call sites (ValidateSession expiry, absolute lifetime, inactive user, Logout) switched to `s.deleteCachedSession(ctx, hash)`.

### MFA keys

Same pattern:
- `mfa_session:{tenantID}:{hash}` → JSON payload
- `mfa_tenant:{hash}` → tenantID
- `mfa_attempts:{tenantID}:{hash}` → attempt counter

`VerifyMFA` and `VerifyRecoveryCode` now call `s.resolveMFATenant(ctx, hash)` to get the tenantID before reading the payload. `handleMFAFailure` signature gained a `tenantID uuid.UUID` parameter. `deleteMFASession` helper cleans up all three entries.

**Verification:** Grepping for the literal `"session:"` as a flat prefix returns only the new tenant-scoped format in `sessionKey(tenantID, hash)`.

---

## Fix 5 — Per-tenant KEK (documented, deferred)

### Code change: TODO comment only

`services/storage/cmd/server/main.go:136`:

```go
PublicUploadBase: os.Getenv("VAULTDMS_S3_PUBLIC_BASE"),
// TODO(per-tenant-kek): Single KEK across all tenants. Finding k in
// docs/audit/04-antipatterns.md; target design + migration plan in
// docs/tech-debt/per-tenant-kek.md.
TenantKEKID:      "vaultdms-storage-default",
```

### Design doc

Written to `docs/tech-debt/per-tenant-kek.md`. Covers:
- Current behavior (single shared KEK) + impact (blast radius, rotation, compliance)
- Target schema (`tenant_kms_config` table) and resolution path
- Two migration options (big-bang vs lazy live re-wrap)
- Required code changes + operational considerations

---

## Verification

```
✓ grep -rn "math/rand" services/auth/             → 0 matches
✓ grep -rn "persist(" web/src/store/              → only uiStore.ts (non-auth)
✓ grep -rn "sessionToken" web/src/                → 0 matches
✓ grep -rn "bcrypt.GenerateFromPassword.*, 10" services/auth/ → 0 matches
✓ go build ./... (pkg + services/auth + services/storage) → PASS
✓ go test ./services/auth/internal/service/...    → ok, 5.131s
✓ npx tsc --noEmit (web/)                          → exit 0, 0 errors
```

## Manual browser test — not performed

The prompt's Task 8 manual check requires `docker compose up` with
Postgres/Redis/NATS/OpenSearch running. Infrastructure is not currently
provisioned locally. What the code change guarantees:

- **HttpOnly cookie** — backend already sets all 4 flags correctly
  (verified in handler.go:117-128)
- **No localStorage `vaultdms-auth`** — `persist` wrapper is gone from
  authStore; there is no code path that writes to localStorage
- **Cookie sent on refresh** — `withCredentials: true` on axios causes
  the browser to include the cookie; `GET /api/v1/auth/me` will succeed
  while the cookie is valid

These should be re-verified by a human in staging before merging.

---

## Files changed

| File | Change |
|------|--------|
| `services/auth/internal/sso/saml.go` | Removed `math/rand` import; switched serial to `crypto/rand.Int(rand.Reader, 2^128)` |
| `services/auth/internal/service/mfa.go` | bcrypt cost 10 → 12; tenant-scoped Redis keys; new `resolveMFATenant`, `deleteMFASession` helpers; `handleMFAFailure` gained `tenantID` param |
| `services/auth/internal/service/session.go` | Tenant-scoped Redis keys; new `sessionTenantKey`, `deleteCachedSession` helpers; 4 call sites updated |
| `services/storage/cmd/server/main.go` | Added TODO comment citing the tech-debt doc |
| `web/src/store/authStore.ts` | Removed `persist` + `sessionToken` field; `login(user, tenantId)` signature |
| `web/src/api/client.ts` | Added `withCredentials: true`; removed Authorization header interceptor |
| `web/src/hooks/useAuth.ts` | Updated `authLogin` call site |
| `web/src/hooks/useWebSocket.ts` | Removed `?token=` query param; uses `isAuthenticated` gate |
| `web/src/routes/login.tsx` | Updated `authLogin` call site |
| `docs/tech-debt/per-tenant-kek.md` | **New** — target design + 2 migration options |
