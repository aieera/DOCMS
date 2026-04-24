# Runbook 14 — Performance baseline execution

Operator procedure for running the k6 load scenarios against a staging
cluster and producing the monthly `docs/performance/baseline-YYYY-MM-DD.md`.
Pairs with the empty baseline stub at the path for the current date.

## Pre-flight

1. **Staging cluster** sized to pilot target: 3× each of auth/policy/document/storage/search, 2× each of workflow/notification/signature/billing/connector, 4× intelligence-worker, 2× collaboration, Postgres Patroni 3-node, OpenSearch 3-node, NATS 3-node, Qdrant single-node.
2. **Seed data**: `./scripts/seed/seed.sh --tenants 10 --docs-per-tenant 500` against the staging DB. Without this the search scenario returns zero hits and every "p99" is meaningless.
3. **k6 ≥ 0.49** on the runner host. The WS scenario uses `subprotocols` which older k6 lacks.
4. **Env vars** exported before any `make` target:

   ```bash
   export BASE_URL='https://staging.vaultdms.local/api/v1'
   export WS_URL='wss://staging.vaultdms.local/ws'
   export AUTH_URL='https://staging.vaultdms.local'
   export TEST_EMAIL='loadtest@acme.test'
   export TEST_PASSWORD='<load-test-admin-password>'
   export TENANT_SLUG='acme'
   export DOC_ID='<pre-seeded-doc-uuid>'
   export TENANTS=10
   ```

5. **pprof access** — services expose `/debug/pprof/` on their health port in staging (the in-prod flag that disables it is off in staging per [deploy/helm/vaultdms/values.yaml](../../deploy/helm/vaultdms/values.yaml)). Confirm with `curl -s $BASE_URL/../debug/pprof/ | head`.

## Execute

Each scenario writes `tests/load/results/<timestamp>-<target>.json` — don't overwrite; the baseline doc cross-references these filenames.

```bash
cd tests/load

# Matches brief targets:
#   Upload 500/min steady, 2000/min burst 5m  →  03-upload.js
#   Search 2000 q/s mixed 70/30               →  02-search.js
#   Auth 500 logins/s                          →  extend 06-mixed-realistic.js (see §"Auth-only slice" below)
#   Collab 1000 WS/pod                         →  08-collaboration-ws.js

make doc-crud     # ~10 min
make search       # ~15 min — set K6_VUS=2000 on the command line to hit the burst target
make upload       # ~15 min — burst phase is built-in to 03-upload.js
make ocr          # ~20 min
K6_OUT="--out json=results/$(date +%Y%m%d-%H%M%S)-collab.json" \
  k6 run -e WS_URL=$WS_URL -e AUTH_URL=$AUTH_URL -e TEST_EMAIL=$TEST_EMAIL \
         -e TEST_PASSWORD=$TEST_PASSWORD -e TENANT_SLUG=$TENANT_SLUG -e DOC_ID=$DOC_ID \
         scenarios/08-collaboration-ws.js      # ~5 min
make isolation    # ~5 min — zero cross-tenant leaks asserted inline

# Total wall clock ≈ 75 min.
```

### Auth-only slice

No dedicated auth scenario exists yet. For the 500 logins/s target either:

- (a) Extend `06-mixed-realistic.js` — raise its login-ratio arm to hit 500/s.
- (b) Write a 4-line dedicated scenario that calls `/auth/login` with a pool of seeded credentials. Acceptable for a one-off baseline run.

## Capture bottlenecks

While each scenario runs, capture a 30-second pprof CPU profile from the service under load:

```bash
# Example for the search service during 02-search:
curl -s "https://staging.vaultdms.local/search/debug/pprof/profile?seconds=30" \
     -o profiles/$(date +%Y%m%d-%H%M%S)-search-cpu.pprof

# Flamegraph:
go tool pprof -http=:9999 profiles/<timestamp>-search-cpu.pprof
# → capture the SVG from the browser view, commit it under
#   docs/performance/profiles/.
```

Repeat for `document` during upload, `policy` during permission-heavy runs, and `auth` during login burst.

## Compare to SLOs

For each scenario, open the results JSON in k6's reporter:

```bash
k6 summary results/<timestamp>-<target>.json
```

Or pipe into Grafana if the deploy has the k6-influx-grafana bridge; [deploy/monitoring/dashboards/load-test-results.json](../../deploy/monitoring/dashboards/load-test-results.json) exists for that.

SLO targets (from brief §13.2):

| SLO | Target |
|---|---|
| Search p99 | < 300 ms |
| Upload initiate p99 | < 200 ms |
| OCR end-to-end p95 | < 30 s |
| Permission check p99 | < 5 ms |
| Auth login p99 | < 200 ms |
| WS concurrent per pod | ≥ 1 000 |
| Cross-tenant leaks | 0 |

## File the baseline doc

1. Copy [docs/performance/baseline-TEMPLATE.md](../../docs/performance/baseline-TEMPLATE.md) to `docs/performance/baseline-YYYY-MM-DD.md` (or populate the stub if today's already exists).
2. Fill the Summary table — `Pass?` is ☑ only when Observed ≤ Target.
3. For every ☐ (missed SLO): file a fix PR under that row. DoD is **"every missed SLO has a tracked fix PR"**, not "every SLO passes". A ☐ with a linked PR is acceptable; a ☐ without one blocks the merge of the baseline doc.
4. Commit screenshots/flamegraphs under `docs/performance/profiles/` and link from the doc.

## Cost per 1k operations

Stripe Meter reporting (wired 2026-04-20 per [runbook 13](./13-control-plane.md)) pushes per-metric usage to Stripe. Aggregate cost per 1k ops = staging infra cost per hour × hour-fraction spent on that run, divided by total ops from k6's `http_reqs` counter. Record in the baseline doc under "Cost signal" — exact $ comes from the cloud billing export, not the k6 output.

## Gotchas

- **Cold OpenSearch** — first `02-search` run after a cluster restart shows p99 in the multi-second range because Lucene segments are unmapped. Run a warmup pass first, or discard the first 60 s.
- **k6 VU ramp** — the provided scenarios ramp from 0 → target. p99 during ramp is meaningless; read only the steady-state phase.
- **Connection pool exhaustion** during auth burst — `pgxpool` default max is 10. For 500 logins/s the auth service needs `max_conns` bumped in its pool config before the run, or the p99 will be pg-wait bound, not CPU bound.
- **Clamav** is in-path for upload; a single slow scan blocks the handler. `03-upload.js` uses small PDFs where scan is fast. Don't extrapolate upload p99 to 50 MB files.
