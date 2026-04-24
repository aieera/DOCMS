# Runbook 15 — Force-change-password

**Owner:** Platform Security · **Last rehearsed:** *(fill on next on-call tabletop)*

## What it does
Wave 15.3 mandates a password change on: first login, admin reset, or after the tenant-configured expiry (default 90d). SSO-federated users are skipped. On any of those triggers, `users.must_change_password` is set; the next successful `/auth/login` returns `{require_password_change: true, one_time_change_token}` instead of a session.

## Key surfaces
| Endpoint | Purpose |
|---|---|
| `POST /api/v1/auth/change-password` | Single-use token + new password → session |
| `POST /api/v1/admin/users/{id}/force-password-reset` | Admin trigger |
| `POST /api/v1/admin/password-policy/sweep-expired` | Owner-role; flags users whose `password_expires_at` is past |

## Known gotchas
1. **One-time token TTL is 10 min.** If the user doesn't complete in time, they sign in again to receive a fresh token. The token is keyed by `sha256(token)` in Redis; `GETDEL` makes it truly single-use.
2. **Admins cannot force-reset themselves.** The handler rejects `target == actor`. If an owner needs to rotate their own password, they use self-service.
3. **SSO-federated users are immune.** Both `Login` and `ForcePasswordReset` short-circuit on `users.sso_federated=true`. Flipping that column manually is the escape hatch if an SSO provider is decommissioned.
4. **The sweeper is external.** No Temporal schedule ships in this PR — operators call `POST /api/v1/admin/password-policy/sweep-expired` from a k8s CronJob or equivalent. A native Temporal wiring is tracked as Wave 15.3 follow-up in `docs/reports/WAVE_15_PROGRESS.md`.
5. **Reuse-check window is 5.** Last 5 hashes per user are kept in `password_history`; `ChangePassword` bcrypts the candidate against each and also against the current `users.password_hash`.

## Symptoms → fixes

### User reports "change-password link expired" immediately after login
- Cause: clock skew between web + Redis, or the user hit the page twice.
- Check: Redis key `auth:pwchange:<sha256(token)>` TTL.
- Fix: have the user sign in again; a fresh 10-min token is issued.

### Admin reset doesn't propagate — user still sees old login work
- Cause: live sessions not revoked. `ForcePasswordReset` calls `RevokeAllForUser` in the same TX; if the TX rolled back, the flag is also not set.
- Check: `SELECT must_change_password, updated_at FROM users WHERE id=:id`.
- Fix: retry the endpoint; escalate if TX keeps failing (Postgres connection exhaustion most likely).

### Sweeper flags more users than expected on first run
- Cause: `password_changed_at` backfilled to `created_at`, so users created before the migration with long-running hashes trip immediately on a short `password_expiry_days`.
- Fix: confirm tenant's `organizations.password_expiry_days`; if it was set during provisioning to something aggressive, bump it and re-run.

### Prom metric `auth_password_expiries_pending` never decreases
- Cause: the gauge is a monotonic running total — the sweeper `Add(n)` per tick. It's not a true "current pending" figure; pending is derivable from `SELECT count(*) FROM users WHERE must_change_password AND NOT sso_federated`.
- Fix: update Grafana to query the DB gauge, not the sweeper counter. Tracked as follow-up.

## Forensics: "how did user X end up flagged?"
1. Pull `dms.auth.password_reset_requested.v1` from AUDIT_EVENTS with `aggregate_id = user_id` — shows admin-triggered resets.
2. Pull `dms.auth.password_expired.v1` — shows sweeper triggers.
3. Pull `dms.auth.password_changed.v1` with `reason` label — completed changes.
4. Cross-reference `password_history.created_at` for the hash history timeline.

## Backout
- `docs/backlog/out-of-scope.md` Wave 15 scope-flip can be reverted by flipping the four items back to deferred. The migration `000001_password_lifecycle.down.sql` is idempotent — columns are dropped additively, `password_history` table drops, and `organizations.password_expiry_days` is removed.
- Rolling back the service + web + migration in that order restores pre-Wave-15 behaviour with no data loss beyond `password_history` rows (intentional — hashes only).
