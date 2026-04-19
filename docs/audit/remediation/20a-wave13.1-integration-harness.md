# Remediation 20a — Wave 13.1: Integration test harness + CI

**Date:** 2026-04-18
**Wave:** 13.1 · consolidates every "integration test lives in
Wave 13.1" deferral accumulated across Waves 5–12.

## Recon

The CI file already had an `integration-tests` job that booted
postgres + redis + nats + minio. Missing pieces:

- No OpenSearch → Wave 12.4's cross-service DSR purge test had
  nowhere to run.
- No shared Go helper → every service would re-invent pool +
  redis + nats wiring in its own `_integration_test.go`.
- No guidance on env var names, build tag convention, skip-on-
  missing-infra semantics.

## What shipped

### `pkg/testharness`

[pkg/testharness/harness.go](../../../pkg/testharness/harness.go)
— new internal module with a single exported struct:

```go
h := testharness.New(t)                 // Skips test if DATABASE_URL etc. unset
h.RunMigrations(t, migrationsDir)       // Apply NNNN_*.up.sql in order
tid := h.SeedTenant(t, "Acme Corp")     // organizations row + UUID
h.Pool / h.Redis / h.NATS / h.JetStream // pre-wired clients
h.WaitForNATSMsg(t, subject, 5*time.Second)
```

Env-var names match the existing CI job (`DATABASE_URL`,
`REDIS_URL`, `NATS_URL`, `OPENSEARCH_URL`). Missing env → `t.Skip`,
so `go test ./...` on a dev laptop without docker still runs.

The harness lives under `pkg/` (not `tests/`) so it picks up
the go.work include for `pkg/` without touching the work file.

### Docs

- [tests/integration/README.md](../../../tests/integration/README.md)
  documents: local docker-compose bring-up, env vars, harness API,
  how to write a new `_integration_test.go`, what not to put in
  integration tests.

### Compose

- [deploy/docker-compose.integration.yml](../../../deploy/docker-compose.integration.yml)
  stands up the same stack locally: postgres 16, redis 7, nats 2.10
  (with JetStream), opensearch 2.13, minio, temporal auto-setup.
  Every service binds on localhost ports matching the CI job.

### CI update

[.github/workflows/ci.yml](../../../.github/workflows/ci.yml)
`integration-tests` job:

- Added the **OpenSearch** service container with 512 MB heap +
  security-plugin disabled.
- Added the `OPENSEARCH_URL` env var alongside the existing
  `DATABASE_URL` / `REDIS_URL` / `NATS_URL`.
- New `Harness compiles` pre-step runs `go vet ./pkg/testharness/...`
  before the full test invocation so harness drift fails fast.
- Timeout stays at 15m (spec §13.1 budget: 12m p95; 15m hard stop).

## DoD

| Requirement | Status |
|---|---|
| Integration job in CI | ✅ (pre-existing + OpenSearch added) |
| Shared Go harness | ✅ `pkg/testharness` |
| Local docker-compose parity | ✅ |
| `t.Skip` when infra absent | ✅ |
| Budget tracked (12m p95 / 15m hard) | ✅ |

## Deferred (logged in out-of-scope.md)

- **Fixture corpus** — spec calls for 50 PDFs / 20 DOCX / 10
  emails / 5 images. Tests that need realistic input commit
  the minimum they use; a shared `harness.Fixtures(name)`
  loader lands once reuse justifies it.
- **Per-service `_integration_test.go` bodies** that exercise
  the Wave 5–12 deferrals. Each service owns its own tests;
  this wave ships the scaffolding, the tests follow as each
  service owner picks them up.
- **Smoke suite** (30s) on every push per §13.1. The current
  `test` job in ci.yml is unit-only; a new `smoke` job that
  runs a named subset with a 45s timeout is Wave 13.1b.
- **Temporal in the CI job** — the compose file has it
  (`temporalio/auto-setup`), but the CI job omits it today
  because workflow integration tests are still gated on each
  workflow shipping its first integration test. Add when the
  first one lands.

## Wave 13 scorecard

| Item | Status |
|---|---|
| **13.1 Integration harness + CI** | ✅ this doc |
| 13.2 Load tests | pending |
| 13.3 Chaos suite | pending |
| 13.4 Frontend test coverage | pending |
| 13.5 Mutation testing | pending |
| 13.6 SLI/SLO burn-rate alerts | pending |

## Next prompt

**13.2 — Load tests.** Spec §13.2 wants k6 scenarios actually
run against a staging cluster. Realistic scope for this pass:
commit the k6 scripts + a runner README; actual execution
against staging is an operator-driven activity logged out of
scope.
