# Performance runbook

Wave 13.2. The k6 scripts under [tests/load/scenarios/](../../tests/load/scenarios/)
have existed since the blueprint but were never run against a
realistic cluster. This runbook is what an SRE follows to change
that.

## Target SLOs (from spec §13.2)

| Surface | SLO | Scenario file |
|---|---|---|
| Upload initiate | p99 < 200 ms @ 500 docs/min steady, 2000 docs/min 5-min burst | `03-upload.js` |
| Search | p99 < 300 ms @ 2000 q/s, 70% lexical / 30% semantic | `02-search.js` |
| OCR | p95 < 30 s end-to-end | `04-ocr-pipeline.js` |
| Permission check | p99 < 5 ms | covered inside `01-document-crud.js` |
| Auth login | p99 < 200 ms @ 500 logins/s | not yet scripted — see § "Backlog" |
| Collab WS | 1000 concurrent connections per pod | `05-websocket.js` |
| Cross-tenant isolation | zero leaks across any scenario | `07-cross-tenant-isolation.js` |

## Pre-flight

1. **Staging cluster sized to pilot target.** Sizing: 2 doc
   service pods (4 vCPU / 8 GiB), 3 storage pods, 2 policy, 2
   search, 1 workflow, 1 notification. Postgres: `db.r6g.xlarge`
   or equivalent (4 vCPU / 32 GiB). NATS: 3-node cluster.
2. **Seed data.** Run [scripts/seed/](../../scripts/seed/) to
   populate 10 tenants × 500 documents each. The k6 scripts
   expect tenant ids in `tests/load/lib/config.js`; regenerate
   from the seed output.
3. **Observability paths open.** Prometheus + Grafana reachable
   from the k6 runner so flamegraphs + RED panels collect during
   the run.
4. **Kill switch.** Exposed staging means runaway k6 can cost
   real money. Confirm the staging cluster has autoscaling caps
   (MaxReplicas ≤ 10× baseline).

## Executing

```bash
cd tests/load
BASE_URL=https://staging.vaultdms.example.com/api/v1 \
WS_URL=wss://staging.vaultdms.example.com/ws \
TENANTS=10 \
make all
```

`make all` runs the five production-critical scenarios
(doc-crud, search, upload, mixed, isolation). Each emits a
`results/YYYYMMDD-HHMMSS-<scenario>.json` file with per-stage
RED data.

Runtime budget: ~90 minutes wall-clock for the full suite.

## Capture + report

After the run, author `docs/performance/baseline-YYYYMMDD.md`
using the template below. Attach:

- Grafana screenshots of the storage-service RED panel and DB /
  NATS / Redis USE panels during each scenario.
- Top-10 slowest endpoints from k6's summary.
- Per-scenario p50/p95/p99 latency vs. the SLO target.
- Cost per 1 000 operations (cloud billing export; bucket by
  service).

If any SLO misses, file a fix PR before closing the run.
Spec §13.2 DoD: every missed SLO has a tracked fix PR.

## What this runbook does NOT do

- **Run in CI.** Load tests are expensive and destructive; they
  belong on a pre-production cluster, run by an SRE, not on a
  per-PR GitHub Actions runner. CI lints the k6 scripts (see the
  `k6-lint` job in `.github/workflows/ci.yml`, added in Wave 13.2)
  but doesn't execute them.
- **Cover the full performance envelope.** These scenarios are
  scoped to the blueprint's p99 SLOs. Long-tail edge cases
  (100 GB document upload, pathological JSON Schema validator)
  live under `tests/load/chaos/` and run as part of the
  chaos suite (Wave 13.3).

## Backlog (logged in out-of-scope.md)

- Auth login scenario (§SLOs table) — not yet scripted.
- Continuous baseline — a weekly cron that runs `make all`
  against staging and diffs the latest baseline JSON against
  the committed `baseline-<date>.md` expectation. Burns budget
  automatically if a regression > 10%.
- Multi-region load — the current scripts all hit one region.
  Post-ADR-0026 multi-region deployments need a separate matrix.

## Related

- [tests/load/scenarios/](../../tests/load/scenarios/) — k6 scripts
- [tests/load/lib/config.js](../../tests/load/lib/config.js) —
  shared tenant IDs + probability distributions
- [tests/load/Makefile](../../tests/load/Makefile) — runner entry
