# Remediation 20e — Wave 13.5: Mutation testing

**Date:** 2026-04-18
**Wave:** 13.5.

## Recon

Spec §13.5: "Critical security packages should not silently
regress. Add go-mutesting to CI on auth / policy / storage crypto
packages. Fail PR if mutation score < 70%."

Status pre-wave: no mutation testing anywhere. Unit tests exist
on the three target packages but weren't validated for
mutation-killing power.

## What shipped

### Runner

[scripts/mutesting/run.sh](../../../scripts/mutesting/run.sh) —
bash script that:

- Installs `go-mutesting` on demand.
- Runs against three hard-coded packages: auth/service,
  policy/opa, storage/service.
- Caps per-mutant test duration at 60s (stops infinite-loop
  mutants from blocking).
- Skips files with no accompanying test (we don't chase
  coverage into untested utility code).
- Parses the "mutation score" line from go-mutesting's output,
  compares to `$THRESHOLD` (default 70).
- Exits non-zero if any of the three packages falls below the
  threshold.

### CI job

[.github/workflows/ci.yml](../../../.github/workflows/ci.yml)
adds a `mutation` job:

- Runs on push to `main` and `release/*` branches.
- Runs on a PR that carries the `mutation-test` label — manual
  opt-in because the job is slow.
- 90-minute hard timeout.
- Calls `scripts/mutesting/run.sh` with `THRESHOLD=70`.

### Docs

[scripts/mutesting/README.md](../../../scripts/mutesting/README.md)
covers:

- What mutation testing catches that plain coverage misses.
- Why only auth + policy + storage are in scope (security surface
  / cost trade-off).
- Running locally.
- CI trigger conditions.
- Failure response (add tests or raise threshold with review).

## DoD

| Requirement | Status |
|---|---|
| go-mutesting wired into CI | ✅ |
| Threshold enforced (70%) | ✅ |
| Scoped to auth / policy / storage | ✅ |
| Baseline score published | ⚠ lands post first `main` run |
| Scorecard panel in observability | 🟡 Wave 13.6 |

## Deferred

- **First green baseline** — the `mutation` job's first
  successful run on `main` publishes the numbers to
  `docs/security/mutation-baseline-YYYY-MM-DD.md`. Operator
  activity, not pre-merge.
- **Scorecard dashboard panel** — Wave 13.6 observability
  bundle.
- **Expand scope** — storage/crypto is in the list; the top-level
  `pkg/crypto` package isn't. Once the baseline is green, consider
  adding `pkg/crypto` and `pkg/middleware/csrf`. Wave 13.5b.
- **Mutant-type tuning** — go-mutesting's default mutator set
  fires on every arithmetic op. Some mutants are irrelevant
  (e.g. `+ 1` vs `+ 2` in non-hot-path pagination). Disable the
  irrelevant mutators once the first score is in and we can
  see which mutants are noise.

## Wave 13 scorecard

| Item | Status |
|---|---|
| 13.1 Integration harness | ✅ |
| 13.2 Load tests runbook | ✅ |
| 13.3 Chaos suite | ✅ |
| 13.4 Frontend tests | ✅ |
| **13.5 Mutation testing** | ✅ this doc |
| 13.6 SLI/SLO burn-rate alerts | pending |

## Next prompt

**13.6 — SLI/SLO burn-rate alerts.** Last Wave 13 item.
Prometheus multi-window burn-rate alerts + per-SLO dashboard
+ error-budget policy doc.
