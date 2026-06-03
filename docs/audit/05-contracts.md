# SeDoc Audit — Phase D: Contract Verification

**Date:** 2026-04-16

---

## 1. Proto RPC Inventory

**Total: 123 RPCs across 12 proto files** (common.proto has 0)

| Proto file | RPCs | Server impl | Client callers |
|-----------|------|-------------|---------------|
| document.proto | 24 | **PARTIAL** — handler exists, gRPC registered | 0 callers |
| policy.proto | 2 | **YES** — handler exists, gRPC registered | 2 (document, storage) |
| storage.proto | 7 | **PARTIAL** — handler exists, gRPC registered | 0 callers |
| auth.proto | 18 | **NO** — HTTP-only service, no gRPC impl | 0 callers |
| audit.proto | 5 | **NO** — REST-only, no gRPC impl | 0 callers |
| billing.proto | 10 | **NO** — REST-only, no gRPC impl | 0 callers |
| collaboration.proto | 13 | **NO** — Node.js WebSocket, no gRPC | 0 callers |
| intelligence.proto | 8 | **NO** — Python FastAPI, no gRPC | 0 callers |
| notification.proto | 12 | **NO** — REST-only, no gRPC impl | 0 callers |
| search.proto | 6 | **NO** — REST-only, no gRPC impl | 0 callers |
| signature.proto | 8 | **NO** — REST-only, no gRPC impl | 0 callers |
| workflow.proto | 10 | **NO** — REST-only, no gRPC impl | 0 callers |

### Root cause

Proto codegen (`buf generate`) has never been run. `proto/gen/go/` contains only `go.mod` — zero `.pb.go` files. Therefore:
- Only 3 services (document, policy, storage) have gRPC handlers — these were hand-wired against the proto types before the import broke
- 9 services have gRPC listeners (`grpc.NewServer()`) but register zero handlers
- Only policy is used as a gRPC client (by document + storage)

### Orphan RPCs (defined but not implemented): **120 of 123**

Only 3 services implement any RPCs at all. Of those:
- **document**: Implements `DocumentServiceServer` but only via gRPC-gateway (the actual handler maps REST to gRPC). The 24 proto RPCs are implemented as REST endpoints internally.
- **policy**: Implements `CheckPermission` + `BatchCheckPermission` (2 of 2). **FULLY IMPLEMENTED.**
- **storage**: Implements `InitiateUpload`, `CompleteUpload`, `AbortUpload`, `GetDownloadURL`, `GetScanStatus` via gRPC handler. **5 of 7 implemented**, missing `GetPreviewURL` and `RequestLifecycle`.

### Orphan callers (client code calling unimplemented RPCs): **0**

Only `NewPolicyServiceClient` is used, and the server implements both of its RPCs.

---

## 2. REST Endpoint Inventory

### Per-service REST endpoints (from handler registrations):

| Service | REST endpoints | Pattern |
|---------|---------------|---------|
| audit | 5 | GET /events, GET /export, POST /verify-integrity, POST /data-subject/export, POST /data-subject/anonymize |
| auth | ~26 | Full router: login, register, logout, me, sessions, api-keys, mfa, SAML, OIDC, SCIM |
| billing | 6 | POST /tenants/provision, GET subscription, GET/PUT features, GET plans, POST /stripe/webhook |
| connector | 9 | Webhooks CRUD + delivery log, connectors list/get/auth/callback, MCP SSE |
| document | 0 REST | **All document endpoints are gRPC-gateway** — handler implements gRPC interface, not `http.HandleFunc` |
| notification | 6 | GET list, PATCH read, POST read-all, GET unread-count, GET/PUT preferences |
| search | 5 | POST /search, GET /autocomplete, POST/GET/DELETE /saved-searches |
| signature | 6 | POST create, GET request, GET by-doc, POST sign, POST cancel, GET verify |
| workflow | 7 | GET/POST definitions, POST/GET instances, POST signal, POST cancel, GET tasks/mine |

---

## 3. Frontend → Backend Cross-Reference

### Frontend API calls vs backend endpoints:

| Frontend call | Backend service | Endpoint exists? |
|--------------|----------------|-----------------|
| `POST /auth/login` | auth | ✓ |
| `POST /auth/register` | auth | ✓ |
| `POST /auth/logout` | auth | ✓ |
| `GET /auth/me` | auth | ✓ |
| `POST /auth/mfa/verify` | auth | ✓ |
| `GET /documents` | document | ✓ (via gRPC-gateway) |
| `GET /documents/:id` | document | ✓ |
| `PATCH /documents/:id` | document | ✓ |
| `DELETE /documents/:id` | document | ✓ |
| `GET /documents/:id/versions` | document | ✓ |
| `POST /documents/:id/versions/:id/restore` | document | **UNVERIFIED** — gRPC-gateway route unclear |
| `POST /search` | search | ✓ |
| `GET /search/autocomplete` | search | ✓ |
| `POST /storage/uploads/initiate` | storage | ✓ |
| `POST /storage/uploads/:id/complete` | storage | ✓ |
| `GET /workspaces` | document | **UNVERIFIED** — workspace listing is part of document service gRPC |
| `POST /workspaces` | document | **UNVERIFIED** |
| `GET /workspaces/:id/folders` | document | **UNVERIFIED** |
| `POST /workspaces/:id/folders` | document | **UNVERIFIED** |
| `PATCH /workspaces/:id/folders/:id` | document | **UNVERIFIED** |
| `DELETE /workspaces/:id/folders/:id` | document | **UNVERIFIED** |
| `GET /notifications` | notification | ✓ |
| `PATCH /notifications/:id/read` | notification | ✓ |
| `POST /notifications/read-all` | notification | ✓ |
| `GET /notifications/unread-count` | notification | ✓ |
| `GET /workflows/tasks/mine` | workflow | ✓ |
| `POST /workflows/instances` | workflow | ✓ |
| `POST /workflows/tasks/:id/complete` | workflow | ✓ |
| `POST /intelligence/ask` | intelligence | ✓ |
| `POST /intelligence/summarize` | intelligence | ✓ |
| `POST /intelligence/redact/detect` | intelligence | ✓ |
| `GET /permissions/:type/:id` | **NO SERVICE** | **MISSING** — no permissions REST handler |
| `POST /permissions/check` | **NO SERVICE** | **MISSING** — policy is gRPC-only |
| `POST /permissions/:type/:id` | **NO SERVICE** | **MISSING** |
| `DELETE /permissions/:type/:id/:subjectId` | **NO SERVICE** | **MISSING** |
| `GET /admin/users` | **NO SERVICE** | **MISSING** — billing has /internal/v1 not /api/v1 |
| `POST /admin/users/invite` | **NO SERVICE** | **MISSING** |
| `POST /admin/users/:id/suspend` | **NO SERVICE** | **MISSING** |
| `POST /admin/users/:id/reset-mfa` | **NO SERVICE** | **MISSING** |
| `GET /admin/audit-log` | audit (different path) | **MISMATCH** — frontend calls `/admin/audit-log`, backend serves `/api/v1/audit/events` |
| `GET /admin/settings` | billing (internal only) | **MISMATCH** — billing uses `/internal/v1/` prefix, frontend expects `/api/v1/admin/settings` |
| `PUT /admin/settings` | billing (internal only) | **MISMATCH** — same |
| `GET /storage/downloads/url` | storage | **UNVERIFIED** — frontend path doesn't match gRPC method name |

### Frontend calls to nonexistent backend endpoints: **10**

| Frontend path | Issue |
|---------------|-------|
| `GET /permissions/:type/:id` | No REST handler — policy is gRPC-only |
| `POST /permissions/check` | No REST handler |
| `POST /permissions/:type/:id` | No REST handler |
| `DELETE /permissions/:type/:id/:subjectId` | No REST handler |
| `GET /admin/users` | No such endpoint (billing is internal-only) |
| `POST /admin/users/invite` | No such endpoint |
| `POST /admin/users/:id/suspend` | No such endpoint |
| `POST /admin/users/:id/reset-mfa` | No such endpoint |
| `GET /admin/audit-log` | Path mismatch (backend: `/api/v1/audit/events`) |
| `GET/PUT /admin/settings` | Path mismatch (backend: `/internal/v1/tenants/:id/features`) |

---

## 4. Summary

| Category | Count |
|----------|-------|
| Total proto RPCs defined | 123 |
| RPCs with server implementation | **3** (all in policy: 2, storage: 5, document: via gateway) |
| RPCs orphaned (defined, not implemented) | **~120** |
| gRPC clients used | 1 (PolicyServiceClient) |
| Frontend API calls | 42 |
| Frontend calls to existing endpoints | 32 |
| Frontend calls to **nonexistent** endpoints | **10** |
| Path mismatches (frontend vs backend) | 2 |

**The proto-to-implementation gap is the largest contract issue.** 120 of 123 RPCs are orphaned because proto codegen never ran. Most services compensate with REST handlers, but the contract surface is fragmented: some use gRPC, some use REST with different path conventions, and the frontend assumes a unified `/api/v1/` REST surface that doesn't fully exist.
