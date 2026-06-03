# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository

VaultDMS — multi-tenant enterprise Document Management System.

- **13 Go microservices** in `services/*` (audit, auth, billing, connector, document, graphql-gateway, mcp-server, notification, policy, search, signature, storage, workflow) — these are exactly the modules listed in `go.work`.
- **2 Python workers**: `services/intelligence` (OCR/AI) and `services/preview` (rendering).
- **1 Node service**: `services/collaboration` (Yjs, on :8083).
- **1 JVM signer**: `services/signature-signer` (Kotlin/Gradle, PDF signing) — companion to the Go `signature` service.
- **Clients/edges**: `web/` (React/Vite), `extension/` (browser), `addins/{outlook,word}` (Office add-ins, ADR 0112/0113), `mobile/`.
- **Go tooling binaries** in `cmd/`: `dms-admin`, `dms-installer`, `license-gen`.

Not every directory under `services/` is a Go module — only the 13 above are in `go.work`; the Python/Node/JVM services build via their own toolchains and Dockerfiles. Protobufs in `proto/` are the source of truth for service APIs and generate into `proto/gen/go`.

## Common commands

Backend (run from repo root):

| Task | Command |
|---|---|
| Build all service binaries → `./bin` | `make build` |
| All Go tests with race detector | `make test` |
| One service's tests | `make test-<svc>` (e.g. `test-storage`) — see Makefile for the list |
| Single test | `go test -race -run TestName ./services/<svc>/internal/<pkg>/...` |
| Integration tests (testcontainers) | `go test -tags integration ./services/<svc>/...` |
| Lint | `make lint` (golangci-lint across the workspace) |
| Format | `make fmt` |
| Tidy every module | `make tidy` |
| Regenerate proto stubs / gateway / OpenAPI | `make proto-gen` |
| Proto lint / breaking-change check | `make proto-lint` / `make proto-breaking` |
| gosec + govulncheck | `make security-check` |

Migrations (per-service, separate `<svc>_schema_migrations` bookkeeping table — versions do **not** share a namespace across services):

```
make migrate-up     SERVICE=document
make migrate-down   SERVICE=document
make migrate-create SERVICE=document NAME=add_xyz
```

Local stack:

```
make setup            # gen-env → docker up → migrate → seed (one-shot onboarding)
make docker-up        # build-from-source compose stack
make docker-up-prebuilt   # pull ghcr.io/aieera/docms images instead (much faster)
./scripts/wait-for-healthy.sh
make wake             # recover from a Docker/WSL restart: up + verify + restart dead-port containers
make run-all          # run the Go services on the host (must `docker compose stop` them first; ports collide otherwise)
make stop-all
make reset            # DANGER: docker down -v + rerun setup
```

After a Docker Desktop / WSL restart, host ports frequently return `000` or cross-wire (one service answering on another's port). `make wake` is the fix — batch-restarts the API-fronting containers; prefer it over manual `docker restart`.

Load & security testing: `make security-check` (gosec + govulncheck), `make load-test` and the `load-*` targets (doc-crud, search, upload, mixed, isolation, chaos) — a full real campaign is expensive (cloud-scoped), so confirm scope before running beyond the local smoke checks.

Frontend (`cd web`):

| Task | Command |
|---|---|
| Dev server | `npm run dev` (or `make run-web` from root) |
| Build | `npm run build` |
| Unit tests | `npm test` (vitest) — single test: `npm test -- -t "name"` |
| E2E | `npm run test:e2e` (Playwright) |
| Lint | `npm run lint` |

## Architecture

Read `docs/architecture.md` for the full picture. The non-obvious load-bearing pieces:

**Two communication modes, deliberately split.** Synchronous gRPC is reserved for hot-path decisions that can't be deferred: `document → policy` permission checks on every read/write, `storage → policy` on upload initiate, `storage → ClamAV` for virus scans. Everything else (search index updates, notification fan-out, audit log, connector webhooks, AI post-processing) goes through NATS JetStream. Subjects follow the `dms.{domain}.{action}.v1` taxonomy.

**Transactional outbox, not direct publish.** Every domain write inserts into the per-service `outbox` table inside the same transaction. A polling publisher forwards to NATS. Do not publish to NATS directly from a handler — it breaks the at-least-once guarantee under crash-between-commit-and-publish.

**Tenant isolation = Postgres RLS + `NOBYPASSRLS` app role.** Every tenant table has `tenant_id UUID` as the first PK column. All queries must run inside `database.WithTenantTx(tenantID, ...)`, which opens a tx and issues `SET LOCAL app.current_tenant`. The app DB user cannot bypass RLS, so a buggy query that omits the tenant predicate fails-closed (0 rows) rather than leaking. NATS consumers must re-establish the tenant context from the message envelope before any DB work.

**Per-blob envelope encryption.** Each blob has a DEK wrapped by a per-tenant KEK (Vault or AWS KMS in prod, `VAULTDMS_LOCAL_KEK` in dev). Crypto-shredding by dropping the KEK is the deletion mechanism — keep this in mind when touching storage.

**Service layout convention.** Each Go service follows `cmd/server` (entrypoint) + `internal/{handler,service,repository,model,...}` + `migrations/`. The `document` service is the reference implementation — copy its layering when scaffolding a new service. The `pkg/` module holds the shared libraries (config, logger, middleware, database, tenant, events, crypto, validation, errors, metrics, tracing, health).

**ADRs in `docs/adr/`** are the authoritative record of cross-cutting decisions (e.g. ADR 0021 nails down which service emits `dms.version.uploaded.v1` — currently the document service, not storage, despite the upload-completion path).

**Lifecycle invariants worth knowing before editing the document service:**
- States: `draft → in_review → active → superseded → retained → archived → disposed`
- `legal_hold` freezes any lifecycle transition
- `region_pin` is immutable after the first upload — PATCH cannot change it; cross-region moves require the compliance migration tool

## Stack

Go 1.25 (toolchain 1.26.2), React 18 + Vite + TanStack Router/Query + Zustand + Radix + Tailwind, Postgres 16, OpenSearch 2.12, Qdrant 1.7, Redis 7, NATS 2.10 + JetStream, MinIO (S3), Temporal, OPA (embedded), ClamAV.
