# ADR 0123 — Session revocation must beat the Redis fast path

Date: 2026-07-12
Status: accepted

## Context

`ValidateSession` serves the per-request hot path from a Redis cache
(`session:{tenant}:{hash}`) and, on a hit, returned the cached identity
**without ever re-checking `revoked_at` or the user's status**. The
cache TTL equals the session lifetime (24h). So an admin "revoke
session" or "suspend user" — a security control — only marked the
Postgres row; the cached copy kept authorizing requests for up to 24
hours. Revocation didn't work.

Two independent gaps compounded it:
- revoke/suspend paths updated Postgres but never cleared the cache, and
  there was **no index** from a user to their cached token hashes to
  clear;
- `SuspendUser` revoked sessions but **not** the user's API keys
  (`ValidateAPIKey` checks the key's own `revoked_at`, never the owner's
  status), so a suspended user kept programmatic access.

## Decision

**1. Active invalidation via a user→sessions index.** Every cached
session is also registered in a Redis SET
`user_sessions:{tenant}:{user}`. Revoke / suspend / MFA-reset /
revoke-all now call `invalidateUserSessions`, which reads that set and
deletes each session's cache entries immediately. The Postgres
revocation stays the source of truth; this stops the cache from masking
it. Effect is enforced within seconds.

**2. Bounded fast-path trust window (defense in depth).** Cached entries
carry `LastChecked`. The fast path is trusted only while
`now - LastChecked < FastPathRevalidateInterval`; past that it falls
through to the Postgres path (which filters `revoked_at` and re-checks
user status) and re-primes. So a **missed** active invalidation — a
Redis blip, a lost index, a future code path that forgets to call it, or
the SCIM deprovision path below — self-heals within the window instead
of lingering for the full 24h.

`FastPathRevalidateInterval = 3 minutes.` Rationale: it bounds the
worst-case staleness of a missed invalidation to 3 minutes, while the
cost is at most one extra Postgres session read per active token per 3
minutes. Against a hot path that otherwise serves from Redis on every
request, and with a per-user concurrent-session cap of 5, that overhead
is negligible. Legacy cache entries written before this field have a
zero `LastChecked`, which reads as "never checked" and forces an
immediate revalidation on first use after deploy — so the fleet
self-corrects on rollout.

**3. Deactivation revokes API keys.** `SuspendUser` now also bulk-revokes
the user's API keys (`api_keys.RevokeAllForUser`) in the same
transaction. The SCIM deprovision path already revoked both sessions and
keys in Postgres.

## Scope note — the SCIM path

`scim.Repo.DeactivateUser` (IdP-driven deprovisioning) revokes sessions
+ keys in Postgres but lives in a package with no Redis handle, so it
does not actively invalidate the cache. It is covered by the self-heal
window (item 2): a SCIM-deactivated user's cached session stops
authorizing within `FastPathRevalidateInterval`. Wiring active
invalidation there is a follow-up (pass the auth Service or a Redis
handle into the SCIM layer); the 3-minute bound made it non-urgent.

## Consequences

- Admin revoke/suspend takes effect within seconds (active), and any
  path that only touches Postgres self-heals within 3 minutes.
- The hot path stays a Redis read for the 3-minute window; the added
  Postgres reads are bounded and rare.
- Tests in `session_revocation_integration_test.go` pin: revoke → next
  fast-path read is 401; suspend → all sessions + API keys dead; the
  self-heal window forces revalidation of a still-cached revoked
  session; and the cache-miss fallback still authorizes a valid session.
