# Remediation 20b — Wave 13.2: Load tests runbook + k6 lint

**Date:** 2026-04-18
**Wave:** 13.2 · converts the 7 pre-existing k6 scenarios from
"committed but never run" to "reviewed, lintable, documented."

## Recon

`tests/load/scenarios/` already held 7 k6 scripts + a Makefile
from the original blueprint. None of them had:

- A runbook describing how to actually execute against a
  staging cluster.
- A baseline template operators fill in post-run.
- Any CI coverage (so a broken script would only be caught when
  an SRE tried to run it).

Spec §13.2: "k6 scenarios exist but have never been run…
Execute against a staging cluster sized to pilot target."
Running against a real cluster is an operator activity, not a
deliverable we ship from an AI pass — but we can make the
runbook executable and gate the scripts on lint.

## What shipped

### Performance runbook

[docs/performance/README.md](../../performance/README.md):

- SLO → scenario table (upload p99 <200ms, search p99 <300ms,
  OCR p95 <30s, permission p99 <5ms, 1k WS/pod).
- Pre-flight checklist (staging sizing, seed data, observability,
  kill switch).
- Executing recipe: `make all` from `tests/load/` with the four
  env vars the scripts read.
- Post-run capture: Grafana screenshots, top-10 slowest endpoints,
  per-scenario p50/95/99 vs. SLO, cost per 1 000 ops.
- What the runbook does NOT do: run in CI, cover the long tail.

### Baseline template

[docs/performance/baseline-TEMPLATE.md](../../performance/baseline-TEMPLATE.md)
— copy-and-fill scaffold for each dated run. Includes the
summary table with target / observed / pass / fix-PR columns,
per-scenario details, cost table, bottlenecks + fixes, flamegraph
attachments.

### CI `k6-lint` job

[.github/workflows/ci.yml](../../../.github/workflows/ci.yml):

- New `k6-lint` job installs the official k6 APT package and
  runs `k6 archive --out /dev/null` on every script in
  `tests/load/scenarios/`. `k6 archive` performs the same parse
  + import resolution as a real run without actually sending
  traffic, so a broken script fails CI within ~30 seconds.
- Job runs independently of integration-tests; no extra service
  containers needed.

## DoD

| Requirement | Status |
|---|---|
| Documented runbook | ✅ |
| Baseline template | ✅ |
| CI lint gate on scripts | ✅ |
| Actual run against staging cluster | ⚠ operator activity |
| Fix PRs for missed SLOs | ⚠ depends on the run |

The last two rows are explicitly operator work. Spec §13.2 DoD
("every missed SLO has a fix PR") applies at the point an SRE
files the first `baseline-<date>.md` — not from this wave.

## Deferred (logged in out-of-scope.md)

- **Auth login scenario** — not in the committed script set.
  Blueprint called for 500 logins/s; today's scripts cover
  post-auth surfaces only. Wave 13.2b.
- **Continuous baseline cron** — weekly staging run + diff
  against the last committed baseline, PR-on-regression > 10%.
  Wave 13.6 observability bundle.
- **Multi-region load matrix** — all current scripts hit one
  region. ADR 0026 regional split means a real multi-region
  deployment needs separate SLO tracking per region.
- **First baseline run** — operator activity. The template +
  runbook are in place; the first `baseline-YYYY-MM-DD.md`
  lands when the staging cluster exists.

## Wave 13 scorecard

| Item | Status |
|---|---|
| 13.1 Integration harness | ✅ |
| **13.2 Load tests runbook + lint** | ✅ this doc |
| 13.3 Chaos suite | pending |
| 13.4 Frontend test coverage | pending |
| 13.5 Mutation testing | pending |
| 13.6 SLI/SLO burn-rate alerts | pending |

## Next prompt

**13.3 — Chaos suite.** Spec §13.3 lists 6 scenarios (pod kill,
network partition, clock skew, disk full, NATS disconnect, KMS
outage). Realistic scope: commit YAML scenario descriptors +
runbook; actual runs are SRE drills, same as load tests.
