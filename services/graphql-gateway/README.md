# GraphQL Gateway (services/graphql-gateway)

Read-only GraphQL surface for the SeDoc frontend. Implements
blueprint §12.1 and ADR 0074.

## What it does

- Accepts `POST /graphql` with `{ id, variables, operationName }`
  where `id` is the SHA-256 hex of an operation in the build-time
  persisted-query manifest.
- Walks the validated selection set, calls upstream gRPC services
  (document, workflow, collaboration, policy, audit), batches
  fan-outs via per-request DataLoader cache.
- Gates every resource access via `policy.CheckPermission` before
  the upstream call. Denies render as JSON `null`, never partial-
  result leakage.
- Per-tenant Redis token-bucket rate limit on `/graphql` (separate
  from the REST surface).

Mutations stay on REST. The blueprint and ADR call out why.

## What it does not do

- No subscriptions (use the existing SSE / WS channels for live
  updates).
- No mutations.
- No schema stitching from upstream gRPC reflection — the schema
  is hand-curated in `schema/schema.graphqls` so the read surface
  stays small and reviewable.

## Endpoints

| Verb | Path | Notes |
|------|------|-------|
| `POST` | `/graphql` | Persisted-query path. Body must be `{ id, variables?, operationName? }`. |
| `GET`  | `/healthz` | Liveness; returns `{ status, persisted_count }`. |

Production builds reject inline queries. `dev` builds (`version=dev`)
honor `?dev=1` from loopback for ad-hoc curl testing.

## Persisted queries

Build-time allow-list. Source of truth lives in
`internal/persisted/manifest.json` and is embedded into the binary
via `go:embed`. The frontend's `npm run build` regenerates the
manifest from `web/.graphql/*.graphql`; the same JSON is copied to
this directory by CI so the backend manifest matches what the
client will actually send.

To add a new operation:

1. Add the `.graphql` file to `web/.graphql/`.
2. Run `npm run graphql:manifest` (frontend) — produces
   `web/.graphql/manifest.json`.
3. Copy the JSON to `services/graphql-gateway/internal/persisted/manifest.json`.
4. Rebuild the backend container.

CI fails the PR if the two manifests drift.

## DataLoaders

Per-request, no goroutine windowing. Resolvers that fan out call
`LoadMany(keys)` once at the parent level; per-child `Load(k)`
becomes a cache hit. Loaders enumerated in ADR 0074 §"N+1 via
DataLoader".

## Local dev

```sh
cd services/graphql-gateway
go run ./cmd/server
```

Then:

```sh
curl -s http://localhost:8080/graphql \
  -H "Content-Type: application/json" \
  -H "X-Auth-Tenant-ID: <tenant-uuid>" \
  -H "X-Auth-User-ID: <user-uuid>" \
  -d '{
    "id": "8c4d9f1bb3a8c2e0fefb4d11f1c0ffee1234567890abcdef1234567890abcdef",
    "variables": { "id": "<doc-uuid>" }
  }' | jq
```

Inline queries (dev only):

```sh
curl -s 'http://localhost:8080/graphql?dev=1' \
  -H "Content-Type: application/json" \
  -H "X-Auth-Tenant-ID: <tenant-uuid>" \
  -H "X-Auth-User-ID: <user-uuid>" \
  -d '{ "query": "{ __schema { queryType { name } } }" }' | jq
```

## Future migration to fully gqlgen-generated code

The runtime today is a hand-rolled `gqlparser`-based dispatcher
under `internal/exec`. The schema and `gqlgen.yml` are wired so
`go generate ./...` produces the canonical scaffolding under
`internal/gqlgen/`. The hand-rolled and generated paths will
coexist during the migration; today only the hand-rolled path is
mounted in `cmd/server/main.go`.
