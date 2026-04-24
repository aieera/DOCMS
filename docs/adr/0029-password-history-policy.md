# ADR 0029 — Password history policy for force-change-password

* Status: Accepted
* Date: 2026-04-21
* Wave: 15.3

## Context

Wave 15.3 enforces password rotation on first login, admin reset, and tenant-configurable expiry. The DoD requires that a new password be rejected if it matches any of the user's last 5 prior passwords. We need to decide:

1. **Storage format** — bcrypt hash, HMAC, plaintext (obviously no).
2. **Retention window** — how many prior hashes per user, and how rows are trimmed.
3. **Reuse-check surface** — bcrypt-compare each or equality on digest.
4. **Deletion semantics** on user delete.

Constraint from Wave 15 brief: no new third-party dependencies without a prior ADR. HIBP client and zxcvbn scorer are both logged as Wave 15.3 follow-ups requiring their own ADRs.

## Decision

### Storage
Store **bcrypt cost-12 hashes** in `password_history(tenant_id, user_id, id, password_hash, created_at)`. Same cost as `users.password_hash` — the reuse check must use the same bcrypt cost or an attacker could derive the policy by timing side-channel. Hashes are not reversible; reuse-check is `bcrypt.CompareHashAndPassword(prior, candidate)` per prior row.

### Retention
**Keep the 5 most recent hashes per (tenant, user).** On `ChangePassword`, we `INSERT` the new hash and `DELETE` all rows outside the top-5-by-`created_at`. Ring-buffer behaviour without a counter column; `DELETE … NOT IN (SELECT … LIMIT 5)` is atomic inside the surrounding tenant TX.

The number 5 is common in SOC2/HIPAA/PCI-DSS guidance and matches the brief. Going higher costs tenant-row bloat; lower trips audit findings. Kept as a compile-time constant `PasswordHistoryKeep` so a future ADR can raise it without a migration.

### Reuse-check surface
Bcrypt-compare against:
1. The current `users.password_hash` (catches "change to same password").
2. The top-N rows of `password_history` in `created_at DESC` order.

No equality-on-digest shortcut — every row requires a fresh bcrypt compare. Cost is bounded at N+1 = 6 bcrypts per change attempt, which at cost 12 is <1 s end-to-end on current hardware and is gated by the 5-per-minute `/change-password` IP rate limiter.

### Deletion semantics
`password_history` has `ON DELETE CASCADE` from `users(tenant_id, id)`. Hard-delete of a user removes their hash history automatically. Soft-delete (`users.deleted_at`) does not touch history — rejoining a reinstated user keeps them blocked from reusing the pre-departure password, which is the intent.

### RLS
`password_history` has `FORCE ROW LEVEL SECURITY` with the same `current_setting('app.current_tenant')::uuid` policy as `users`. All reads/writes go through `database.WithTenantTx`. A cross-tenant history leak would be a P0 per the brief's stop conditions.

## Consequences

**Positive**
- No plaintext ever stored. No hash-of-hash indirection. No second crypto primitive to audit.
- Ring-buffer is self-limiting; no sweeper job needed.
- Reuse-check is the same bcrypt primitive already trusted by the service — no new code surface to review.

**Negative**
- 6 bcrypts per change attempt at cost 12 is a visible latency hit. Rate-limiter mitigates abuse.
- History is opaque to ops — you can't distinguish "was this the password on 2026-02-14?" without trying to bcrypt-compare, which is infeasible without the plaintext. That's by design; pivoting to an equality-index on a separate digest would weaken the property.

**Neutral**
- If a future ADR raises `PasswordHistoryKeep` to N, older rows are not backfilled — users start accumulating N rows only after the constant changes. Acceptable.

## Alternatives considered

1. **HMAC-SHA256 with per-tenant KEK-wrapped key** — allows equality comparison (faster). Rejected: introduces a second key-management surface (rotation, re-wrap on KEK roll), and the bcrypt path is already trusted. Latency difference is <1 s per change attempt — not worth the complexity.
2. **Store plaintext encrypted with per-tenant KEK** — allows old-password recovery for UX ("we know this is the one you used last year"). Rejected outright: any path where plaintext old passwords can be recovered is a SOC2/ISO-27001 finding.
3. **Compare only against the current hash** — DoD explicitly wants the last 5.
4. **Compare against the last N via a digest column plus bcrypt on mismatch** — added state, same attack surface as (1).

## Follow-ups

- HIBP-compromised-password check at change-time. Requires either a cached breach-corpus file (GB-scale) bundled into the container, or a network call to the HIBP k-anonymity endpoint. Each has an ADR's worth of tradeoffs — tracked in `docs/backlog/out-of-scope.md`.
- zxcvbn ≥ 3 score gate. Adds `github.com/trustelem/zxcvbn` or equivalent; needs its own ADR per the Wave 15 no-new-deps constraint.
- Temporal-native expiry sweeper (current design calls out to an admin HTTP endpoint from an external scheduler).
