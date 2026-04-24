# Chaos 07 — OCR worker kill at mid-batch

## Premise

Kill the `intelligence-worker` pod once a 100-document OCR batch has
processed ~50% of expected pages. The hardening contract from
[services/intelligence/app/tasks/ocr.py](../../../services/intelligence/app/tasks/ocr.py)
(at-least-once + dedupe via `ocr_processed_events` PK + Celery
`acks_late` + JetStream redelivery) must hold:

- Exactly 100 `dms.ocr.completed.v1` events end up on `INTEL_EVENTS`.
- No duplicate `ocr_processed_events` rows (structurally guaranteed by
  PK `(tenant_id, event_id)`; assertion exists to catch schema drift).
- No `dms.version.uploaded.v1` message stays unacked > 5 min after pod
  recovery (queue not blocked).
- No document ends in `status='enqueued'` after the batch completes
  (no lost docs).

## Setup

- Chaos-mesh CRD: `PodChaos` with `action: pod-kill`, `mode: one`,
  selector `app.kubernetes.io/name=intelligence-worker`.
  Apply once when trigger condition fires (see Verification).
- k6 driver: scenario [04-ocr-pipeline.js](../../../tests/load/scenarios/04-ocr-pipeline.js)
  parameterised to upload 100 distinct PDFs (~20 pages each, ~2000
  total pages) into a single chaos test tenant.
- Worker concurrency: `ocr_per_tenant_cap=8` (default), so the kill
  lands while up to 8 docs are in flight; their pages will be reprocessed
  by the replacement pod and dedupe MUST suppress the second emit.

## Trigger

Kill the pod the first time `ocr_pages_total` (the Prometheus counter
exported by `services/intelligence/app/metrics.py`) crosses
~1000 pages — i.e. ~50% of the expected 2000.

```
expected_pages=2000
target=$((expected_pages / 2))
while true; do
  current=$(curl -s "${PROM_URL}/api/v1/query?query=ocr_pages_total" \
    | jq -r '.data.result[0].value[1] // 0' | cut -d. -f1)
  [ "${current:-0}" -ge "$target" ] && break
  sleep 2
done
kubectl -n "$NS" delete pod -l app.kubernetes.io/name=intelligence-worker \
  --grace-period=0 --force
```

## Verification

Run after the batch settles (give the redelivered messages 5 min budget
to drain after the kill).

### 1. No duplicate completions

```sql
-- Should return 0 rows. The PK on (tenant_id, event_id) makes >1
-- structurally impossible; this query catches PK regression.
SELECT event_id, COUNT(*)
FROM   ocr_processed_events
WHERE  tenant_id = :chaos_tenant
GROUP  BY event_id
HAVING COUNT(*) > 1;
```

### 2. Exactly 100 completed events on the stream

```bash
nats stream info INTEL_EVENTS --json \
  | jq '.state.subjects["dms.ocr.completed.v1"] // 0'
# Expect: 100
```

PromQL equivalent (publisher-side counter):

```
sum(increase(ocr_documents_total{status="completed",tenant=~"chaos-.*"}[15m])) == 100
```

### 3. No documents stuck in `enqueued`

```sql
SELECT COUNT(*) FROM ocr_processed_events
WHERE  tenant_id = :chaos_tenant AND status = 'enqueued'
  AND  processed_at < now() - interval '5 minutes';
-- Expect: 0
```

### 4. Queue not blocked

```
# JetStream consumer pending == 0 within 5 min of pod recovery.
nats consumer info INTEL_EVENTS intelligence-ocr --json \
  | jq '.num_pending'
# Expect: 0
```

### 5. Pod recovered

```
kubectl -n "$NS" get pods -l app.kubernetes.io/name=intelligence-worker \
  -o jsonpath='{.items[*].status.containerStatuses[*].ready}'
# Expect: every value true within 60 s of kill.
```

## Expected behaviour

- Kill SIGKILLs the worker; in-flight Celery tasks (up to 8) never
  emit `dms.ocr.completed.v1` because the publish is the last step.
- `acks_late=True` means the broker (Redis or NATS depending on
  Celery transport) holds the message; on worker restart it
  redelivers.
- Replacement pod re-runs OCR on those docs. The `ocr_processed_events`
  insert is the dedupe gate: rows from the first attempt have
  `status='enqueued'`. The second attempt's `mark_completed` UPSERT
  flips them to `completed` and emits exactly once.
- Per-tenant semaphore in the killed pod is gone; the new pod's fresh
  semaphore lets the redelivered docs through immediately.

## Expected failure modes (log but don't fail)

- A short spike in `ocr_dedupe_hits_total` as the redelivered subset
  hits the dedupe gate. Expected; it's the proof the contract works.
- `ocr_duration_seconds` p95 climbs for the redelivered subset
  because they restart from page 1. Tolerated.

## Fail-the-test triggers

- Any verification step 1–4 returns the wrong value.
- Pod doesn't return to `Ready` within 60 s of the kill.
- Total wall-clock for the batch exceeds 10 min (the un-chaos baseline
  from scenario 04 is ~3 min for 100 × 20-page docs).

## Post-mortem template

See [../post-mortem-TEMPLATE.md](../post-mortem-TEMPLATE.md).
