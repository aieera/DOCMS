# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

VaultDMS — multi-tenant, region-aware enterprise Document Management System. Go microservices + React 18 SPA. Postgres (RLS) for tenant isolation, NATS/JetStream for events, Temporal for workflows, MinIO for blobs, OpenSearch + Qdrant for search, OPA for policy.

## Workspace layout

Go workspace (`go.work`) with one module per service. Don't `cd` into a single module to run cross-cutting tests — use the workspace.

- `proto/` — buf-managed protobuf; **source of truth** for all RPCs. Stubs land in `proto/gen/go` (a workspace module).
- `pkg/` — shared libraries (`auth`, `database`, `events`, `tenant`, `middleware`, `storage`, `crypto`, …). One module.
- `services/<name>/` — one Go module each (`auth`, `policy`, `document`, `storage`, `search`, `audit`, `workflow`, `notification`, `signature`, `billing`, `connector`, `acknowledgement`). Layout: `cmd/server`, `internal/{handler,service,repository,model}`, `migrations/`.
- `services/document/` is the **reference implementation** (Phase 5). Mirror its structure when scaffolding new services.
- `cmd/dms-admin/` — admin CLI (only non-service binary in `cmd/`).
- `web/` — React 18 + Vite + TanStack Router/Query + Tailwind + zustand. Tests: vitest + Playwright.
- `mobile/` — Expo / React Native client. Separate toolchain from `web/`; shares the REST surface, not components.
- `services/collaboration`, `services/intelligence`, `services/preview` — non-Go (the "3 Application non-Go" tier in compose).
- `services/signature-signer/` — JVM / Gradle (Kotlin) PAdES signer. Split from `services/signature` because the EU DSS library is JVM-only. Build via its own Gradle wrapper, not `go build`.
- `tests/` — top-level cross-service tests: `contract/`, `e2e/`, `integration/`, `load/`, `fixtures/`. Per-module `_test.go` stays next to its code; anything spanning services lives here.
- `deploy/` — `helm/`, `terraform/`, `gateway/`, `monitoring/`, `docker-compose.integration.yml`.
- `ops/prometheus/` — scrape configs and alert rules.

## Common commands

| Task | Command |
|------|---------|
| First-time onboarding | `make setup` (gen-env → docker up → migrate → seed) |
| Build all binaries | `make build` |
| Test everything (race) | `make test` |
| Test one service | `make test-document` (or `go test -race ./services/document/...`) |
| Run one Go test | `go test -race ./services/document/internal/service -run TestName` |
| Integration tests (testcontainers) | `make test-integration` (= `test-integration-document` + `test-integration-wave15` cross-tenant isolation; build tag `integration`, spins Postgres + NATS containers) |
| PAdES corpus validator | `make test-pades` (runs Tier-1 validator against `tests/fixtures/pades/*.pdf`; uses the `pades_corpus` build tag) |
| Lint | `make lint` (golangci-lint v1.57, see `.golangci.yml`) |
| Format | `make fmt` |
| Tidy every module | `make tidy` |
| Regenerate proto stubs | `make proto-gen` (then commit `proto/gen/`; CI fails on drift) |
| Proto breaking check | `make proto-breaking` |
| Migration up/down/create | `make migrate-up SERVICE=document`, `migrate-down`, `migrate-create SERVICE=x NAME=y` |
| Bring up infra+services | `make docker-up` (build) or `make docker-up-prebuilt` (pull from `ghcr.io/aieera/docms`, ~2 min) |
| Run Go services on host | `make run-all` (after `docker compose stop` of those services — ports collide) |
| Web dev server | `make run-web` (Vite on :5173) |
| Web tests | `cd web && npm test` / `npm run test:e2e` |
| Security scan | `make security-check` (gosec + govulncheck) |
| Wipe & restart | `make reset` (DESTROYS volumes) |

CI (`.github/workflows/ci.yml`) runs Go 1.26, golangci-lint, `buf` lint+generate+breaking, race tests with Postgres/Redis/NATS service containers, and asserts no drift in `proto/gen/`.

**`-race` needs CGO.** On hosts without a C toolchain (e.g. Windows dev boxes without MinGW), `go test -race` fails with `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`. Local run-arounds: drop `-race` (`CGO_ENABLED=0 go test ./...`) or install a C toolchain. CI always runs with `-race`.

## Architecture invariants (CI-enforced — don't break)

These are guarded by scripts in `scripts/`; violations fail the build.

- **Outbox-only NATS publishing** (`scripts/check-outbox-only-publishing.sh`). Services MUST NOT call `nats.Publish` directly. Write to the outbox table inside the same DB tx; the outbox publisher (`pkg/database/outbox_publisher.go`) drains it. Allow-list is short — adding to it requires justification.
- **No `context.Background()` in NATS handlers** (`scripts/check-no-background-in-handlers.sh`). Handler ctx must descend from the service lifecycle ctx so SIGTERM drains in-flight work. Defensive `parent = context.Background()` nil-fallbacks are allowed.
- **No `math/rand`** (`scripts/check-no-math-rand.sh`) — use `crypto/rand`.
- **No MinIO naming in prod paths** (`scripts/check-no-minio-leaks.sh`). `MinIOEndpoint`, `VAULTDMS_MINIO_*`, and `minio:9000` are banned in `services/`, `pkg/`, and `deploy/helm/` — prod targets AWS S3; code must use generic `S3*` names. Allowed only in `docker-compose*.yml`, `.env.local.example`, `scripts/switch-s3-backend.sh`, and `docs/`.
- **Per-service migration table**: `make migrate-up` injects `x-migrations-table=<service>_schema_migrations` so versions don't collide. See [docs/architecture/migrations.md](docs/architecture/migrations.md).
- **Per-service Postgres schema with RLS** for tenant isolation. Always thread tenant through `pkg/tenant`; never trust a tenant id from the client request body.
- **Region pin is immutable** after first upload (document service). Lifecycle state machine: `draft → in_review → active → superseded → retained → archived → disposed`; `legal_hold` freezes transitions.

## Event emission

NATS subjects are versioned (`dms.<aggregate>.<event>.v1`). Emission ownership is decided in ADRs under [docs/adr/](docs/adr/) — e.g. `dms.document.version.uploaded.v1` is emitted by the **document** service per ADR 0021 (any older spec pointing at storage's `CompleteUpload` is stale). When adding a new event, add the subject to the JetStream stream filter set or the `§3.2/A3` CI test fails.

## Adding things

- **New endpoint**: edit the `.proto`, `make proto-gen`, implement handler in `services/<svc>/internal/handler`, business logic in `internal/service`, DB in `internal/repository`. The `.claude/commands/add-endpoint.md` slash command captures the full checklist.
- **New service**: use `.claude/commands/new-service.md` — mirrors `services/document/` layout and registers it in `go.work`, `Makefile` `SERVICES`, and `docker-compose.yml`.
- **Review a diff**: `.claude/commands/review-diff.md` runs the seven-point multi-tenant DMS review (tenant isolation, SQLi, perms, error handling, secret logging, pagination, cascade deletes).
- **New Prom metric**: convention lives in [services/document/internal/service/metrics.go](services/document/internal/service/metrics.go) — name `<service>_<subject>_<verb>_total|_seconds|_errors_total`, Help string names the source event or code path, registered on the default registry at package init. Increment at the named code path; scraped from the shared `/metrics` endpoint wired in `cmd/server/main.go`.
- **New workflow test**: reuse the `stubActivities` struct at the bottom of [services/workflow/internal/workflows/review_test.go](services/workflow/internal/workflows/review_test.go). It has no-op method stubs for every activity name the workflows call; new workflows add methods there rather than duplicating per-test stubs. Override a specific activity via `env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: "X", DisableAlreadyRegisteredCheck: true})`.

## Before assuming a feature is missing

Check [docs/backlog/out-of-scope.md](docs/backlog/out-of-scope.md) first. It's a single ledger (~100+ entries) that tracks every deferred item with *what was found, why, and which wave owns it*. Common failure mode: agent reads a spec that looks like an open TODO, builds it, then discovers it was deferred with intent (or already shipped). The ledger's rule #2 is explicit: *"If a feature is needed to make another feature work, log it but still finish the current prompt with a stub; do not re-scope."*

Many pre-Wave-X specs in the repo are stale — the implementation has moved ahead of the brief. Before acting on a brief that says "X is missing" or "X is stubbed", grep for the symbol and read the ledger; the thing is often already done, often with a regression test that explicitly names the bug it guards against (e.g. `TestLocalKM_RejectsEmptyKEKID` in [pkg/crypto/envelope_test.go](pkg/crypto/envelope_test.go) — "the shared-KEK bug this prompt eliminates").

## Operational scripts

Worth knowing before reinventing them — all under `scripts/`:

- `preflight.sh` — pre-deploy health/env checks.
- `smoke-e2e.sh` — end-to-end smoke against a running stack.
- `switch-s3-backend.sh` — flip `.env` between local MinIO and AWS S3 (the one legitimate place MinIO naming still appears).
- `soc2-evidence.sh` — collects compliance evidence artifacts.
- `airgap/` — packaging helpers for air-gapped installs.
- `wait-for-healthy.sh` / `wait-for-health.sh` — block until compose services report healthy.
- `run-all-services.sh` — host-run all Go services (backs `make run-all`).

## Release tooling

Conventional Commits are enforced by `commitlint.config.cjs` (husky commit hook). `release-please-config.json` drives automated version bumps and regenerates `CHANGELOG.md` — **do not hand-edit `CHANGELOG.md`**; edit commit messages instead.

## Local dev gotchas

- Compose has **24 services**; `./scripts/wait-for-healthy.sh` blocks until all report healthy (≤120 s). `minio-init` exits after provisioning and won't show "healthy" — expect ~23 healthy.
- You can't run a Go service on the host AND in compose simultaneously — ports collide. `make run-all` assumes you've stopped the compose copies.
- `make gen-env` refuses to overwrite an existing `.env`. Delete it first if you want fresh secrets.
- Migrations live under `services/<svc>/migrations`; the document service holds the bulk 45-table schema.
- **Postgres max_connections must be ≥ 300** when `run-all` is active — 12 Go services × 50-conn pgxpool > the default 100. One-off: `docker exec vaultdms-postgres psql -U vaultdms -d postgres -c "ALTER SYSTEM SET max_connections=300;" && docker restart vaultdms-postgres`. Otherwise the 12th service will crash on boot with `FATAL: sorry, too many clients already`.
- `run-all` does **not** apply migrations. After `make docker-up` or a volume reset, each service's `migrations/*.up.sql` must be applied manually (`docker exec -i vaultdms-postgres psql -U vaultdms -d vaultdms < services/<svc>/migrations/NNN_*.up.sql`). The Makefile `migrate-up SERVICE=<name>` target works when the `migrate` CLI is installed locally. Silent symptom of a missing migration is a login flow that succeeds at the HTTP layer but returns `401 invalid credentials` because the service is SELECTing a column that doesn't exist yet.
- Vite auto-picks a port if `:3000` is busy — check the terminal banner (may be 3000, 3005, or 5173).

## Cross-service auth plumbing (non-obvious, must-follow)

Every service exposing gRPC + HTTP needs these three middleware primitives stacked correctly; missing any one surfaces as a silent 403/401/500 that's nearly impossible to diagnose from the wire:

1. **gRPC server chain**: `RecoveryInterceptor → CorrelationInterceptor → TenantInterceptor → UserIdentityInterceptor → RequestLogInterceptor`. `UserIdentityInterceptor` reads `x-user-id` + `x-user-role` from incoming gRPC metadata and calls `auth.WithUser`. Without it, `auth.GetUserID(ctx)` returns `uuid.Nil` inside handlers and every OPA policy check (Rule 5 / 6) silently denies.
2. **Cross-service gRPC clients** (e.g. document → policy) must inject outgoing metadata. Pattern: a `withCallerMetadata(ctx, userID)` helper that calls `metadata.AppendToOutgoingContext(ctx, "x-tenant-id", …, "x-user-id", …, "x-user-role", …)`. The downstream service's `TenantInterceptor` returns `Unauthenticated` without it; the upstream's fail-closed path turns that into a generic 403.
3. **HTTP**: user-facing `/api/v1/*` routes need `middleware.SessionAuth(SessionAuthConfig{Pool: pool})` to populate `auth.User(ctx)` from the `dms_session` cookie. Internal worker routes (`/internal/v1/*`) mount **without** SessionAuth so the Temporal worker (which has no cookie) can call them. Split by `chi.Router.Group` or separate muxes; never mount both under one middleware stack.

Policy context for OPA checks **must** include `user_role` (for Rule 6) and `workspace_id` when checking workspace resources (for Rule 5). Empty context → default-deny even for `owner`. See `services/document/internal/service/service.go` `checkPermission` for the canonical forward-all-identity pattern.

## Repo scanner discipline

Any SQL column that is nullable MUST either be `COALESCE`'d in the SELECT or scanned into a pointer (`*string`, `*time.Time`, `*uuid.UUID`). Plain `string` / `time.Time` / `uuid.UUID` destinations panic on NULL with `can't scan into dest[N]: cannot scan NULL into *string`. The bug is infuriatingly easy to miss on a fresh seed where optional columns are empty. Audit applies equally to inline `tx.QueryRow(...).Scan(...)` calls and to dedicated `scanFoo` helpers.

## Status docs

Two and only two living status docs:

- [docs/STATE_OF_THE_PROJECT.md](docs/STATE_OF_THE_PROJECT.md) — single-source status. Gets updated at the end of every session with material change. Dated `*_REPORT_YYYY-MM-DD.md` files were consolidated into this on 2026-04-23; do not re-introduce the pattern.
- [docs/tech-debt/ledger.md](docs/tech-debt/ledger.md) — known debt with exit criteria. "Out of scope" (`docs/backlog/out-of-scope.md`) is separate: it tracks items that were *considered and deferred*, whereas the tech-debt ledger tracks what's *shipped with known debt*.

The blueprint (`DMS Architecture/dms-blueprint.md`) and the alignment plan (`docs/reports/BLUEPRINT_COMPLETION_PLAN_2026-04-19.md`) are the only other spec-level docs worth opening for context.

## Wave 15 (2026-04-21 → 2026-04-23)

Four sub-waves shipped as a conscious scope expansion. New patterns worth knowing:

- **New service `services/acknowledgement/`** — policy-attestation campaigns with per-tenant HMAC + per-campaign SHA-256 hash chain. ADR 0027. Registered in `go.work`, `scripts/run-all-services.sh` (port 8092/8191), `release-please-config.json`.
- **`pkg/geo`** — country-code resolver for geofencing. `StaticResolver` + `CachingResolver` (bounded 10k, 60 s TTL). MaxMind adapter is behind `//go:build maxmind` per ADR 0028; the dep is only pulled when the tag is set.
- **Wave 15 additions to `pkg/middleware`** — `Geofence` middleware returns 451 / 428 / 503 (fail-closed) per the OPA decision, and `SessionAuth` / `UserIdentityInterceptor` (see cross-service auth section above).
- **Outbox fan-out to notification**: emit a second row under `dms.notify.<source>.<event>.v1` with the `DeliveryPayload` shape (tenant, user_ids, title, body, resource_type, resource_id). The notification service's `dms.notify.>` consumer picks it up. Namespace carve-out (not `dms.<aggregate>.<event>.v1`) is documented in the tech-debt ledger as T-D-9.
- **Tier-1 PAdES validator**: `services/signature/internal/pades/` does structural checks (regex-based, `//go:build pades_corpus` for fixture walker). Tier-2 Adobe Reader + EU DSS is an operator-per-release procedure documented in `docs/runbooks/15-pades-harness.md`. Never promote Tier-1 to prod acceptance.
