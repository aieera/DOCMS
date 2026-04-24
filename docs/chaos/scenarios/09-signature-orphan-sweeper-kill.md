# Chaos scenario 09 — Signature profile Delete() killed between tx1 and S3

**Related debt:** T-D-4. Validates the orphan sweeper that compensates for split-tx crashes.

## Hypothesis

When the signature-service pod is SIGKILL'd between `signature_profiles.Revoke`
(tx1) and the `S3 DeleteObject` step, the orphaned row (revoked_at set, image_ref
still populated, S3 object still present) is cleaned up by the 03:00 UTC
`signature-profile-orphan-sweep-<tenant>` schedule on the NEXT day, without
operator intervention.

## Setup

1. Healthy staging cluster with Temporal worker + signature service up.
2. `gh run rerun` any stuck schedules so the baseline is quiet.
3. Create a test tenant and a signature profile for it.
4. Confirm the corresponding S3 object exists under
   `vaultdms-signature-profiles/<tenant>/<user>/<profile>.enc`.

## Execution

1. Set up a go-delve or gdb breakpoint on the S3 DeleteObject call in
   `ProfileService.Delete` — OR, simpler, inject a deterministic
   failure by pointing `VAULTDMS_S3_ENDPOINT` at a black-hole address
   (e.g. `http://127.0.0.1:1`) for this signature pod only.
2. Issue `DELETE /api/v1/signatures/profiles/<id>` as the owning user.
3. tx1 runs (Revoke). S3 DeleteObject fails. The handler returns 5xx.
4. Inspect the DB: `SELECT id, revoked_at, image_ref FROM signature_profiles
   WHERE id = <id>`. Expect `revoked_at IS NOT NULL` AND `image_ref` still set.
5. Confirm the S3 object still exists (MinIO console or `mc ls`).
6. Revert `VAULTDMS_S3_ENDPOINT` to the real target. DO NOT touch the DB row.

## Observation window

- Wait for the next 03:00 UTC tick of
  `signature-profile-orphan-sweep-<tenant>` (or manually run one via
  `temporal schedule trigger -s signature-profile-orphan-sweep-<tenant>`).

## Expected outcome

- The row is now `revoked_at IS NOT NULL AND image_ref IS NULL`. (The
  sweeper does NOT hard-delete — compliance/audit needs the revoked_at
  trail.)
- The S3 object is gone.
- Audit stream has a `dms.signature.profile.orphan_swept.v1` event whose
  payload includes `profile_id`, `user_id`, `revoked_at`, `swept_at`,
  and the `image_ref` that was deleted.
- `internal_auth_total`/workflow metrics: the
  `SignatureProfileOrphanWorkflow` run is visible in Temporal UI with
  `Swept: 1` and no retries.

## Rollback

If the observation window reveals the sweeper did not fire:

1. Check `temporal schedule describe signature-profile-orphan-sweep-<tenant>` — the
   schedule may not exist for this tenant. Run `RegisterWave15Schedules`
   (restart the workflow service).
2. If the schedule exists but its last run errored, look at the
   workflow's failure in the Temporal UI. Common cause: signature service
   URL unset (`VAULTDMS_SIGNATURE_URL`) → activity logs a skip line.
3. Manually `DELETE` the S3 object via `mc rm` and
   `UPDATE signature_profiles SET image_ref = NULL` to restore the
   happy-path invariant.

## Signals to wire into dashboards

- `rate(temporal_workflow_failures_total{workflow_type="SignatureProfileOrphanWorkflow"}[1h]) > 0` — page.
- Daily derivative of `count(signature_profiles WHERE revoked_at IS NOT NULL AND image_ref IS NOT NULL)` — non-zero for >48h indicates a sweep failure.
