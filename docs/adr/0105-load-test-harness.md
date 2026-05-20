# ADR 0105 — Load-test harness for blueprint §16 targets

Status: Accepted (harness shipped; production-scale runs are an
operator responsibility — see Deferrals)
Date: 2026-05-19
Related: blueprint §16 (scale targets), `tests/load/` (pre-existing
harness this ADR extends), ADR 0083 (search), ADR 0080 (RAG),
ADR 0071 (e-sign connectors).

## Context

§16 of the blueprint sets the following non-negotiable scale numbers
that a buyer evaluating VaultDMS will ask us to defend:

| Dimension | Target |
|---|---|
| Concurrent users per tenant | 100,000 |
| Documents per tenant | 100,000,000 |
| Sustained req/s | 10,000 |
| Burst req/s | 50,000 |
| API p95 / p99 latency | <200 ms / <500 ms |
| Search p95 / p99 | <300 ms / <1 s |
| Upload (10 MB) p95 | <2 s |
| OCR throughput per worker | 1000 pages/min |
| Error budget under chaos | <0.1% user-visible 5xx |

The repo already had a k6 harness at `tests/load/` with seven
scenarios, a Makefile, and a small chaos shell script — useful for
small environments but never wired to §16 numbers and missing the
RAG-query and concurrent-signing surfaces.

## What ships now (Phase 1 — code, not a verified run)

- **Four new k6 scenarios** living next to the existing ones:
  - `scenarios/10-browse-search-open.js` — the dominant read path,
    a single VU iteration walks workspace → search → open doc.
  - `scenarios/11-upload-workflow-approve.js` — full write path:
    create document → initiate upload → PUT → complete → version →
    start review workflow → approve.
  - `scenarios/12-rag-query.js` — ADR 0080 RAG ask, including the
    streaming-mode path.
  - `scenarios/13-concurrent-signing.js` — DocuSign-mock signature
    envelope create → send → recipient sign → complete callback.
- **`scenarios/14-mixed-realistic.js`** with the blueprint-mandated
  70/15/10/5 weighting:
  - 70% browse-search-open
  - 15% upload-workflow-approve
  - 10% rag-query
  - 5% concurrent-signing
- **`tests/load/seed.py`** — generates a 100M-document corpus across
  100 tenants. Realistic distribution (log-normal sizes, weighted
  MIME mix, tag vocabulary, folder hierarchy). Bulk inserts via
  COPY for throughput; checkpointed so a multi-day seed can resume.
- **`deploy/load-test/terraform/`** — provisions an isolated load-test
  EKS cluster, separate from prod, with a `loadrunner` node group
  sized for at least 50 vCPUs across k6 pods. Not `terraform apply`d
  in this ADR — see Deferrals.
- **`docs/load-tests/RUNBOOK.md`** — the 10-min warm-up → 20-min
  ramp → 1-hour hold → 10-min burst → 20-min cool-down protocol,
  plus the chaos overlay (pod kills every 5 min for the final
  30 min of the hold).
- **`docs/load-tests/_template/summary.md`** — what every run's
  report MUST contain. Each new run lives in
  `docs/load-tests/<YYYY-MM-DD>/summary.md`.
- **`/admin/platform/load-tests`** — admin page that vite glob-imports
  every `docs/load-tests/*/summary.md` at build time and lists them
  in reverse-chronological order. No backend endpoint — the report
  list is the file system.

## What is explicitly NOT shipping (Deferrals)

- **An actual run against §16 numbers.** This ADR ships the
  harness, not its output. Producing real p50/p95/p99 against a
  100M-doc corpus with 100k concurrent users requires:
  - 7-figure infra spend OR a sustained sandbox lease
  - Multi-day seed time (100M docs × envelope encryption ≠ free)
  - SRE on-call for the chaos overlay
  - Grafana dashboards pointed at the load-test cluster's tenant
- **Grafana dashboard JSON dedicated to the load-test cluster.**
  The existing `deploy/monitoring/` dashboards are pointed at prod;
  the load-test cluster reuses them with the `cluster=loadtest`
  label selector. A bespoke "load-test only" dashboard set is a
  Phase-2 nice-to-have when we have enough runs to want
  trend-over-time.
- **Realistic OCR throughput measurement.** The OCR worker is a
  Python service running Surya — measuring 1000 pages/min/worker
  requires real model weights and GPU/CPU contention, which the k6
  harness simulates by enqueueing OCR jobs but doesn't validate.
  The OCR-specific test stays in `scenarios/04-ocr-pipeline.js`
  and is run separately, on a node with GPU capacity.

## How a run is conducted (summary)

```
# 1. Provision the cluster (one-time per campaign):
cd deploy/load-test/terraform
terraform init
terraform apply -var environment=loadtest-2026-05

# 2. Seed the corpus (8-12 hours; resumable):
kubectl apply -f deploy/load-test/seed-job.yaml
kubectl logs -f job/dms-load-seed

# 3. Run the protocol:
cd tests/load
make run-protocol BASE_URL=https://loadtest.vaultdms.internal/api/v1 \
                  TENANTS=100 \
                  TARGET_VUS=100000

# 4. Capture:
cp results/*.json docs/load-tests/$(date +%F)/raw/
# Fill in summary.md from the template, attach Grafana PDF.
```

The full protocol — including warm-up, ramp, hold, burst, cool-down,
and chaos overlay — is in `docs/load-tests/RUNBOOK.md`.

## Why not Locust, Gatling, Vegeta?

- **k6**: already in the repo, JS-friendly, native Prometheus output,
  good distributed support via `k6-operator` on Kubernetes. Sticks.
- **Locust**: better orchestration UI but Python's GIL is the
  ceiling, distributed mode adds operational surface, no native
  Prometheus output.
- **Gatling**: best metrics, worst onboarding (Scala DSL).
- **Vegeta**: too low-level — fine for one-endpoint hammer tests,
  doesn't model a user journey (auth → browse → search → open).

## How "harness shipped, targets unverified" is honest

The status line in this ADR is "Accepted — harness shipped." The §16
targets are recorded in `docs/load-tests/_template/summary.md` as
the pass/fail gate every run measures itself against. We do not
claim §16 compliance until a run produces a summary.md that says so,
signed off by an SRE. Pretending otherwise — by, e.g., extrapolating
from a 10k-VU smoke run — is exactly the kind of "we tested at
scale" claim that breaks down in the buyer's POC.

## Open questions deferred

- **Soak tests beyond 1 hour.** §16 doesn't mandate a duration but
  buyers typically ask "did you run it overnight?". The harness
  supports `HOLD_DURATION=12h` but no run has used it.
- **Tenant noisy-neighbor isolation.** The mixed-realistic scenario
  spreads VUs evenly across 100 tenants. Phase 2: an "unfair"
  scenario where one tenant pulls 30% of the load, to validate
  per-tenant quota enforcement (rate limits, OPA budgets).
- **EU-region latency.** All current scenarios assume the load
  cluster is in the same region as the SUT. Phase 2: cross-region
  scenarios with the runner in eu-west-1 and the SUT in us-east-1.
