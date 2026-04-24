# Runbook 12 — Secret rotation

Operator procedures for rotating each credential class VaultDMS holds. All
rotations run through `dms-admin secrets rotate` and emit NATS
`dms.rotation.started.v1` + `dms.rotation.completed.v1` events so SRE can
audit end-to-end timing and confirm fan-out.

## Contract

Every rotation writes the NEW secret alongside the OLD, sets a
revocation deadline (default 24 h), and emits the `completed.v1` event
with the new key-id and revoke timestamp. **The OLD secret is not
auto-revoked** — tracked in the ledger as a scheduled cron (Wave 13.5+).
Until then, operators run a revoke step manually after the window (see
§"Revoke after drain" below).

## Pre-flight

```bash
export DATABASE_URL='postgres://...vaultdms?sslmode=require'
export REDIS_URL='redis-primary.vaultdms.svc:6379'
export VAULTDMS_NATS_URL='nats://nats.vaultdms.svc:4222'

# Confirm the NATS stream accepts rotation events.
nats stream subjects --server="$VAULTDMS_NATS_URL" 2>/dev/null | grep '^dms\.rotation\.'
```

`dms.rotation.>` is covered by no dedicated stream today; the events
land under the catch-all `DOC_EVENTS` consumer. If a dedicated subject
binding is desired, add to `pkg/events.DefaultStreams` before running.

## 1. JWT signing key (global)

```bash
dms-admin secrets rotate --target jwt-signing --scope global
```

Effect:
- Redis `jwt_signing_key:new` set to 32-byte hex.
- Redis `jwt_signing_key:old_expires` = now+24 h.
- Auth service preferred read order: `jwt_signing_key:new` ➜ `jwt_signing_key` (falls back while not-yet-expired).

No pod restart required. Token-issuance layer picks up the new key on the next `EXISTS` poll (≤5 s).

**Verify**:
```bash
curl -s -o /dev/null -w '%{http_code}\n' https://api/auth/me  # expect 200 with a fresh login
```

## 2. Tenant KEK

```bash
dms-admin secrets rotate --target tenant-kek --tenant <uuid>
```

Effect:
- `tenant_keks` row inserted with `version = MAX+1` and alias `vaultdms/tenant/<uuid>@v<N+1>`.
- Prior version's `retired_at` set to `now()`.
- Storage service `GenerateDataKey` starts using the new alias; `DecryptDataKey` continues to resolve prior `@v<N>` aliases until hard-revoke.

This command is equivalent to the older `dms-admin kms rotate --tenant`
(retained) but emits the `dms.rotation.*.v1` event envelope that SRE
tooling consumes.

**Verify**: upload a test doc to that tenant, confirm `content_blobs.kek_id` matches the new alias.

## 3. API internal token (per service)

```bash
dms-admin secrets rotate --target api-internal-token --service <name>
```

Services today: `auth, policy, document, storage, search, workflow, notification, audit, signature, billing, connector, intelligence, preview, collaboration`.

Effect:
- `system_secrets` row `api_internal_token:<name>` rewritten with the new value; the old value moves to `previous_value` with `previous_expires_at = now()+24 h`.
- Target service reads `value` first, falls back to `previous_value` when the header carries the old token AND `now() < previous_expires_at`.

**Verify**:
```bash
# Hit an internal endpoint with the new header; should succeed:
curl -H "X-Internal-Token: $NEW" http://<svc>/internal/v1/health
```

## 4. Database password (per role)

```bash
dms-admin secrets rotate --target db-password --role vaultdms
```

Effect:
- `ALTER ROLE <role> PASSWORD '<new>'` executed immediately.
- **Old password STOPS WORKING at once** — Postgres has no dual-password mode.
- The CLI prints the new password fingerprint + a reminder to update the K8s secret.

**Follow-up** (MUST happen within the pool-refresh timeout to avoid 5xx):

```bash
kubectl -n vaultdms create secret generic vaultdms-db-credentials \
  --from-literal=PGPASSWORD='<new>' --dry-run=client -o yaml \
  | kubectl -n vaultdms apply -f -

# Rolling restart each deployment. HPA/PDB settings prevent cascade.
for d in $(kubectl -n vaultdms get deploy -l app.kubernetes.io/instance=vaultdms -o name); do
  kubectl -n vaultdms rollout restart "$d"
  kubectl -n vaultdms rollout status "$d" --timeout=120s
done
```

**Budget**: the K8s secret update + rolling restart should complete in <2 min. If pods hold connections longer than that via the `pgxpool` idle-timeout, tune `max_conn_idle_time` to `<30s` so reconnects pick up the new password before the pod rollout finishes.

## Revoke after drain (manual until scheduled cron lands)

After the dual-validity window elapses:

```bash
# JWT — delete the :new pointer; auth service promotes :new → primary
# on next read cycle.
redis-cli --tls -u "$REDIS_URL" RENAME jwt_signing_key:new jwt_signing_key
redis-cli --tls -u "$REDIS_URL" DEL   jwt_signing_key:old_expires

# api-internal-token — NULL out previous_value.
psql "$DATABASE_URL" -c "
  UPDATE system_secrets
     SET previous_value = NULL, previous_expires_at = NULL
   WHERE key LIKE 'api_internal_token:%'
     AND previous_expires_at IS NOT NULL
     AND previous_expires_at < now();
"

# tenant-kek — operator choice. Retired KEKs must remain resolvable
# until every ciphertext wrapped under the prior version has been
# re-wrapped. `dms-admin kms rewrap --tenant <id>` does that; today
# the rewrap is a backlog item.
```

## Rehearsal script

Run in staging before any production rotation. Asserts zero 5xx on a
continuous probe during the rotate + rolling-restart window.

```bash
#!/usr/bin/env bash
# scripts/rotation-rehearsal.sh — exits non-zero if any 5xx seen.
set -euo pipefail
probe() { while true; do
  code=$(curl -sk -o /dev/null -w '%{http_code}' https://staging.vaultdms/api/v1/health)
  [ "$code" -ge 500 ] && echo "5xx at $(date -Iseconds) code=$code" && exit 1
  sleep 1
done; }
probe &
probe_pid=$!

dms-admin secrets rotate --target jwt-signing --scope global
sleep 30
dms-admin secrets rotate --target api-internal-token --service auth
sleep 30
dms-admin secrets rotate --target tenant-kek --tenant "$STAGING_TENANT"
# db-password intentionally excluded from the rehearsal — its follow-up
# pod restart is tested in its own DR drill.

kill "$probe_pid"
echo "rehearsal passed: zero 5xx across 90s"
```

## Events

Both events carry:
- `rotation_id` — UUIDv7, also set as `Nats-Msg-Id` for JetStream dedupe.
- `target` — `jwt-signing|tenant-kek|api-internal-token|db-password`.
- `started_at` on `.started.v1`; `completed_at` + `duration_ms` + `status` on `.completed.v1`.
- Target-specific fields: `new_alias` (tenant-kek), `new_token_id` fingerprint (jwt/api), `role` (db-password), `revoke_not_before` (all windowed targets).

Access tokens and passwords themselves are **never** in the payload — only fingerprints. Operators tail:

```bash
nats sub 'dms.rotation.>' --server="$VAULTDMS_NATS_URL"
```

## Emergency stop

No in-flight rotation is multi-step at the CLI layer — each subcommand is one atomic operation. If the `completed.v1` event never fires, inspect:

- Redis writes (`:new` + `:old_expires`) for jwt-signing.
- `tenant_keks` rows for tenant-kek.
- `system_secrets.value` vs `previous_value` for api-internal-token.
- `pg_authid` for db-password (`SELECT rolname, rolpassword IS NOT NULL FROM pg_authid WHERE rolname = '<role>';`).

If the NEW write landed but the event is missing, the rotation succeeded — just the observability signal failed. Re-publishing the event by hand is acceptable.
