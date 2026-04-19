# Remediation 21b — Wave 14.2: OpenAPI completion

**Date:** 2026-04-18
**Wave:** 14.2.

## Recon

Spec §14.2: "Every HTTP endpoint has an OpenAPI 3.1 definition.
Published docs. CI check prevents handler ↔ spec drift."

Status pre-wave:

- `docs/api/openapi.yaml` exists, 527 lines, OpenAPI 3.1, 15 paths
  documented (auth, documents, search, storage, webhooks, intelligence,
  audit, permissions, admin).
- Handler code declares **~237 route registrations across 38 files**
  (measured: `grep -rE '\.(Get|Post|Put|Delete|Patch|HandleFunc|Handle)\(' services/`).
- No CI gate on the spec.
- No drift detection.

The gap is real but not new — the spec has been a pointer document,
not a contract. Closing it fully is a multi-week per-service backfill;
this wave ships the **rail** (tooling + CI) and sets up the incremental
completion.

## What shipped

### Drift detector

[scripts/openapi/check-drift.sh](../../../scripts/openapi/check-drift.sh)
— bash script that:

- Greps every `(Get|Post|Put|Delete|Patch|Handle|HandleFunc)` route
  registration across `services/`.
- Normalises path params (`{id}` → `{param}`) and prefixes to
  `/api/v1/...` when missing.
- Naive-parses `paths:` from `docs/api/openapi.yaml`.
- Prints two diffs: undocumented (code ∖ spec) and stale (spec ∖ code).
- Exits non-zero on any undocumented path; stale is warn-only unless
  `STRICT=1`.

Deliberately approximate — catches chi/gorilla/net/http, misses
dynamically-prefixed groups. False positives are fine; the goal is
that *adding a new route* forces a spec update in the same PR.

### CI job

[.github/workflows/ci.yml](../../../.github/workflows/ci.yml) adds an
`openapi` job:

- `spectral lint docs/api/openapi.yaml --fail-severity=error`
- Drift check via `scripts/openapi/check-drift.sh`

The drift step is **advisory** (`continue-on-error: true`) until the
backfill is green, matching the Wave 13.5 mutation-rollout pattern.
Flip to hard-blocking after the first all-services backfill lands.

### Tooling docs

[scripts/openapi/README.md](../../../scripts/openapi/README.md) covers
local running, STRICT mode, how the matcher works and what it misses,
and the rollout plan.

## DoD

| Requirement | Status |
|---|---|
| OpenAPI 3.1 spec exists | ✅ pre-existing |
| Lint in CI (spectral) | ✅ |
| Drift check in CI | ✅ advisory |
| Every endpoint documented | ❌ 15 / ~237 — backfill in progress |
| Published rendered docs | 🟡 Wave 14.2b |

## Backfill scorecard

Per-service. Each row is one PR: list the service's routes, add the
missing paths/schemas to `openapi.yaml`, and flip drift check to green
for that service's path prefix.

| Service | Routes (approx) | Documented | Owner |
|---|---|---|---|
| auth | ~40 | partial | — |
| document | ~35 | partial | — |
| storage | ~15 | partial | — |
| search | ~10 | partial | — |
| policy | ~12 | partial | — |
| workflow | ~20 | none | — |
| billing | ~15 | none | — |
| collaboration | ~15 | none | — |
| connector | ~25 | none | — |
| notification | ~8 | none | — |
| signature | ~15 | none | — |
| audit | ~8 | none | — |
| intelligence | ~10 | none | — |
| preview | ~5 | none | — |

Target: all rows green, drift check flipped to hard fail, by the end
of Wave 14 (pairs well with 14.6 release engineering — v1.0.0 release
implies a frozen public contract).

## Deferred

- **Full per-service backfill** — 14.2a through 14.2n, one PR each.
  Mechanical work once the rail is in place.
- **Rendered docs site** (Wave 14.2b) — build `redoc` or `elements`
  static HTML from the spec, publish on merge to main. Pointer in
  `scripts/openapi/README.md`.
- **Contract tests** — `schemathesis` or `dredd` that generates
  property-based requests from the spec and hits a real server.
  Best added after backfill is complete so the fuzzer has real
  contracts to exercise.
- **Flip drift check to blocking** — after the first all-services
  green run, remove `continue-on-error: true` from the CI job.
- **SDK generation** — once the spec is complete, generate Go + TS
  client SDKs from it. Removes hand-written client drift.

## Wave 14 scorecard

| Item | Status |
|---|---|
| 14.1 Service READMEs | ✅ |
| **14.2 OpenAPI completion** | 🟡 rail shipped; backfill in progress |
| 14.3 DR runbook + rehearsal | pending |
| 14.4 Threat model + pentest plan | pending |
| 14.5 Air-gapped / on-prem packaging | pending |
| 14.6 Release engineering | pending |

## Next prompt

**14.3 — Disaster recovery runbook + rehearsal.** Spec §14.3:
RTO/RPO targets per data store, documented restore procedures,
quarterly rehearsal schedule.
