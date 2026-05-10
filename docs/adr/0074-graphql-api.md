# ADR 0074 — GraphQL Read API

Date: 2026-05-10
Status: Accepted (backend-first; frontend swap follows)
Closes: blueprint §12.1 — "GraphQL — Frontend queries requiring flexible data fetching"

## Context

The frontend talks to the backend exclusively over REST today. Most
pages map cleanly onto a single REST endpoint, but two views have
fan-out problems that are getting worse as the schema grows:

- **Document detail** (`/workspaces/$workspaceId/documents/$documentId`)
  needs the document, its current version, the version history, the
  comment thread, the annotation list, the workflow instance(s) it
  participates in, the open tasks assigned to the caller for it, and
  the caller's effective permissions. That's 6–8 REST round trips
  today, each waterfall-blocked on the previous because some
  responses (workflow instance ids, current version id) feed the
  next request.
- **Activity timeline** (the dashboard's "Recent activity" card and
  the per-document audit panel) merges audit events, workflow
  transitions, comments, version uploads, and signature events into
  one chronological stream. The merge happens client-side after 4–5
  parallel REST calls, with awkward correlation logic and no shared
  cursor.

A REST batch endpoint per page would solve the round-trip problem
but bakes shape decisions into the backend that should belong to
the client. §12.1 specified GraphQL for exactly this case: "queries
requiring flexible data fetching". REST stays the primary surface
for everything else (uploads, mutations, third-party integrations,
webhooks).

## Decision

### Standalone service, not bolted onto the gateway

GraphQL gets its own service: `services/graphql-gateway`. The
existing REST gateway is a thin reverse-proxy / auth-frontend; bolting
GraphQL onto it would couple two products with very different
operational shapes (schema versioning, persisted-query manifests,
DataLoader request scoping, query-cost analysis) into one binary.
A separate service:

- Lets us scale GraphQL pods independently when one tenant runs an
  expensive query.
- Lets the schema evolve on its own deploy cadence; a schema bump
  doesn't ride the gateway's release train.
- Keeps the dependency surface small — gqlgen + the gRPC clients
  for the four upstream services it talks to (document, workflow,
  collaboration, policy), and that's it.

The service speaks **HTTP only** to the world (POST `/graphql` and
GET `/graphql/persisted/:hash`); to the rest of the cluster it
speaks **gRPC** over the same client stubs the document service uses.

### gqlgen, not graphql-go or thunder

`gqlgen` is the de-facto Go GraphQL toolkit at this point: schema-
first (matches the rest of our IDL-first ecosystem — protobufs +
OpenAPI), generated resolver scaffolding, first-class DataLoader
integration via `dataloaden`, mature directives (`@auth`, `@cost`),
plays well with `net/http`. graphql-go is code-first which would
diverge from how every other API surface in the repo works;
thunder is unmaintained.

### Read-only for v1

The schema covers the read surface enumerated in the prompt:
`document`, `version`, `comment`, `permission`, `workflow`, `task`.
**No mutations.** Mutations stay on REST for now because:

- The transactional outbox lives in each service's domain database.
  Routing mutations through GraphQL would either duplicate the
  outbox plumbing into the GraphQL service, or push it onto the
  REST handlers anyway via a write-through. Both are worse than
  letting clients keep posting to REST.
- Authorization for mutations needs the same `pkg/policy` checks
  that already gate the REST handlers; doing them twice (once in
  GraphQL, once in the downstream gRPC) is wasted work; doing them
  only in GraphQL leaves a hole if a caller bypasses the gateway.
- The two pages we're migrating are read-heavy; mutations on those
  pages (post comment, complete task) keep using their existing
  REST endpoints.

When mutations land later, they'll pass through to the existing gRPC
mutation RPCs with the GraphQL layer adding only argument coercion
+ dataloader cache invalidation.

### Permission enforcement: gate inside the resolver, not after

Two anti-patterns we explicitly avoid:

- **Post-fetch filtering** — pull every document, then drop the
  ones the caller can't see. Wastes upstream bandwidth, leaks
  existence (slow query times for forbidden ids), and the cache
  layer downstream sees rows it shouldn't.
- **Field-level @auth directive only** — handy for hiding fields
  but doesn't stop the upstream RPC from running; the rejected row
  still costs a gRPC round trip and a dataloader entry.

Resolvers gate by calling `policy.CheckPermission` for every
resource id *before* dispatching to the upstream gRPC. The check is
batched via `BatchCheckPermission` (already in policy.proto) so a
list of 50 documents costs one policy call, not 50.

For the common case (document already loaded, then resolver wants
its versions / comments / annotations) the permission check on the
parent document gates all child resolvers — children inherit the
parent's allow decision. This matches how `pkg/policy` already
expresses cascading permissions.

### N+1 via DataLoader

gqlgen's dataloader integration solves the canonical fan-out:
"resolve a list of documents, then for each document resolve its
versions". Without batching, a 50-document list becomes 51 gRPC
calls (1 list + 50 ListVersions). With per-request DataLoaders
keyed by `(tenantID, parentID)`, it's 2.

DataLoaders we ship in v1:

| Loader | Keyed by | Upstream call |
|---|---|---|
| `versionsByDocumentLoader` | `document_id` | `DocumentService.ListVersions` (batched) |
| `commentsByDocumentLoader` | `document_id` | `CollaborationService.ListComments` (batched) |
| `annotationsByDocumentLoader` | `document_id` | `CollaborationService.ListAnnotations` (batched) |
| `userByIDLoader` | `user_id` | `AuthService.GetUser` |
| `permissionsByResourceLoader` | `(resource_kind, resource_id)` | `PolicyService.BatchCheckPermission` |
| `workflowInstancesByDocLoader` | `document_id` | `WorkflowService.ListInstances` (batched) |

DataLoader scope is **per-request**, not global. Cache lives in
`context.Context`; goes out of scope when the HTTP handler returns.
This is the only correctness-safe scope for a multi-tenant server
— a global loader would leak rows across tenant boundaries.

### Persisted queries (operation allow-list)

The public `/graphql` endpoint **rejects ad-hoc query strings**.
Clients submit a SHA-256 hash of the operation document; the server
looks it up in an embedded allow-list. Unrecognized hashes get a
400 with `{ "error": "PersistedQueryNotFound", "hash": "..." }`
which tells the client to recompile its query manifest.

The allow-list is a **build-time artifact**, not a runtime store:

```
web/.graphql/manifest.json   ← generated by `urql persisted`
                                during `npm run build`
services/graphql-gateway/internal/persisted/manifest.embed.go
                              ← go:embed of the same JSON
```

Build the manifest in CI; embed it into the service binary; reject
anything that doesn't match. Concretely, this gives us:

- **Query-cost predictability** — every query the server can answer
  was reviewed by humans + CI (cost analysis runs over the AST at
  build time), so a tenant cannot blow up the server with a
  hand-crafted depth-30 query.
- **Cheaper hot path** — the server hashes the inbound `id`, looks
  up the parsed-and-validated query document, skips re-parsing.
- **Cleaner audit trail** — every accepted query is identifiable by
  its hash; logs and traces carry the operation name + hash.

There is a `?dev=1` escape hatch that accepts ad-hoc queries — but
*only* when the build is `version=dev` AND the request is from
loopback. Production binaries don't honor it (the env-var gate is
checked at boot, not per-request).

### Rate limiting

Per-tenant token bucket via the existing `pkg/middleware/ratelimit`.
The bucket is keyed on `(tenant_id, "graphql")` — separate from the
REST per-tenant bucket so a noisy GraphQL workload can't starve
the REST surface (or vice versa). Default 60 ops/sec burst 120;
overridable per-tenant in admin config.

Cost analysis runs at build time on the manifest entries, not at
runtime — every persisted query has a known maximum cost. The
runtime rate-limit is a per-second op count, not a sum of weighted
costs, because the manifest already prevents any single op from
being expensive.

### Authentication

The service trusts the gateway-signed `X-Auth-User-ID` /
`X-Auth-Tenant-ID` headers via `pkg/middleware/RequireGatewaySignature`,
the same trust boundary the REST services already enforce. No
direct cookie parsing; no session lookups. This keeps the
GraphQL gateway out of the auth path's critical chain.

### Subscriptions

Out of scope for v1. The collaboration service already exposes a
gRPC streaming presence channel (`StreamPresence`) and we have
SSE wired up for notifications. GraphQL subscriptions would
require a third concurrent transport (websockets) for marginal
gain. Revisit if a new use case actually needs a unified pub/sub
view across resources.

## Consequences

- **One more service in the topology.** Compose entry, Helm chart,
  health probe, log shipping. Worth it for the isolation; flagged
  on the deployment checklist.
- **Persisted-query manifest is a build dependency.** If a
  developer adds a new GraphQL operation without rebuilding the
  manifest, their dev server (with `?dev=1`) works but the prod
  build rejects the call. CI fails the PR if the manifest hash
  inside the binary doesn't match what `urql persisted` produces
  from the source tree.
- **Two-PR rollout.** This PR lands the backend (service, schema,
  resolvers, DataLoaders, persisted-query allow-list, rate limit,
  Helm + compose entries). Frontend swap (urql client + doc
  detail + activity timeline migration + e2e) follows in a
  separate PR after the schema is reviewed.
- **No mutations.** Frontend keeps posting to REST for any write.
  Until that changes the GraphQL service is purely a read
  optimization, not a unification.
- **gqlgen generated files are checked into the repo** (matching
  the protobuf convention). Re-running `go generate ./...` is the
  contract for schema changes; the generated bundle is reviewed
  alongside the schema diff.
