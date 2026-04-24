# Runbook 15 — Acknowledgement campaigns

**Owner:** Compliance Engineering · **Last rehearsed:** *(fill on next on-call tabletop)*

## What it does
Wave 15.1 lets a compliance officer distribute a policy document to a group of users, require each to click "I have read and understood", and produce a tamper-evident report per campaign. Attestations use a per-tenant HMAC signing key + per-campaign hash chain. See ADR 0027 for the design.

## Key surfaces
| Endpoint | Purpose |
|---|---|
| `POST /api/v1/acknowledgement/campaigns` | Create (optionally `activate=true`) |
| `GET  /api/v1/acknowledgement/campaigns` | List (tenant-scoped) |
| `GET  /api/v1/acknowledgement/campaigns/{id}` | Read one |
| `POST /api/v1/acknowledgement/campaigns/{id}/close` | Close |
| `GET  /api/v1/acknowledgement/campaigns/{id}/report` | Rollup + ack rate |
| `GET  /api/v1/acknowledgement/my` | Current user's pending |
| `POST /api/v1/acknowledgement/assignments/{id}/ack` | Acknowledge |

## Known gotchas
1. **Recipient resolution is user-list only today.** The schema stores `recipient_policy` as JSONB with `{groups,users,roles}` but the default `StaticRecipientResolver` rejects group/role fields with a validation error. A follow-up PR wires the auth-service resolver that expands groups / roles → user IDs; until then callers must enumerate users explicitly.
2. **Reminders + escalation are not wired.** Temporal schedule is a Wave 15.1 follow-up (see `docs/reports/WAVE_15_PROGRESS.md`). Operators who need "remind in 3d" today must run the logic externally and call the sweeper endpoint — which itself is a follow-up. The schema + outbox subjects (`dms.acknowledgement.reminded.v1`, `.escalated.v1`) already exist so downstream consumers can bind early.
3. **Attestation key is generated on first acknowledgement per tenant.** If you need a tenant to have a signing key before the first ack (e.g., to export the wrapped-key for DR), call any ack-adjacent endpoint with a forced error path, or add a `dms-admin acknowledgement ensure-key --tenant <id>` command (also tracked as follow-up).
4. **Assignment list endpoint is stubbed.** `GET /campaigns/{id}/assignments` returns 403 by design today — exposing the full list leaks IP + user-agent for every acknowledger, which is PII-adjacent. The export bundle path (not yet shipped) is the proper surface.
5. **Idempotent ack** — re-POST of `/ack` on an already-acknowledged assignment returns 409, not a silent success. Frontend must treat 409 on this route as a "refresh your state" signal.

## Symptoms → fixes

### Chain verification fails after an apparent DB hand-edit
Expected — that's the whole point. Identify the divergent row:
```sh
dms-admin acknowledgement verify-chain --tenant <id> --campaign <id>
```
(sub-command is a Wave 15.1 follow-up; until then, export `acknowledgement_events` rows ordered by `created_at` and replay SHA-256 by hand per ADR 0027).

### HMAC verification fails for a single row
Either: (a) the tenant signing key was rotated and the old plaintext was lost, OR (b) the row was edited. Check `acknowledgement_signing_keys.rotated_at`. If no rotation, the row is tampered — escalate as a security incident.

### Outbox event subjects not arriving at subscribers
The outbox publisher drains this service's `outbox` table into NATS JetStream. Check:
1. `SELECT count(*) FROM outbox WHERE NOT published` — if growing, publisher is stuck.
2. Publisher logs for NATS connection errors.
3. JetStream stream filter must include `dms.acknowledgement.>` (the Wave-5 test `§3.2/A3` guards this in CI).

### Ack returns 403 for the rightful assignee
Defence-in-depth: the service checks `assignment.assignee_user_id == ctx.user.id` even if the token / session is valid. If this fires for a legitimate user, either the assignment was created for a different identity (compare `assignee_user_id` to `users.id` — not `email`) or the user is acting on a token minted for a different account (shared session bug).

## Forensics: "did user X really acknowledge at time T?"
1. Fetch the assignment row. Note `attestation_hash`, `acknowledged_at`.
2. Fetch the tenant signing key (wrapped blob from `acknowledgement_signing_keys`, unwrap via the KEK).
3. Recompute `HMAC-SHA256(key, campaign_id || "|" || user_id || "|" || RFC3339Nano(acknowledged_at))`.
4. If the bytes match, the attestation is genuine. If not, either the timestamp or user id is wrong, or the row was tampered.

Do this step-by-step before escalating — clock-skew between replicas has occasionally produced nanosecond-off timestamps that break the HMAC; the fix is to use the stored `acknowledged_at` exactly, not round-trip through a timestamp parser.

## Backout
The migration's `.down.sql` drops all four tables (`acknowledgement_*` + `outbox`). Acknowledgement history is destroyed. Do NOT run in prod without first exporting the chain bundles you care about.
