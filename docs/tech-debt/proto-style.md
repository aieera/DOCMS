# Proto style warnings — deferred

## Status
Suppressed in `proto/buf.yaml`. Tracked for v1 API freeze.

138 cosmetic lint findings across 3 rules are currently excepted. The
remaining 2 dead-import findings (IMPORT_USED) were **fixed at the time
of this deferral**; they were not suppressed.

## What we're suppressing

| Rule | Meaning |
|------|---------|
| `RPC_REQUEST_STANDARD_NAME` | method `Foo` should accept request type `FooRequest` |
| `RPC_RESPONSE_STANDARD_NAME` | method `Foo` should return response type `FooResponse` |
| `RPC_REQUEST_RESPONSE_UNIQUE` | each RPC should have a unique request/response message pair (no shared types like `User` returned by 4 different RPCs) |

No other lint rules are suppressed. Package naming, import paths, field
ordering, enum naming, service PascalCase, etc. all remain strict.

## Why defer

Renaming the message types required to satisfy these rules would require
coordinated edits across:

- 3 Go services with generated gRPC clients (document, policy, storage)
- gRPC-gateway HTTP route mappings (in `document.proto`)
- Frontend API contracts (`web/src/api/*.ts` — 12 modules)
- Mobile API contracts (`mobile/api/*.ts` — 6 modules)
- OpenAPI spec (`docs/api/openapi.yaml`)
- Every handler, service, repository layer that references the types in question
- All integration tests

Total blast radius: 4 codebases, ~80 type renames.

Pre-v1 we prefer velocity. Once the service names stabilize and these
messages become external API commitments, the stylistic wrapper types
(e.g. `GetUserResponse { User user = 1; }`) become worth the cost.

## Before v1 GA — REQUIRED action

1. Audit every non-standard RPC method name and request/response type.
2. Introduce wrapper messages so each RPC has unique, standardized
   request/response types (e.g. `User` → `GetUserResponse { User user = 1; }`).
3. Rename any RPC that doesn't follow Verb+Noun form with matching
   `FooRequest` / `FooResponse` types.
4. Bump API version: `proto/vaultdms/v1` → `proto/vaultdms/v2`.
5. Serve both v1 and v2 for a deprecation period (minimum 90 days).
6. Coordinate frontend + mobile + any external API consumers.
7. Remove the three `except` entries from `proto/buf.yaml`.
8. Delete this file.

## Owner

[assign before starting v1 GA planning]

## Cross-refs

- Audit findings: [docs/audit/03-inconsistencies.md](../audit/03-inconsistencies.md)
- Contract audit: [docs/audit/05-contracts.md](../audit/05-contracts.md)
- Build remediation (includes ShareLink rename work): [docs/audit/remediation/01-builds-unblocked.md](../audit/remediation/01-builds-unblocked.md)

## Warning inventory

Counts as of 2026-04-17 (the 2 IMPORT_USED findings were resolved by
removing dead imports from intelligence.proto and storage.proto; they
are NOT in the suppressed set).

| Rule | Count |
|------|------:|
| RPC_RESPONSE_STANDARD_NAME | 73 |
| RPC_REQUEST_RESPONSE_UNIQUE | 59 |
| RPC_REQUEST_STANDARD_NAME | 6 |
| **Total suppressed** | **138** |
| IMPORT_USED | 0 (fixed, not suppressed) |

### RPC_REQUEST_RESPONSE_UNIQUE — detailed file:line list

These are the rare findings and the most likely to need real fixes during
v1 freeze. Each line shows an RPC whose request or response type is
shared with another RPC.

**audit.proto**
- `audit.proto:16:3` — `ExportJob` shared across multiple RPCs
- `audit.proto:17:3` — `ExportJob` shared

**auth.proto**
- `auth.proto:13:3` — `Tenant` shared (CreateTenant/GetTenant/UpdateTenant)
- `auth.proto:14:3` — `Tenant` shared
- `auth.proto:15:3` — `Tenant` shared
- `auth.proto:17:3` — `User` shared across 4 RPCs (Create/Get/Update/Disable)
- `auth.proto:18:3` — `User` shared
- `auth.proto:19:3` — `User` shared
- `auth.proto:20:3` — `User` shared
- `auth.proto:23:3` — `Group` shared across Create/AddMember/RemoveMember
- `auth.proto:24:3` — `Group` shared
- `auth.proto:25:3` — `Group` shared
- `auth.proto:28:3` — `Session` shared across Login/RefreshSession
- `auth.proto:29:3` — `Session` shared

**billing.proto**
- `billing.proto:14:3` — `Subscription` shared across Get/ChangePlan/Cancel
- `billing.proto:15:3` — `Subscription` shared
- `billing.proto:16:3` — `Subscription` shared

**collaboration.proto**
- `collaboration.proto:13:3` — `Comment` shared across Create/Reply/Resolve
- `collaboration.proto:14:3` — `Comment` shared
- `collaboration.proto:15:3` — `Comment` shared
- `collaboration.proto:23:3` — `Annotation` shared across Create/Update
- `collaboration.proto:24:3` — `Annotation` shared

**document.proto**
- `document.proto:218:3` — `Folder` shared (CreateFolder/GetFolder/UpdateFolder)
- `document.proto:221:3` — `Folder` shared
- `document.proto:227:3` — `Folder` shared
- `document.proto:230:3` — `google.protobuf.Empty` shared (DeleteFolder)
- `document.proto:234:3` — `Document` shared (CreateDocument/GetDocument/UpdateDocument/MoveDocument/UpdateLifecycle)
- `document.proto:237:3` — `Document` shared
- `document.proto:240:3` — `Document` shared
- `document.proto:243:3` — `google.protobuf.Empty` shared
- `document.proto:246:3` — `Document` shared
- `document.proto:260:3` — `Document` shared
- `document.proto:270:3` — `google.protobuf.Empty` shared
- `document.proto:283:3` — `google.protobuf.Empty` shared
- `document.proto:291:3` — `GetMetadataSchemaResponse` shared across Get/Update
- `document.proto:294:3` — `GetMetadataSchemaResponse` shared

**intelligence.proto**
- `intelligence.proto:17:3` — `OCRJob` shared (RunOCR/GetOCRJob)
- `intelligence.proto:18:3` — `OCRJob` shared

**notification.proto**
- `notification.proto:16:3` — `Template` shared (Create/Update/Get)
- `notification.proto:17:3` — `Template` shared
- `notification.proto:18:3` — `Template` shared
- `notification.proto:21:3` — `Preferences` shared (Get/Update)
- `notification.proto:22:3` — `Preferences` shared

**search.proto**
- `search.proto:13:3` — `SearchResponse` shared across Search/SemanticSearch/HybridSearch
- `search.proto:14:3` — `SearchResponse` shared
- `search.proto:15:3` — `SearchResponse` shared
- `search.proto:17:3` — `ReindexJob` shared (Reindex/GetReindexJob)
- `search.proto:18:3` — `ReindexJob` shared

**signature.proto**
- `signature.proto:14:3` — `Envelope` shared across Create/Get/Cancel
- `signature.proto:15:3` — `Envelope` shared
- `signature.proto:16:3` — `Envelope` shared
- `signature.proto:19:3` — `SignResponse` shared (Sign/Decline)
- `signature.proto:20:3` — `SignResponse` shared

**workflow.proto**
- `workflow.proto:12:3` — `WorkflowDefinition` shared (Create/Get/Publish)
- `workflow.proto:13:3` — `WorkflowDefinition` shared
- `workflow.proto:15:3` — `WorkflowDefinition` shared
- `workflow.proto:17:3` — `WorkflowInstance` shared (Start/Get/Cancel)
- `workflow.proto:18:3` — `WorkflowInstance` shared
- `workflow.proto:20:3` — `WorkflowInstance` shared

### RPC_REQUEST_STANDARD_NAME (6 findings)

Low count — not enumerated here. Run `buf lint --config=... --except=PACKAGE_VERSION_SUFFIX --except=RPC_RESPONSE_STANDARD_NAME --except=RPC_REQUEST_RESPONSE_UNIQUE` to re-surface on demand.

### RPC_RESPONSE_STANDARD_NAME (73 findings)

High count — the majority of the deferral. These are cases where the RPC
returns a domain type directly (e.g. `rpc GetUser returns (User)`)
instead of a wrapper (`GetUserResponse`). Not enumerated individually.
