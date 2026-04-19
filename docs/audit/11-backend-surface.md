# Phase 1 — Backend surface inventory

**Audit scope (narrowed per triage):** REST handlers + grpc-gateway REST
paths + user-facing WebSocket + NATS subject list + internal-only
surfaces. Pure service-to-service gRPC RPCs with no gateway route are
listed in bulk at the bottom as "intentionally backend-only" — per
prompt guidance we do not treat them as missing frontend reachability.

Row count: **139** (REST 87 · gRPC-gateway REST 24 · Python FastAPI 10
· WebSocket 1 · intentional internal/gRPC 17). OpenAPI spec cross-check
at the end.

`Status` column is filled in Phase 3. Here it's blank.

---

## audit service

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | GET `/api/v1/audit/events` | [handler.go:30](../../services/audit/internal/handler/handler.go#L30) | session (upstream) | `X-Tenant-ID` header | |
| REST | GET `/api/v1/audit/export` | [handler.go:31](../../services/audit/internal/handler/handler.go#L31) | session | `X-Tenant-ID` header | |
| REST | POST `/api/v1/audit/verify-integrity` | [handler.go:32](../../services/audit/internal/handler/handler.go#L32) | session | `X-Tenant-ID` header | |
| REST | POST `/api/v1/audit/data-subject/export` | [handler.go:33](../../services/audit/internal/handler/handler.go#L33) | session | `X-Tenant-ID` header | |
| REST | POST `/api/v1/audit/data-subject/anonymize` | [handler.go:34](../../services/audit/internal/handler/handler.go#L34) | session | `X-Tenant-ID` header | |

**gRPC:** `audit.proto` declares 5 RPCs (Query / GetEvent / Export /
GetExport / VerifyChain). None registered on the server — service is
REST-only. All are `backend-only`.

## auth service

### Public (no auth required)

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | POST `/api/v1/auth/register` | [router.go:32](../../services/auth/internal/handler/router.go#L32) (IP-ratelimited 5/min) | public | body `tenant_slug` → org lookup | |
| REST | POST `/api/v1/auth/login` | [router.go:33](../../services/auth/internal/handler/router.go#L33) | public | body `tenant_slug` | |
| REST | POST `/api/v1/auth/mfa/verify` | [router.go:34](../../services/auth/internal/handler/router.go#L34) | public (uses MFA session token) | from MFA session | |
| REST | POST `/api/v1/auth/mfa/recovery` | [router.go:35](../../services/auth/internal/handler/router.go#L35) | public | from MFA session | |
| REST | GET `/api/v1/auth/saml/{tenant_slug}/metadata` | [saml_handler.go:32](../../services/auth/internal/handler/saml_handler.go#L32) | public | path `tenant_slug` → org | |
| REST | GET `/api/v1/auth/saml/{tenant_slug}/login` | [saml_handler.go:48](../../services/auth/internal/handler/saml_handler.go#L48) (IP-ratelimited 30/min) | public | path `tenant_slug` | |
| REST | POST `/api/v1/auth/saml/{tenant_slug}/acs` | [saml_handler.go:67](../../services/auth/internal/handler/saml_handler.go#L67) (IP-ratelimited 30/min) | public (IdP-signed assertion) | path `tenant_slug` | |
| REST | GET `/api/v1/auth/oidc/{tenant_slug}/login` | [oidc_handler.go:30](../../services/auth/internal/handler/oidc_handler.go#L30) (IP-ratelimited 30/min) | public | path `tenant_slug` | |
| REST | GET `/api/v1/auth/oidc/{tenant_slug}/callback` | [oidc_handler.go:47](../../services/auth/internal/handler/oidc_handler.go#L47) (IP-ratelimited 30/min) | public | path `tenant_slug` | |

### Session-authenticated

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | POST `/api/v1/auth/logout` | [router.go:59](../../services/auth/internal/handler/router.go#L59) | session cookie | from session | |
| REST | GET `/api/v1/auth/sessions` | [router.go:62](../../services/auth/internal/handler/router.go#L62) | session | from session | |
| REST | POST `/api/v1/auth/sessions/revoke-all` | [router.go:63](../../services/auth/internal/handler/router.go#L63) | session | from session | |
| REST | DELETE `/api/v1/auth/sessions/{session_id}` | [router.go:64](../../services/auth/internal/handler/router.go#L64) | session | from session | |
| REST | POST `/api/v1/auth/mfa/setup` | [router.go:68](../../services/auth/internal/handler/router.go#L68) | session | from session | |
| REST | POST `/api/v1/auth/mfa/confirm` | [router.go:69](../../services/auth/internal/handler/router.go#L69) | session | from session | |
| REST | POST `/api/v1/auth/mfa/disable` | [router.go:70](../../services/auth/internal/handler/router.go#L70) | session | from session | |
| REST | POST `/api/v1/auth/api-keys` | [router.go:75](../../services/auth/internal/handler/router.go#L75) | session (role: admin/owner) | from session | |
| REST | GET `/api/v1/auth/api-keys` | [router.go:76](../../services/auth/internal/handler/router.go#L76) | session | from session | |
| REST | DELETE `/api/v1/auth/api-keys/{key_id}` | [router.go:77](../../services/auth/internal/handler/router.go#L77) | session | from session | |

### Admin users (04b)

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | GET `/api/v1/admin/users` | [router.go:87](../../services/auth/internal/handler/router.go#L87) | session (role: admin/owner) | from session | |
| REST | POST `/api/v1/admin/users/invite` | [router.go:88](../../services/auth/internal/handler/router.go#L88) | session (role: admin/owner) | from session | |
| REST | POST `/api/v1/admin/users/{id}/suspend` | [router.go:89](../../services/auth/internal/handler/router.go#L89) | session (role: admin/owner) | from session | |
| REST | POST `/api/v1/admin/users/{id}/reset-mfa` | [router.go:90](../../services/auth/internal/handler/router.go#L90) | session (role: admin/owner) | from session | |

### SCIM 2.0 (13 paths, per-tenant bearer token)

Mounted at `/scim/v2/{tenant_slug}/...` via [router.go:97](../../services/auth/internal/handler/router.go#L97); bearer token validated against `sso_configs`.

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | GET `/scim/v2/{tenant_slug}/ServiceProviderConfig` | [scim/handler.go:33](../../services/auth/internal/scim/handler.go#L33) | SCIM bearer | path | |
| REST | GET `/scim/v2/{tenant_slug}/ResourceTypes` | [scim/handler.go:34](../../services/auth/internal/scim/handler.go#L34) | SCIM bearer | path | |
| REST | GET `/scim/v2/{tenant_slug}/Schemas` | [scim/handler.go:35](../../services/auth/internal/scim/handler.go#L35) | SCIM bearer | path | |
| REST | GET `/scim/v2/{tenant_slug}/Users` | [scim/handler.go:38](../../services/auth/internal/scim/handler.go#L38) | SCIM bearer | path | |
| REST | POST `/scim/v2/{tenant_slug}/Users` | [scim/handler.go:39](../../services/auth/internal/scim/handler.go#L39) | SCIM bearer | path | |
| REST | GET `/scim/v2/{tenant_slug}/Users/{id}` | [scim/handler.go:40](../../services/auth/internal/scim/handler.go#L40) | SCIM bearer | path | |
| REST | PUT `/scim/v2/{tenant_slug}/Users/{id}` | [scim/handler.go:41](../../services/auth/internal/scim/handler.go#L41) | SCIM bearer | path | |
| REST | PATCH `/scim/v2/{tenant_slug}/Users/{id}` | [scim/handler.go:42](../../services/auth/internal/scim/handler.go#L42) | SCIM bearer | path | |
| REST | DELETE `/scim/v2/{tenant_slug}/Users/{id}` | [scim/handler.go:43](../../services/auth/internal/scim/handler.go#L43) | SCIM bearer | path | |
| REST | GET `/scim/v2/{tenant_slug}/Groups` | [scim/handler.go:47](../../services/auth/internal/scim/handler.go#L47) | SCIM bearer | path | |
| REST | POST `/scim/v2/{tenant_slug}/Groups` | [scim/handler.go:48](../../services/auth/internal/scim/handler.go#L48) | SCIM bearer | path | |
| REST | GET `/scim/v2/{tenant_slug}/Groups/{id}` | [scim/handler.go:49](../../services/auth/internal/scim/handler.go#L49) | SCIM bearer | path | |
| REST | PATCH `/scim/v2/{tenant_slug}/Groups/{id}` | [scim/handler.go:50](../../services/auth/internal/scim/handler.go#L50) | SCIM bearer | path | |
| REST | DELETE `/scim/v2/{tenant_slug}/Groups/{id}` | [scim/handler.go:51](../../services/auth/internal/scim/handler.go#L51) | SCIM bearer | path | |

**gRPC:** `auth.proto` declares 18 RPCs (CreateTenant, ListUsers, IssueAPIKey, …). None registered on the server — auth is HTTP-only. All are `intended-for-internal-admin-tooling` (e.g. `dms-admin` CLI).

## billing service

### Internal (X-API-Key)

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | POST `/internal/v1/tenants/provision` | [handler.go:35](../../services/billing/internal/handler/handler.go#L35) | X-API-Key | request body | |
| REST | POST `/internal/v1/stripe/webhook` | [handler.go:36](../../services/billing/internal/handler/handler.go#L36) | Stripe signature | webhook payload | |
| REST | GET `/internal/v1/tenants/{tenantId}/subscription` | [handler.go:37](../../services/billing/internal/handler/handler.go#L37) | X-API-Key | path | |
| REST | GET `/internal/v1/tenants/{tenantId}/features` | [handler.go:38](../../services/billing/internal/handler/handler.go#L38) | X-API-Key | path | |
| REST | PUT `/internal/v1/tenants/{tenantId}/features` | [handler.go:39](../../services/billing/internal/handler/handler.go#L39) | X-API-Key | path | |
| REST | GET `/internal/v1/plans` | [handler.go:40](../../services/billing/internal/handler/handler.go#L40) | X-API-Key | n/a (global) | |

### User-facing admin (04b)

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | GET `/api/v1/admin/settings` | [admin.go:21](../../services/billing/internal/handler/admin.go#L21) | session + role owner | from session | |
| REST | PUT `/api/v1/admin/settings` | [admin.go:22](../../services/billing/internal/handler/admin.go#L22) | session + role owner | from session | |

**gRPC:** `billing.proto` declares 10 RPCs. None registered — REST only. `backend-only` (gRPC definitions are for future service-to-service).

## connector service

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | POST `/api/v1/webhooks` | [handler.go:30](../../services/connector/internal/handler/handler.go#L30) | session (upstream) | `X-Tenant-ID` | |
| REST | GET `/api/v1/webhooks` | [handler.go:31](../../services/connector/internal/handler/handler.go#L31) | session | `X-Tenant-ID` | |
| REST | DELETE `/api/v1/webhooks/{id}` | [handler.go:32](../../services/connector/internal/handler/handler.go#L32) | session | `X-Tenant-ID` | |
| REST | GET `/api/v1/webhooks/{id}/deliveries` | [handler.go:33](../../services/connector/internal/handler/handler.go#L33) | session | `X-Tenant-ID` | |
| REST | GET `/api/v1/connectors` | [handler.go:35](../../services/connector/internal/handler/handler.go#L35) | session | `X-Tenant-ID` | |
| REST | GET `/api/v1/connectors/{provider}` | [handler.go:36](../../services/connector/internal/handler/handler.go#L36) | session | `X-Tenant-ID` | |
| REST | GET `/api/v1/connectors/{provider}/auth-url` | [handler.go:37](../../services/connector/internal/handler/handler.go#L37) | session | `X-Tenant-ID` | |
| REST | POST `/api/v1/connectors/{provider}/callback` | [handler.go:38](../../services/connector/internal/handler/handler.go#L38) | OAuth state | state-bound | |
| SSE | POST `/api/v1/mcp` | [handler.go:40](../../services/connector/internal/handler/handler.go#L40) | session | `X-Tenant-ID` | |

## document service (grpc-gateway REST + gRPC)

**24 grpc-gateway paths** registered at [main.go:143](../../services/document/cmd/server/main.go#L143). Gateway proxies to the local gRPC server on the same pod. Auth: every request carries the session cookie forwarded by the ingress; the gateway converts to gRPC metadata. Tenant: from session via `TenantInterceptor`.

| Method + path | RPC | Proto ref |
|---|---|---|
| POST `/api/v1/workspaces/{workspace_id}/folders` | CreateFolder | [document.proto:219](../../proto/vaultdms/v1/document.proto#L219) |
| GET `/api/v1/folders/{folder_id}` | GetFolder | [document.proto:222](../../proto/vaultdms/v1/document.proto#L222) |
| GET `/api/v1/workspaces/{workspace_id}/folders` | ListFolders | [document.proto:225](../../proto/vaultdms/v1/document.proto#L225) |
| PATCH `/api/v1/folders/{folder_id}` | UpdateFolder | [document.proto:228](../../proto/vaultdms/v1/document.proto#L228) |
| DELETE `/api/v1/folders/{folder_id}` | DeleteFolder | [document.proto:231](../../proto/vaultdms/v1/document.proto#L231) |
| POST `/api/v1/documents` | CreateDocument | [document.proto:235](../../proto/vaultdms/v1/document.proto#L235) |
| GET `/api/v1/documents/{document_id}` | GetDocument | [document.proto:238](../../proto/vaultdms/v1/document.proto#L238) |
| PATCH `/api/v1/documents/{document_id}` | UpdateDocument | [document.proto:241](../../proto/vaultdms/v1/document.proto#L241) |
| DELETE `/api/v1/documents/{document_id}` | DeleteDocument | [document.proto:244](../../proto/vaultdms/v1/document.proto#L244) |
| POST `/api/v1/documents/{document_id}/move` | MoveDocument | [document.proto:247](../../proto/vaultdms/v1/document.proto#L247) |
| GET `/api/v1/workspaces/{workspace_id}/documents` | ListDocuments | [document.proto:250](../../proto/vaultdms/v1/document.proto#L250) |
| POST `/api/v1/documents/{document_id}/versions` | CreateVersion | [document.proto:254](../../proto/vaultdms/v1/document.proto#L254) |
| GET `/api/v1/documents/{document_id}/versions` | ListVersions | [document.proto:257](../../proto/vaultdms/v1/document.proto#L257) |
| POST `/api/v1/documents/{document_id}/lifecycle` | UpdateLifecycle | [document.proto:261](../../proto/vaultdms/v1/document.proto#L261) |
| POST `/api/v1/documents/{document_id}/share-links` | CreateShareLink | [document.proto:265](../../proto/vaultdms/v1/document.proto#L265) |
| GET `/api/v1/documents/{document_id}/share-links` | ListShareLinks | [document.proto:268](../../proto/vaultdms/v1/document.proto#L268) |
| DELETE `/api/v1/share-links/{share_link_id}` | DeleteShareLink | [document.proto:271](../../proto/vaultdms/v1/document.proto#L271) |
| POST `/api/v1/shared/{token}` | AccessShareLink | [document.proto:274](../../proto/vaultdms/v1/document.proto#L274) |
| POST `/api/v1/tags` | CreateTag | [document.proto:278](../../proto/vaultdms/v1/document.proto#L278) |
| GET `/api/v1/tags` | ListTags | [document.proto:281](../../proto/vaultdms/v1/document.proto#L281) |
| DELETE `/api/v1/tags/{tag_id}` | DeleteTag | [document.proto:284](../../proto/vaultdms/v1/document.proto#L284) |
| POST `/api/v1/documents/batch/metadata` | BatchUpdateMetadata | [document.proto:288](../../proto/vaultdms/v1/document.proto#L288) |
| GET `/api/v1/tenants/metadata-schema` | GetMetadataSchema | [document.proto:292](../../proto/vaultdms/v1/document.proto#L292) |
| PUT `/api/v1/tenants/metadata-schema` | UpdateMetadataSchema | [document.proto:295](../../proto/vaultdms/v1/document.proto#L295) |

**Workspace CRUD is notably missing.** No `POST /api/v1/workspaces`, no `GET /api/v1/workspaces`. The frontend calls these (remediation 04b surfaced the 404s). Flag for Phase 3.

## intelligence service (Python FastAPI)

Router prefix `/api/v1/intelligence` (set at [routes.py:15](../../services/intelligence/app/api/routes.py#L15)).

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | POST `/api/v1/intelligence/ask` | [routes.py:54](../../services/intelligence/app/api/routes.py#L54) | session (upstream) | body/headers | |
| REST | POST `/api/v1/intelligence/summarize` | [routes.py:76](../../services/intelligence/app/api/routes.py#L76) | session | body/headers | |
| REST | POST `/api/v1/intelligence/redact/detect` | [routes.py:88](../../services/intelligence/app/api/routes.py#L88) | session | body/headers | |
| REST | POST `/api/v1/intelligence/redact/apply` | [routes.py:101](../../services/intelligence/app/api/routes.py#L101) | session | body/headers | |
| REST | GET `/healthz` | [main.py:56](../../services/intelligence/app/main.py#L56) | public | n/a | |
| REST | GET `/metrics` | [main.py:61](../../services/intelligence/app/main.py#L61) | scrape (internal) | n/a | |

**gRPC:** `intelligence.proto` declares 8 RPCs (Summarize, ExtractEntities, AskDocument, RunOCR, GetOCRJob, Embed, BatchEmbed, DetectPII). **Not implemented** — service is Python FastAPI, no gRPC. Flag for Phase 3: `Embed` + `BatchEmbed` + `RunOCR` have no REST equivalent.

## notification service

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | GET `/api/v1/notifications` | [handler.go:29](../../services/notification/internal/handler/handler.go#L29) | session | `X-Tenant-ID` | |
| REST | PATCH `/api/v1/notifications/{id}/read` | [handler.go:30](../../services/notification/internal/handler/handler.go#L30) | session | `X-Tenant-ID` | |
| REST | POST `/api/v1/notifications/read-all` | [handler.go:31](../../services/notification/internal/handler/handler.go#L31) | session | `X-Tenant-ID` | |
| REST | GET `/api/v1/notifications/unread-count` | [handler.go:32](../../services/notification/internal/handler/handler.go#L32) | session | `X-Tenant-ID` | |
| REST | GET `/api/v1/notifications/preferences` | [handler.go:33](../../services/notification/internal/handler/handler.go#L33) | session | `X-Tenant-ID` | |
| REST | PUT `/api/v1/notifications/preferences` | [handler.go:34](../../services/notification/internal/handler/handler.go#L34) | session | `X-Tenant-ID` | |

**gRPC:** `notification.proto` declares 12 RPCs (Send, SendBatch, template CRUD, webhook CRUD, preferences, ListDeliveries). None registered — `backend-only` (planned future).

## policy service (04b REST + gRPC)

### REST (session-authenticated, 04b)

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | GET `/api/v1/permissions/{resource_type}/{resource_id}` | [http.go:36](../../services/policy/internal/handler/http.go#L36) | session (SessionAuth) | from session | |
| REST | POST `/api/v1/permissions/check` | [http.go:37](../../services/policy/internal/handler/http.go#L37) | session | from session | |
| REST | POST `/api/v1/permissions/{resource_type}/{resource_id}` | [http.go:38](../../services/policy/internal/handler/http.go#L38) | session + resource ADMIN | from session | |
| REST | DELETE `/api/v1/permissions/{resource_type}/{resource_id}/{principal_id}` | [http.go:39](../../services/policy/internal/handler/http.go#L39) | session + resource ADMIN | from session | |

### gRPC (service-to-service only)

| Transport | Method | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| gRPC | `PolicyService/CheckPermission` | [handler.go:31](../../services/policy/internal/handler/handler.go#L31) | mTLS/cluster-internal | gRPC metadata | |
| gRPC | `PolicyService/BatchCheckPermission` | [handler.go:48](../../services/policy/internal/handler/handler.go#L48) | mTLS/cluster-internal | gRPC metadata | |

## preview service (Python FastAPI)

Router prefix `/api/v1/previews` ([routes.py:21](../../services/preview/app/api/routes.py#L21)).

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | GET `/api/v1/previews/{document_id}/thumbnail` | [routes.py:69](../../services/preview/app/api/routes.py#L69) | session (upstream) | body/headers | |
| REST | GET `/api/v1/previews/{document_id}/pages/{page_number}` | [routes.py:88](../../services/preview/app/api/routes.py#L88) | session | body/headers | |
| REST | POST `/api/v1/previews/{document_id}/regenerate` | [routes.py:108](../../services/preview/app/api/routes.py#L108) | session | body/headers | |
| REST | GET `/api/v1/previews/{document_id}/status` | [routes.py:135](../../services/preview/app/api/routes.py#L135) | session | body/headers | |
| REST | GET `/healthz` | [main.py:67](../../services/preview/app/main.py#L67) | public | n/a | |
| REST | GET `/readyz` | [main.py:72](../../services/preview/app/main.py#L72) | public | n/a | |

## search service

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | POST `/api/v1/search` | [handler.go:31](../../services/search/internal/handler/handler.go#L31) | session | `X-Tenant-ID` + `X-User-ID` + `X-Group-IDs` | |
| REST | GET `/api/v1/search/autocomplete` | [handler.go:32](../../services/search/internal/handler/handler.go#L32) | session | `X-Tenant-ID` + `X-User-ID` | |
| REST | POST `/api/v1/saved-searches` | [handler.go:33](../../services/search/internal/handler/handler.go#L33) | session | `X-Tenant-ID` + `X-User-ID` | |
| REST | GET `/api/v1/saved-searches` | [handler.go:34](../../services/search/internal/handler/handler.go#L34) | session | `X-Tenant-ID` + `X-User-ID` | |
| REST | DELETE `/api/v1/saved-searches/` (trailing slash!) | [handler.go:35](../../services/search/internal/handler/handler.go#L35) | session | `X-Tenant-ID` + `X-User-ID` | |

**gRPC:** 6 RPCs declared; not registered. `backend-only`.

**Flag:** DELETE route `/saved-searches/` with a trailing slash looks like a drift bug — Go 1.22 mux treats `/saved-searches/` and `/saved-searches/{id}` as different. Phase 3 item.

## signature service

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | POST `/api/v1/signatures/requests` | [handler.go:25](../../services/signature/internal/handler/handler.go#L25) | session | `X-Tenant-ID` + `X-User-ID` | |
| REST | GET `/api/v1/signatures/requests/{id}` | [handler.go:26](../../services/signature/internal/handler/handler.go#L26) | session | `X-Tenant-ID` | |
| REST | GET `/api/v1/signatures/document/{documentId}` | [handler.go:27](../../services/signature/internal/handler/handler.go#L27) | session | `X-Tenant-ID` | |
| REST | POST `/api/v1/signatures/requests/{id}/sign/{signerId}` | [handler.go:28](../../services/signature/internal/handler/handler.go#L28) | signer token (URL-embedded) | from request row | |
| REST | POST `/api/v1/signatures/requests/{id}/cancel` | [handler.go:29](../../services/signature/internal/handler/handler.go#L29) | session | `X-Tenant-ID` | |
| REST | GET `/api/v1/signatures/verify/{documentId}` | [handler.go:30](../../services/signature/internal/handler/handler.go#L30) | session | `X-Tenant-ID` | |

**gRPC:** 8 RPCs declared; not registered. `backend-only`.

## storage service (gRPC only)

No REST handlers — grpc server at [main.go:150](../../services/storage/cmd/server/main.go#L150). Called by frontend indirectly via grpc-gateway hosted in document service? **No** — document service only proxies document.proto. Storage RPCs are reached directly from the web via the storage service's grpc port using an internal gateway layer set up by deployment; in the dev stack there is no grpc-gateway for storage, so these are currently unreachable from browsers. Flag for Phase 3.

| Transport | Method | RPC | Proto |
|---|---|---|---|
| gRPC | `StorageService/InitiateUpload` | | [storage.proto:13](../../proto/vaultdms/v1/storage.proto#L13) |
| gRPC | `StorageService/CompleteUpload` | | [storage.proto:14](../../proto/vaultdms/v1/storage.proto#L14) |
| gRPC | `StorageService/AbortUpload` | | [storage.proto:15](../../proto/vaultdms/v1/storage.proto#L15) |
| gRPC | `StorageService/GetDownloadURL` | | [storage.proto:17](../../proto/vaultdms/v1/storage.proto#L17) |
| gRPC | `StorageService/GetPreviewURL` | | [storage.proto:18](../../proto/vaultdms/v1/storage.proto#L18) |
| gRPC | `StorageService/GetScanStatus` | | [storage.proto:20](../../proto/vaultdms/v1/storage.proto#L20) |
| gRPC | `StorageService/RequestLifecycle` | | [storage.proto:21](../../proto/vaultdms/v1/storage.proto#L21) |

## workflow service

| Transport | Method + path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| REST | GET `/api/v1/workflows/definitions` | [handler.go:27](../../services/workflow/internal/handler/handler.go#L27) | session | `X-Tenant-ID` | |
| REST | POST `/api/v1/workflows/definitions` | [handler.go:28](../../services/workflow/internal/handler/handler.go#L28) | session | `X-Tenant-ID` + `X-User-ID` | |
| REST | POST `/api/v1/workflows/instances` | [handler.go:29](../../services/workflow/internal/handler/handler.go#L29) | session | `X-Tenant-ID` + `X-User-ID` | |
| REST | GET `/api/v1/workflows/instances/{id}` | [handler.go:30](../../services/workflow/internal/handler/handler.go#L30) | session | `X-Tenant-ID` | |
| REST | POST `/api/v1/workflows/instances/{id}/signal` | [handler.go:31](../../services/workflow/internal/handler/handler.go#L31) | session | `X-Tenant-ID` + `X-User-ID` | |
| REST | POST `/api/v1/workflows/instances/{id}/cancel` | [handler.go:32](../../services/workflow/internal/handler/handler.go#L32) | session | `X-Tenant-ID` | |
| REST | GET `/api/v1/workflows/tasks/mine` | [handler.go:33](../../services/workflow/internal/handler/handler.go#L33) | session | `X-Tenant-ID` + `X-User-ID` | |

**gRPC:** 10 RPCs declared; not registered. `backend-only`.

## collaboration service (WebSocket)

| Transport | Path | Handler | Auth | Tenant scoping | Status |
|---|---|---|---|---|---|
| WebSocket | `/ws` on port 8083 | [index.js:15](../../services/collaboration/src/index.js#L15) | **none today** — upgrade is unauth'd | **none today** — no tenant binding | **⚠ P0 gap** |

See prior prompt's "WebSocket auth on collaboration service" finding. This is a known cross-tenant hijack risk.

**gRPC:** 13 RPCs in `collaboration.proto` (comments + share-link events + annotations + `StreamPresence`). Not registered. `backend-only`.

---

## NATS subjects (published or outbox event_type)

Grepped from `services/` + `pkg/`. Frontend reaches NATS only via the collaboration WebSocket fanout. Intelligence + preview consume but don't expose to frontend.

**Auth events** (publisher: auth service outbox):

- `dms.auth.api_key_issued.v1`
- `dms.auth.api_key_revoked.v1`
- `dms.auth.login_failed.v1`
- `dms.auth.login_success.v1`
- `dms.auth.logout.v1`
- `dms.auth.mfa_disabled.v1`
- `dms.auth.mfa_enabled.v1`
- `dms.auth.user_registered.v1`
- `dms.user.invited.v1`
- `dms.user.mfa_reset.v1`
- `dms.user.suspended.v1`

**Document events** (document service outbox):

- `dms.document.created.v1`
- `dms.document.deleted.v1`
- `dms.document.moved.v1`
- `dms.document.state_changed.v1`
- `dms.document.updated.v1`
- `dms.folder.created.v1`
- `dms.folder.moved.v1`
- `dms.sharelink.created.v1`
- `dms.version.created.v1`

**Permission events** (policy service outbox):

- `dms.permission.changed.v1` (referenced by search consumer)
- `dms.permission.granted.v1`
- `dms.permission.revoked.v1`

**Storage events** (storage service outbox):

- `dms.storage.upload_completed.v1`
- `dms.storage.upload_deduplicated.v1`
- `dms.storage.upload_initiated.v1`
- `dms.storage.upload_quarantined.v1`

**Intelligence events** (intelligence Celery tasks; Python-side direct JetStream publish):

- `dms.version.ocr_completed.v1` — publisher exists in Python (04a)
- `dms.version.classified.v1`
- `dms.version.entities_detected.v1`

**Preview events** (preview Celery tasks):

- `dms.version.preview_ready.v1`

**Signature/workflow events** (via outbox):

- `dms.notify.signature_requested.v1`
- `dms.notify.workflow_assigned.v1`
- `dms.signature.completed.v1`
- `dms.workflow.completed.v1`

**⚠ Referenced but NOT published anywhere:**

- `dms.version.uploaded.v1` — consumed by intelligence + preview; **no Go producer**. Known P0 bug from prior prompt.

Subscribers:

- audit service: `dms.>` (everything, for hash-chain log)
- connector service: `dms.>` (webhook fanout)
- notification service: `dms.notify.>`
- search service: `dms.document.*.v1`, `dms.version.ocr_completed.v1`, `dms.version.classified.v1`, `dms.version.entities_detected.v1`, `dms.permission.changed.v1`
- intelligence service: `dms.version.uploaded.v1`, `dms.version.ocr_completed.v1`
- preview service: `dms.version.uploaded.v1`

---

## Intentional backend-only (informational)

**Pure gRPC RPCs with no gateway route, no REST wrapper, not registered:**
117 RPCs across audit/auth/billing/collaboration/intelligence/notification/signature/storage/workflow — retained in proto files for future inter-service calls + `dms-admin` CLI tooling. Not flagged as gaps.

**`/internal/v1/*`** — billing service only. 6 endpoints listed above; all X-API-Key authed. Not user-facing by design.

**`/healthz`, `/readyz`, `/metrics`** — every service exposes these on port 8081; scraped by Prometheus / Kubernetes probes. Not user-facing.

---

## OpenAPI spec cross-check

`docs/api/openapi.yaml` currently documents **16 top-level paths**. Our inventory has **~121** user-facing paths. Documentation gap is large but **out of scope for this phase** — flagged for a doc-only follow-up. Paths documented in the spec all exist in this table; nothing documented-but-missing.

---

## Summary

- **REST endpoints (Go + Python):** 97 user-facing (excl. internal/v1 + healthz/metrics) + 10 internal/admin CLI
- **gRPC-gateway REST:** 24 (all in document service)
- **WebSocket:** 1 (`/ws` on collaboration — unauthenticated; P0 issue)
- **NATS subjects:** 35 unique, plus 1 consumer-only subject with no publisher (`dms.version.uploaded.v1`)
- **Intentionally backend-only:** 117 gRPC RPCs + 6 billing `/internal/v1` + 3 probe endpoints per service

**Known broken surfaces (will be explicit gaps in Phase 3):**

1. Workspace CRUD (`GET/POST /api/v1/workspaces`) has no handler on any service. Frontend calls it.
2. Storage gRPC has no HTTP gateway — browsers can't call `InitiateUpload` / `CompleteUpload` / `GetDownloadURL` directly. The document service's upload flow must be proxying it somehow (or not — this is likely why remediation 10's smoke test stalls at step 4).
3. Collaboration WebSocket has no session validation on upgrade.
4. `DELETE /api/v1/saved-searches/` with trailing slash mismatches the expected `/saved-searches/{id}` shape.
5. `dms.version.uploaded.v1` is consumed but not published — kills the OCR pipeline.

Those five items plus anything Phase 2 surfaces will drive the coverage matrix in Phase 3.
