# VaultDMS

Enterprise Document Management System. Multi-tenant, region-aware, policy-enforced.

## Stack

| Layer          | Tech                                  |
|----------------|---------------------------------------|
| Backend        | Go 1.22+ (workspaces, gRPC, REST)     |
| Frontend       | React 18 + TypeScript + Vite          |
| Database       | PostgreSQL 16 (RLS for tenant isolation) |
| Search         | OpenSearch 2.12                       |
| Vector DB      | Qdrant 1.7                            |
| Cache          | Redis 7                               |
| Object Storage | MinIO (S3-compatible)                 |
| Event Bus      | NATS 2.10 + JetStream                 |
| Workflow       | Temporal                              |
| Auth Policy    | OPA (embedded)                        |

## Repository layout

```
vaultdms/
  proto/              # Protobuf contracts (source of truth for APIs)
  pkg/                # Shared Go libraries (config, logger, middleware, ...)
  services/           # One Go module per microservice
    document/
    storage/
    search/
    auth/
    policy/
    workflow/
    notification/
    audit/
    signature/
    billing/
    connector/
  scripts/            # DB init, seeders, tooling
  .github/workflows/  # CI + release
  docker-compose.yml  # Local dev: all dependencies + services
```

## Quickstart

```bash
# 1. Start the full compose tier — 24 services (10 infra + 3 app +
#    11 Go), every long-running container with a healthcheck. Host
#    ports follow scripts/run-all-services.sh so existing dev
#    tooling (curl :8081/healthz, :8180 HTTP APIs, etc.) keeps
#    working against the containerised services.
make docker-up

# 2. Block until every compose service reports healthy (≤120s):
./scripts/wait-for-healthy.sh

# 3. (Optional) run Go services on the host instead of in containers.
#     You cannot do both at once — host ports would collide.
docker compose stop auth policy document storage search audit \
                    workflow notification signature billing connector
make run-all
```

Service counts in compose:

| Tier | Count | Services |
|---|---|---|
| Infrastructure | 10 | postgres, redis, nats, minio, opensearch, qdrant, temporal, temporal-ui, clamav, minio-init (one-shot) |
| Application (non-Go) | 3 | collaboration, intelligence-worker, preview-worker |
| Application (Go) | 11 | auth, policy, document, storage, search, audit, workflow, notification, signature, billing, connector |

`docker compose ps --format json | jq '[.[] | select(.Health == "healthy")] | length'`
reports ~23 healthy once every container has passed its start_period
(minio-init exits after provisioning buckets and therefore doesn't
contribute a "healthy" status).

## Development

| Command                       | Purpose                                  |
|-------------------------------|------------------------------------------|
| `make build`                  | Build all service binaries               |
| `make test`                   | Run tests with race detector             |
| `make lint`                   | golangci-lint                            |
| `make proto-gen`              | Generate Go + gateway + OpenAPI stubs    |
| `make migrate-up SERVICE=x`   | Apply migrations for service `x`         |
| `make docker-build`           | Build all service images                 |
| `make security-check`         | gosec + govulncheck                      |

See `make help` for the full list.

## Status

This repository is scaffolded in phases. See `docs/phases.md` for progress.

- [x] Phase A — root scaffold
- [x] Phase B — shared `pkg/` libraries
- [x] Phase C — proto contracts
- [x] Phase D — service scaffolds (build + boot; proto handlers land in Phase E)
- [x] Phase 5 — **document service reference implementation**. Template for the other 10 services.

## License

See [LICENSE](LICENSE).
