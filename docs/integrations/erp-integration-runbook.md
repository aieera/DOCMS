# ERP ↔ VaultDMS Integration — Operator Runbook

**Audience:** on-call engineers, support, customer success.
**Purpose:** what to do when the integration breaks, how to verify it's healthy,
and the safe procedures for routine credential rotation.

For the architectural overview, API contract, and developer onboarding, see the
[integration guide](erp-integration.md). This runbook assumes that's already understood.

---

## 1. Health check (60 seconds, no client tools needed)

Run from the DMS host. Four queries; all four should return rows.

```sql
-- 1.1 Active production API key exists
SELECT name, scopes, last_used_at
  FROM api_keys
 WHERE tenant_id = '<TENANT_ID>'
   AND scopes && ARRAY['documents:write']
   AND revoked_at IS NULL;
-- expect: at least one row, last_used_at within 24h

-- 1.2 Webhook subscription active
SELECT id, url, active, failure_count, last_success_at, last_failure_at
  FROM webhook_subscriptions
 WHERE tenant_id = '<TENANT_ID>' AND active = true;
-- expect: 1 row; failure_count=0 (or low and last_success_at is recent)

-- 1.3 Recent ERP-tagged documents flowing in
SELECT COUNT(*) AS recent_docs,
       MAX(created_at) AS latest
  FROM documents
 WHERE tenant_id = '<TENANT_ID>'
   AND custom_metadata ? 'erp_entity_type'
   AND created_at > now() - interval '24 hours';
-- expect: > 0 if the ERP has had business activity in 24h

-- 1.4 No piling-up failures
SELECT COUNT(*) AS failures
  FROM webhook_deliveries
 WHERE created_at > now() - interval '1 hour'
   AND status_code >= 400;
-- expect: 0, or a small number with a known cause
```

If all four pass, the integration is healthy. If any fails, jump to the matching
section below.

---

## 2. Common failures and remediation

### 2.1 "No active production API key"

**Symptom:** Section 1.1 returns 0 rows, or every row has `revoked_at` set.
**Impact:** ERP push fails with `401`. Sync log rows pile up as `failed`
with `last_error: "DMS POST /api/v1/storage/uploads/initiate → 401"`.

**Remediation:**
```bash
TENANT_ADMIN_PASS='…' CALLBACK_URL='https://<erp>/api/webhooks/dms' \
  TENANT_SUBDOMAIN='<sub>' bash dms-provision.sh
```
Copy the printed key into ERP Settings → External APIs → DMS → save.
Then trigger a resync for one row to confirm:
```
POST /api/dms-sync/invoice/<known-id>/resync
```

### 2.2 "Webhook subscription missing"

**Symptom:** Section 1.2 returns 0 rows. Inbound is dead — `invoices.dms_signed_at`
never gets set, no `dms.*.v1` callbacks land.
**Impact:** ERP shows stale lifecycle state. No `dms_webhook_events` rows.

**Remediation A (preferred — let the bootstrap handle it):**
Restart the ERP process. `ensureDmsSubscription` runs at boot, creates the
subscription, persists the encrypted secret to `external_apis`, sets
`process.env.DMS_WEBHOOK_HMAC_SECRET`.

**Remediation B (manual, when ERP can't be restarted):**
```bash
RESP=$(curl -sS -X POST https://<dms>/api/v1/webhooks \
  -H "Authorization: Bearer <KEY>" \
  -H 'Content-Type: application/json' \
  -d '{
    "url":"https://<erp>/api/webhooks/dms",
    "events":["dms.signature.completed.v1","dms.version.uploaded.v1",
              "dms.document.state_changed.v1","dms.document.deleted.v1"]
  }')
echo "$RESP" | jq
# Persist the returned secret to ERP (see § 4.2 for the SQL).
```

**Remediation C (the bootstrap ran but soft-failed):**
Check ERP logs for `DMS webhook bootstrap failed`. Common causes:
- `dms_callback_url` empty in `external_apis` → admin must save it via Settings UI
- DMS unreachable at boot time → admin clicks "Subscribe now" in Settings once
  DMS comes back up

### 2.3 "Receiver returns 503 — webhook receiver not configured"

**Symptom:** DMS shows webhook deliveries failing with `503`. ERP logs show
`webhook hit but DMS_WEBHOOK_HMAC_SECRET unset`.
**Impact:** Inbound events fail; DMS retries 6× over ~8 hours then DLQs.

**Remediation:** `DMS_WEBHOOK_HMAC_SECRET` is missing from the ERP process env.
- If subscription exists and `external_apis.dms_webhook_secret_encrypted` is
  populated: restart ERP — the bootstrap will decrypt and surface it.
- If neither: subscription was never created. Run § 2.2.

### 2.4 "Receiver returns 401 — invalid signature"

**Symptom:** DMS webhook deliveries fail with `401: invalid signature, reason: bad signature`.
**Impact:** Same as 2.3 but more specific — the secret on ERP doesn't match
DMS's stored secret.

**Remediation:** Rotate. The cleanest path is to delete and recreate:
1. On DMS:
   ```bash
   curl -X DELETE https://<dms>/api/v1/webhooks/<SUB_ID> \
     -H "Authorization: Bearer <KEY>"
   ```
2. On ERP: clear `external_apis.dms_webhook_subscription_id` and
   `dms_webhook_secret_encrypted`, restart. Bootstrap recreates.

If `reason: timestamp skew`, the clocks differ by > 5 min. Fix NTP on whichever
side is wrong; webhook deliveries will succeed on the next retry.

### 2.5 "ERP-tagged documents missing custom_metadata"

**Symptom:** Section 1.3 finds documents but `erp_amount_cents`, `erp_customer_id`,
etc. are absent.
**Impact:** DMS-side search by metadata returns wrong results; analytics queries break.

**Cause:** The document was pushed before the per-entity metadata builder
(`customMetadata.js`) was deployed, or `loadJoinedRows` returned a row missing
the source field (e.g. `invoice.total` null).

**Remediation:** Triggers a resync per affected row:
```sql
-- Find them
SELECT external_doc_id,
       custom_metadata->>'erp_entity_type' AS type,
       custom_metadata->>'erp_invoice_id'  AS inv_id
  FROM documents
 WHERE tenant_id = '<TENANT_ID>'
   AND custom_metadata ? 'erp_entity_type'
   AND NOT (custom_metadata ? 'erp_amount_cents');
```
Then for each, from the ERP:
```
POST /api/dms-sync/<entity_type>/<entity_id>/resync
```

### 2.6 "ClamAV unhealthy, but uploads still succeed"

**Symptom:** `docker compose ps` shows `vaultdms-clamav (unhealthy)`, but new
documents are still landing.
**Impact:** None in dev (`VAULTDMS_STORAGE_SKIP_VIRUS_SCAN=true`). In prod, this
is a healthcheck-script bug — the daemon itself is fine if `SelfCheck: Database
status OK.` appears in `docker logs vaultdms-clamav`.

**Remediation:** Restart the container; the healthcheck recovers.

### 2.7 "Sync log piling up with status='failed'"

**Symptom:** Daily count of `dms_sync_log WHERE status='failed'` keeps growing.
**Impact:** Documents not making it to DMS.

**Triage:**
```sql
SELECT entity_type, last_error, COUNT(*)
  FROM dms_sync_log
 WHERE status = 'failed'
   AND updated_at > now() - interval '1 day'
 GROUP BY entity_type, last_error
 ORDER BY COUNT(*) DESC;
```
Top error tells you which class:
| Error pattern | Section |
|---|---|
| `401: …` | 2.1 |
| `INVALID_KEY` | 2.1 |
| `network error` / `timeout` | DMS or network is down — re-run after recovery |
| `PDF render failed for …` | Bad source data (invoice without line items, missing customer). Fix the ERP row, then resync. |
| `invoice X not found` | The entity was deleted in ERP after enqueue. Mark the sync row `failed` permanently. |
| `INFECTED` (rare in dev) | Real virus or false positive — quarantine policy applies, do not retry. |

### 2.8 "Webhook deliveries failing with 5xx from ERP"

**Symptom:** Section 1.4 returns high count. `webhook_deliveries.response_body`
shows ERP 500s.
**Impact:** Lifecycle state in ERP stale; signature completion not reflected.

**Triage:** Check ERP logs for the corresponding `event_id`. Common causes:
- Handler hit an unhandled exception — the ERP server crashed/restarted mid-batch.
  Re-deliver via the DMS UI when the cause is fixed.
- ERP database connection saturation — webhook handler couldn't get a connection.
  Bump pool size or rate-limit DMS deliveries.
- Specific entity row deleted in ERP after the DMS event was emitted — handler
  returns 500 because target row vanished. Patch handlers to soft-skip when
  target is gone.

---

## 3. Routine operations

### 3.1 Rotate the DMS API key

**When:** quarterly, or any time the key may have leaked.

```bash
# 1. Mint a new key in DMS admin with the same scopes.
#    Save the plaintext.

# 2. In ERP Settings → DMS, paste the new key, save.
#    The form is "write only" — saving with the new value triggers re-encrypt.

# 3. Trigger a resync of one row to confirm the new key works.
#    Watch dms_sync_log; expect status -> synced.

# 4. Once confirmed, revoke the old key in DMS admin.

# 5. Verify no fallback to the old key for ~5 minutes (in case of cached config).
SELECT name, last_used_at FROM api_keys
  WHERE tenant_id = '<TENANT_ID>' AND revoked_at IS NOT NULL
  ORDER BY revoked_at DESC LIMIT 5;
```

Zero-downtime: ERP starts using the new key immediately; old key keeps working
until you click revoke.

### 3.2 Rotate the webhook secret

**When:** quarterly, after a suspected leak, or when 2.4 keeps recurring.

ERP-side:
```
POST /api/dms-sync/webhook/rotate
```
This calls DMS's `POST /api/v1/webhooks/{id}/rotate-secret`, saves the new
encrypted secret to `external_apis`, updates `process.env.DMS_WEBHOOK_HMAC_SECRET`.

DMS continues delivering with the new secret immediately. The old secret is
discarded — there is no overlap window, so brief 401s during the rotation are
expected and DMS retries within seconds.

### 3.3 Force-resync a single document

**When:** a sync failed but the cause is fixed, OR you need to backfill metadata
on an old doc.
```
POST /api/dms-sync/<entity_type>/<entity_id>/resync
```
Bypasses the trigger check. Pushes the current PDF + metadata. Idempotent on
both sides: the DMS-side `Idempotency-Key` makes the create-document call a
no-op replay if it's already there.

### 3.4 Backfill: resync a date range

**When:** a code change updated the metadata schema and old docs need re-indexing.
No bulk endpoint yet (see § 5 for the open item). For now, script it from a
worker REPL:
```js
const rows = await db.dmsSyncLog.findAll({
  where: { entity_type: 'invoice', synced_at: { [Op.gt]: '2026-05-01' } },
});
for (const row of rows) {
  await dmsSyncService.maybeEnqueue({
    entityType: row.entity_type,
    entityId: row.entity_id,
    documentNumber: row.document_number,
    status: 'resync',
    actorId: 'system',
  });
}
```

### 3.5 Send a test event to confirm receiver health

End-to-end check that doesn't require generating a real document:
```
POST /api/dms-sync/webhook/test
```
Calls DMS's `POST /api/v1/webhooks/{id}/test`, DMS dispatches a synthetic
`dms.webhook.test.v1` payload, your receiver returns 200 with
`handled: false, reason: "subject not subscribed"`. A row lands in
`dms_webhook_events` with status `processed`.

If the test event comes through but real events don't, the issue is between
NATS → connector delivery, not the HMAC plumbing.

---

## 4. Emergency procedures

### 4.1 DMS is down

ERP behavior:
- Push: BullMQ worker retries 5×, then marks `dms_sync_log.status='failed'`.
- Read-back: any UI hitting `view_url` gets a network error; badges still show
  cached state from `dms_sync_log`.
- Inbound: DMS retries deliveries on its side for ~8 hours.

Operator action:
- Don't touch ERP. The queue absorbs the outage.
- After recovery, bulk-resync any rows that hit max attempts (§ 3.4 with a
  filter on `status='failed' AND updated_at > <outage start>`).

### 4.2 ERP is down

DMS behavior:
- Push: not applicable (ERP isn't pushing).
- Inbound: DMS retries deliveries 6× over 8 hours, then DLQs.

Operator action:
- After ERP recovery, replay failed deliveries from the DMS admin:
  ```
  POST /api/v1/webhooks/<sub_id>/deliveries/<delivery_id>/redeliver
  ```
- Or trigger a fresh subscribe (§ 2.2) — the old deliveries are abandoned.
- Then `POST /api/dms-sync/webhook/test` to confirm end-to-end.

### 4.3 Lost the webhook secret (DB restore, key rotation gone wrong)

You'll see § 2.4 symptoms. The secret can't be retrieved — DMS doesn't reveal
it on `GET /api/v1/webhooks/{id}`. Two options:

**Option A (preserve subscription_id, just rotate secret):**
```
POST /api/dms-sync/webhook/rotate
```
Forces DMS to generate a new secret, updates ERP. Best when there are
references to the subscription_id you want to keep.

**Option B (full reset):**
```bash
# DMS side: delete the subscription
curl -X DELETE https://<dms>/api/v1/webhooks/<OLD_SUB_ID> \
  -H "Authorization: Bearer <KEY>"

# ERP side: clear and rebootstrap
UPDATE external_apis SET dms_webhook_subscription_id = NULL,
                         dms_webhook_secret_encrypted = NULL;
# Restart ERP — Phase 6 bootstrap creates a fresh one.
```

### 4.4 Both sides drifted (test/prod confusion)

You've configured the production ERP to talk to the sandbox DMS by accident, or
vice versa. Symptoms: events arrive for doc IDs the ERP doesn't know.

Action: full reset on the ERP side.
```sql
UPDATE external_apis SET dms_enabled = false;
TRUNCATE TABLE dms_webhook_events;
-- DO NOT truncate dms_sync_log — it owns the (entity_type, entity_id) uniqueness
-- that prevents duplicate pushes.
```
Then fix the config (correct base_url, key, callback_url), set `dms_enabled=true`,
restart.

---

## 5. Known limitations / open items

| Item | Workaround |
|---|---|
| No bulk resync endpoint | Script via worker REPL (§ 3.4) |
| No email alert on consecutive webhook failures | Monitor `webhook_subscriptions.failure_count` via DB query or Grafana |
| No DMS-side bulk delivery replay UI | Per-delivery `redeliver` via API |
| Per-user API keys not supported | All ERP pushes are owned by the service account; per-user audit lives in `dms_sync_log.triggered_by` on the ERP side |
| Browser-based DMS deep link assumes user has DMS access | Out of band — link opens but DMS may show 403 if user isn't in the tenant |

---

## 6. Useful one-liners

```bash
# How many invoices flowed into DMS today?
docker exec vaultdms-postgres psql -U vaultdms -d vaultdms -c "
  SELECT COUNT(*) FROM documents
   WHERE tenant_id = '<TENANT_ID>'
     AND custom_metadata->>'erp_entity_type' = 'invoice'
     AND created_at > current_date;"

# Are any sync rows failing repeatedly?
docker exec vaultdms-postgres psql -U vaultdms -d vaultdms -c "
  -- run against ERP MySQL, not DMS Postgres:
  SELECT entity_type, entity_id, attempts, last_error
    FROM dms_sync_log
   WHERE status = 'failed' AND attempts >= 3
   ORDER BY updated_at DESC LIMIT 20;"

# When did the last successful webhook delivery happen?
docker exec vaultdms-postgres psql -U vaultdms -d vaultdms -c "
  SELECT subject, MAX(delivered_at) FROM webhook_deliveries
   WHERE tenant_id = '<TENANT_ID>' AND status_code BETWEEN 200 AND 299
   GROUP BY subject;"

# Force-test signature handler (ERP side)
TS=$(date +%s) BODY='{"id":"manual","type":"dms.signature.completed.v1","data":{"document_id":"<DOC_ID>"}}'
SIG="sha256=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $2}')"
curl -X POST https://<erp>/api/webhooks/dms \
  -H "Content-Type: application/json" \
  -H "X-DMS-Signature: $SIG" -H "X-DMS-Timestamp: $TS" \
  -d "$BODY"
```
