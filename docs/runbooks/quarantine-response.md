# Runbook — Upload quarantine response

Audience: on-call + platform admins. Triggered by:

- Alert: `storage_quarantine_events_total` non-zero over 5 min.
- Admin UI banner: "File quarantined" notification (source: `dms.notify.quarantine.v1`).
- User report: "my upload came back `413 / 503 / quarantined`".

## Event shape

Every quarantine writes three things in one DB tx (services/storage/internal/service/service.go CompleteUpload):

1. `upload_sessions.status = 'quarantined'`
2. `scan_results(result='infected', signature=...)`
3. `quarantine_events(reason, declared_mime, detected_mime, storage_bucket, storage_key)`

…and emits two NATS events:

- `dms.storage.upload_quarantined.v1` — the canonical aggregate event.
- `dms.notify.quarantine.v1` — fan-out to the notification service; surfaces in the admin panel and (if configured) Slack.

## Triage by `reason`

| `reason`        | What it means                                                  | Expected action                                                                 |
|-----------------|----------------------------------------------------------------|---------------------------------------------------------------------------------|
| `virus`         | ClamAV flagged the bytes. `signature` is the virus name.        | Real malware — keep object in quarantine bucket. Notify user + security.       |
| `blocked_mime`  | Server-detected MIME is in the executable deny-list.           | Likely benign misnamed file OR an attacker smuggling an exe. Inspect manually. |
| `mime_mismatch` | Declared Content-Type disagrees with detected type (rare path).| Only present when the mismatch itself triggered quarantine — usually combined with `blocked_mime`. |

## 503 "virus scanner unavailable"

New as of the Blueprint §22 fail-closed flip. The service rejects uploads when ClamAV is unreachable.

1. Check clamd health: `docker exec vaultdms-clamav clamdscan --ping` or ping the TCP port directly.
2. If clamd is down, restart: `docker compose restart clamav`.
3. Check signature freshness: `clamav_scan_result_total{result="error"}` climbing without a matching `unavailable` label suggests signature db corruption, not a connectivity outage — run `freshclam` against the container.
4. If outage will exceed the SLA, set the documented break-glass feature flag (`VAULTDMS_STORAGE_SCAN_REQUIRED=false`) — audited, never default.

## Recovering a quarantined upload

```sql
-- Find it
SELECT qe.*, u.created_by, u.filename
FROM quarantine_events qe
JOIN upload_sessions u ON (u.tenant_id, u.id) = (qe.tenant_id, qe.upload_id)
WHERE qe.tenant_id = :tenant
ORDER BY qe.created_at DESC LIMIT 20;
```

If the bytes turn out to be legitimate (false positive, e.g. a dev zipping an installer for an internal share):

1. Copy the object from the `dms-quarantine` bucket to the hot bucket:
   `aws s3 cp s3://dms-quarantine/infected/<tenant>/<key> s3://dms-<region>-hot/<key>`
2. Update `upload_sessions.status = 'completed'` and backfill the `content_blobs` row (use the SHA-256 recorded on the scan).
3. Re-emit `dms.storage.upload_completed.v1` with the same `upload_id` and `content_blob_id` so downstream search/OCR pick it up.

Do NOT skip step (3) — downstream services don't watch `upload_sessions` directly.

## Why we don't auto-release

False-positive releases are rare and high-blast-radius (re-admitting malware). A manual step with an audit trail is the right default. If release rates climb, revisit by adding a `quarantine_events.reviewed_by` column + an admin UI flow.

## See also

- ADR 0033 — security CI gates (SAST/dep/DAST/secret scan).
- `docs/security/ci-security-gates.md` — gate-by-gate operator reference.
- Metrics: `storage_quarantine_events_total`, `clamav_scan_result_total`, `storage_mime_mismatch_total`, `storage_mime_rejected_total`.
