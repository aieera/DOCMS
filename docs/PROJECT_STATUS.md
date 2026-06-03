# VaultDMS — Project Status

*Generated 2026-05-18 from a codebase scan. Reflects state of `feat/upload-scan-toggle-progress` branch with uncommitted in-flight work.*

---

# 1. Project Overview

VaultDMS is a multi-tenant enterprise Document Management System with strong compliance, eSignature, AI, and SaaS-integration surfaces. The platform is a polyglot microservices architecture (12 Go services, 1 Python ML worker, 1 Node co-authoring server, 1 Kotlin/JVM signing sidecar) sitting behind a React+TanStack-Router frontend and a Kong gateway. Documents flow through upload → virus scan → encrypted blob storage (per-tenant KEK envelope) → OCR/NER/classification (Python intelligence pipeline) → indexed in OpenSearch + Qdrant for hybrid search → optionally e-signed via QES/DocuSign/Adobe Sign. Tenant isolation is enforced at the Postgres layer via RLS + `NOBYPASSRLS` app role.

**Overall completion (honest estimate): ~75%.** The core platform (auth, documents, storage, search, signature, audit) is feature-complete and tested. The recent surface (per-tenant integration credentials, iPaaS API, native connectors for Google + 6 placeholder vendors, Drive/Gmail import) is partial. Production gaps are concentrated in: zero test coverage on 4 services, per-tenant KEK not yet wired, several "stub" mocks left in production code paths, and 6 of the 7 §12.4 native connectors not built.

**Last meaningful change:** `69d8740 — perf(storage): skip ClamAV scan in dev` (2026-05-15). The branch carries ~25 modified/new files of in-flight work (eSign per-tenant configs, Twilio/SMTP modal admin, Google Workspace connector OAuth slice, iPaaS triggers) that haven't been committed yet.

---

# 2. Tech Stack

## Frontend
- **Framework**: React 18.2
- **Router**: TanStack Router 1.15 (file-based, type-safe)
- **UI primitives**: Radix UI + 24 shadcn/ui wrappers
- **State**: Zustand 4.5 (3 stores) + TanStack React Query 5.17 (server state)
- **Styling**: Tailwind CSS 3.4 + Class Variance Authority
- **Forms**: React Hook Form 7.75 + Zod 3.22
- **GraphQL**: urql 5.0 with persisted queries
- **Build**: Vite 5.0
- **Testing**: Vitest 4.1 (unit), Playwright 1.45 (E2E)
- **Other**: Lucide icons, Sonner toasts, date-fns, React PDF

## Backend
- **Go services (12)**: Go 1.25 / toolchain 1.26.2. Routing: chi (auth, scim) + `net/http` ServeMux (everything else). gRPC for inter-service via `google.golang.org/grpc`.
- **Python service (1)**: `services/intelligence/` — ML pipeline (OCR, NER, classification, anomaly, RAG). FastAPI-style; LiteLLM for provider routing.
- **Node service (1)**: `services/collaboration/` — OnlyOffice/Collabora co-authoring (Yjs CRDT).
- **JVM service (1)**: `services/signature-signer/` — Kotlin/Gradle PAdES signing sidecar (DSS).
- **ORM**: None — raw pgx queries with parameterized SQL throughout. Migrations via golang-migrate.

## Database
- **Engine**: PostgreSQL 16
- **Hosting**: Single shared instance in dev (compose); per-region clusters in prod (Helm)
- **Schema management**: golang-migrate, per-service migration directories + per-service `<svc>_schema_migrations` bookkeeping
- **Tenant isolation**: RLS forced on every tenant-scoped table; app role is `NOBYPASSRLS` in prod (dev uses BYPASSRLS for convenience)

## Storage
- **Object store**: MinIO in dev, S3 in prod
- **Bucket layout**: one bucket per tier (hot/warm/cold) with tenant-prefixed keys
- **Encryption**: Per-blob DEK wrapped by per-tenant KEK (Vault/AWS KMS in prod, `SEDOC_LOCAL_KEK` in dev). **Note**: Per-tenant KEK derivation is tracked as tech debt — currently a single dev KEK serves all tenants.

## Auth
- **Primary**: HttpOnly session cookie (`dms_session`), SHA-256 hashed in `sessions` table
- **MFA**: TOTP, SMS (Twilio), email OTP, passkeys (WebAuthn) — ADR 0061, 0063
- **SSO**: SAML 2.0 + OIDC per tenant — `services/auth/internal/sso/`
- **Federated identity**: SCIM 2.0 provisioning + LDAP/AD direct-bind sync (ADR 0062)
- **API**: Bearer API keys (prefix `vdms_`) with scopes (ADR 0090)

## Deployment / hosting
- **Local dev**: Docker Compose, 23+ containers (`docker-compose.yml`, `docker-compose.prebuilt.yml`)
- **Prod**: Helm charts under `deploy/k8s/` (referenced but not in this audit scope)
- **Gateway**: Kong (`deploy/gateway/kong.yaml`) injecting `X-Gateway-Signature` header that every backend validates
- **CI**: GitHub Actions (`.github/workflows/`)

## Key third-party SDKs & services
- DocuSign + Adobe Sign (eSign), Swisscom + Intesi + InfoCert (QES/TSPs)
- Twilio Verify (SMS MFA), Stripe (billing), Google OAuth (connectors)
- AWS KMS / HashiCorp Vault (key management)
- ClamAV (malware), Temporal (workflows), NATS JetStream (event bus)
- OpenSearch (lexical search), Qdrant (vector search), Redis (cache + rate limit)
- LiteLLM (multi-provider LLM routing)

---

# 3. Repository Structure

```
DOCMS/
├── services/                    13 microservices (Go/Python/Node/Kotlin)
│   ├── auth/                    Sessions, MFA, SSO, LDAP, SCIM, API keys
│   ├── document/                Core: docs, folders, versions, perms, annotations
│   ├── storage/                 S3/MinIO blob upload + virus scan + encryption
│   ├── search/                  OpenSearch + Qdrant hybrid search, federated
│   ├── signature/               PAdES, QES, DocuSign, Adobe Sign, in-person
│   ├── workflow/                Temporal-backed approval/routing workflows
│   ├── audit/                   Append-only ledger, integrity proofs, DSR export
│   ├── policy/                  RBAC engine (gRPC permission checks)
│   ├── notification/            Email, in-app, digest, preferences
│   ├── billing/                 Stripe subscriptions, tenant provisioning
│   ├── connector/               Webhooks, native iPaaS (Google, etc.), MCP
│   ├── graphql-gateway/         GraphQL aggregator (ADR 0074)
│   ├── intelligence/            Python: OCR, NER, classify, anomaly, RAG
│   ├── collaboration/           Node: Yjs CRDT co-authoring server
│   ├── signature-signer/        Kotlin: DSS PAdES signing sidecar
│   └── preview/                 (Python; preview generation)
├── pkg/                         Shared Go libraries (auth, database, esign,
│                                middleware, crypto, events, notifications, …)
├── proto/                       Protobuf schemas + generated stubs (gRPC contracts)
├── web/                         React frontend (TanStack Router file routes)
│   ├── src/routes/              68 routes
│   ├── src/components/          83 components + 24 shadcn primitives
│   ├── src/api/                 52 typed API clients
│   ├── src/store/               3 Zustand stores
│   ├── src/hooks/               ~12 hooks
│   └── e2e/                     27 Playwright specs
├── docs/                        ADRs (46), runbooks, howtos, architecture
├── deploy/                      Kong gateway config, Helm charts
├── scripts/                     init-db.sql, seeders, wait-for-healthy.sh
└── docker-compose.yml           Local dev: 23+ containers
```

## Entry points

| Component | Entrypoint |
|---|---|
| auth service | `services/auth/cmd/server/main.go` |
| document service | `services/document/cmd/server/main.go` |
| storage service | `services/storage/cmd/server/main.go` |
| signature service | `services/signature/cmd/server/main.go` |
| search service | `services/search/cmd/server/main.go` |
| workflow service + worker | `services/workflow/cmd/server/main.go` + `services/workflow/cmd/worker/main.go` |
| audit service | `services/audit/cmd/server/main.go` |
| policy service | `services/policy/cmd/server/main.go` |
| notification service | `services/notification/cmd/server/main.go` |
| billing service | `services/billing/cmd/server/main.go` |
| connector service | `services/connector/cmd/server/main.go` |
| graphql-gateway | `services/graphql-gateway/cmd/server/main.go` |
| intelligence (Python) | `services/intelligence/app/main.py` |
| collaboration (Node) | `services/collaboration/src/index.ts` |
| frontend | `web/src/main.tsx` (Vite entry) → `web/src/routes/__root.tsx` |

---

# 4. Environment & Configuration

## Env variables (names only)

### Required core
- `SEDOC_DATABASE_URL` — Postgres connection string
- `SEDOC_REDIS_URL` — Redis address (cache + rate limits)
- `SEDOC_NATS_URL` — NATS JetStream URL (event backbone)
- `SEDOC_GATEWAY_SECRET` — Kong↔backend shared secret
- `SEDOC_LOCAL_KEK` — Base64 32-byte KEK for dev/local KMS

### Optional / per-service
- Server ports: `SEDOC_HTTP_PORT`, `SEDOC_GRPC_PORT`, `SEDOC_HEALTH_PORT`
- Storage: `SEDOC_S3_ENDPOINT`, `_ACCESS_KEY`, `_SECRET_KEY`, `_PUBLIC_BASE`, `SEDOC_MINIO_*` (aliases), `SEDOC_STORAGE_ENCRYPT_AT_REST`, `SEDOC_STORAGE_SKIP_VIRUS_SCAN`
- Auth: `SEDOC_WEBAUTHN_RPID`, `_ORIGINS`, `_DISPLAY_NAME`, `_HMAC_SECRET`
- KMS: `SEDOC_KMS_PROVIDER` (`local`|`vault`|`aws`)
- eSign env-mode (deployment-wide fallback; per-tenant DB row wins): `SEDOC_ESIGN_DOCUSIGN_*` (5 vars), `SEDOC_ESIGN_ADOBE_SIGN_*` (5 vars), `SEDOC_ESIGN_STATE_HMAC`
- QES TSP: `SEDOC_QES_SWISSCOM_*`, `SEDOC_QES_INTESI_*`, `SEDOC_QES_INFOCERT_*`, `SEDOC_QES_MOCK_OK`
- Twilio (env-mode fallback; per-tenant `tenant_twilio_configs` wins): `SEDOC_TWILIO_ACCOUNT_SID`, `_AUTH_TOKEN`, `_VERIFY_SID`
- SMTP (env-mode fallback; per-tenant `tenant_smtp_configs` wins): `SEDOC_SMTP_HOST`, `_PORT`, `_USER`, `_USERNAME` (duplicate — see tech debt), `_PASSWORD`, `_FROM`, `_STARTTLS`
- Billing: `STRIPE_WEBHOOK_SECRET`, `SEDOC_INTERNAL_API_KEY`
- Intel: `SEDOC_QDRANT_URL`, `_COLLECTION`, `SEDOC_INTELLIGENCE_EMBED_URL`, `SEDOC_ONLYOFFICE_*` (3 vars), LLM provider keys
- Misc: `SEDOC_PUBLIC_URL`, `SEDOC_ENV`, `SEDOC_DEFAULT_RATE_LIMIT_PER_MIN`

### Inter-service addresses
`POLICY_SERVICE_ADDR`, `STORAGE_SERVICE_ADDR`, `DOCUMENT_SERVICE_ADDR`, `WORKFLOW_SERVICE_ADDR`, `COLLABORATION_SERVICE_ADDR`, `AUDIT_SERVICE_ADDR`, `AUTH_SERVICE_ADDR`, `CLAMAV_ADDR`, `TEMPORAL_ADDR`, `OPENSEARCH_URL`

## Config files present

- `.env` (local-only secrets; not committed)
- `.env.example`, `.env.local.example`, `.env.aws.example` (templates)
- `docker-compose.yml`, `docker-compose.prebuilt.yml`, `docker-compose.prod.yml`, `docker-compose.passkeys.yml`
- `web/vite.config.ts` (per-prefix proxy routing in host mode)
- `web/tailwind.config.js`, `web/postcss.config.js`, `web/tsconfig.json`
- `proto/buf.yaml`, `proto/buf.gen.yaml`
- `deploy/gateway/kong.yaml`
- `Makefile`, `go.work`, per-service `go.mod`

## Missing / unset env variables I can detect

- The `.env` file on this dev box has DocuSign credentials but the `AUTHORIZE_URL` points at the **REST API host** (`demo.docusign.net`) instead of the **OAuth identity host** (`account-d.docusign.com`). This was flagged earlier in the session.
- `SEDOC_TWILIO_*`, `SEDOC_SMTP_*`, `SEDOC_WEBAUTHN_*` blocks have been added to `docker-compose.yml`'s `x-go-env` anchor with defaults of `""` — they remain empty in `.env`, so SMS MFA / email / passkeys fall back to stub/log mode.
- `SEDOC_ESIGN_ADOBE_SIGN_*` not set (only DocuSign is configured).

---

# 5. Database

## Migrations summary

| Service | Up-migrations | Status |
|---|---|---|
| document | 45 (000001 → 000045) | Applied through migration 000045 (connector_configs uniqueness, added this session) |
| audit | 2 | Applied |
| billing | 1 (000010) | Applied |
| connector | 1 (rename only) | Applied — note: the `connector_configs` table is created in document/000001 |
| intelligence | 3 | Applied |
| notification | 1 | Applied |
| search | 3 | Applied |
| **Total** | **56** | **All applied; no pending** |

## ~30 key tables (excluding bookkeeping/outbox)

| Table | Purpose | Notes |
|---|---|---|
| `organizations` | Tenants. NOT RLS-isolated (it IS the tenant). | |
| `users` | User accounts with role, MFA state | RLS forced |
| `groups`, `group_members` | Group directory | RLS forced |
| `sessions` | HTTP session tokens (SHA-256 hashed) | Single-col PK on `token_hash` |
| `api_keys` | Bearer keys with scopes, prefix-indexed | RLS forced |
| `workspaces`, `folders` | Document hierarchy | RLS forced |
| `documents` | Core doc records, soft-deleted | tags[], custom_metadata JSONB |
| `document_versions` | Version history per doc | with `current_version_id` on documents |
| `content_blobs` | Deduplicated encrypted blobs | Single-col PK for cross-tenant dedup |
| `permissions`, `policy_*` | RBAC grants (user/group → resource) | |
| `share_links` | Public/anonymous share tokens | RLS forced |
| `comments`, `comment_replies`, `comment_reactions` | Threaded discussion (ADR 0066) | RLS forced |
| `annotations`, `annotation_layers` | Drawing/highlight overlays (ADR 0067) | RLS forced |
| `signature_requests`, `signature_signers` | eSign envelopes + signers | FK on (tenant_id, version_id) |
| `esign_oauth_tokens` | Per-tenant DocuSign/Adobe Sign tokens (sealed) | ADR 0071 |
| `esign_provider_configs` | Per-tenant DocuSign/Adobe Sign client_id+secret (sealed) | Added 000043 (this session) |
| `tenant_twilio_configs`, `tenant_smtp_configs` | Per-tenant SMS/email creds (sealed) | Added 000044 (this session) |
| `connector_configs` | Native iPaaS configs (Google, Salesforce, M365…) | Repo+schema mismatch fixed 000045 (this session) |
| `audit_events` | Append-only event log, time-partitioned | RLS forced |
| `ocr_results`, `ocr_quality_scores` | OCR output + grading (ADR 0057) | |
| `document_classifications`, `classification_corrections` | ML class + active-learning feedback (ADR 0059, 0060) | |
| `document_entities`, `entity_corrections` | NER results + corrections (ADR 0078) | |
| `compliance_findings` | PII/PHI detections (ADR 0054) | |
| `anomaly_reports`, `anomaly_findings` | Outlier detection (ADR 0058) | |
| `model_versions`, `training_examples` | Active-learning ML registry (ADR 0060) | |
| `qa_conversations`, `qa_messages` | Document Q&A history (ADR 0055) | |
| `legal_holds`, `legal_hold_documents` | Litigation holds | RLS forced |
| `workflow_definitions`, `workflow_instances`, `workflow_tasks` | Temporal-backed approval workflows | |
| `tenant_keks`, `tenant_cmk_*` | Tenant key registry (ADR 0022, 0026) | |
| `dsr_requests`, `dsr_subjects` | GDPR data-subject requests (ADR 0024) | |
| `intake_drop_folders`, `intake_ingested_files` | Watched-folder ingestion (ADR 0088) | |
| `email_configs`, `email_ingested_messages` | Email→document pipeline (ADR 0087) | |
| `notification_preferences` | Per-user channel prefs (ADR 0086) | |
| `webhooks`, `webhook_deliveries` | Customer webhooks (ADR 0076) | |

## Seed data status

- `scripts/init-db.sql` — bootstraps the `vaultdms` DB role + extensions
- Tenant seed: one tenant (`aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa` = "Acme Local") + admin user `admin@acme.local`
- No production seed data — clean tenant per deploy

## ER summary (plain text)

`organizations` is the root of multi-tenancy. Every tenant table FKs to it via `tenant_id` and is RLS-isolated by `current_setting('app.current_tenant')`.

A `workspace` contains a `folder` tree which contains `documents`. Each `document` has many `document_versions`; each version references a `content_blob` (deduplicated globally by sha256, encrypted per-blob with a KEK-wrapped DEK).

A `signature_request` references one `(tenant_id, version_id)` pair and many `signature_signers`. Provider-specific tokens live in `esign_oauth_tokens`; new per-tenant client credentials live in `esign_provider_configs`.

ML/intel tables (`ocr_*`, `document_classifications`, `document_entities`, `compliance_findings`, etc.) all FK back to `(tenant_id, document_id)` and `(tenant_id, version_id)`.

Workflows: a `workflow_definition` (template) instantiates as a `workflow_instance` with N `workflow_tasks` (one per signer/approver). Temporal owns runtime state; the DB tables are the SoT for queryable history.

---

# 6. API / Endpoints

**~170 total** REST + gRPC endpoints across 12 Go services. Exhaustive list available via `make proto-gen` (generates OpenAPI) and via `grep -rE "mux.HandleFunc|r.(Get|Post|Put|Delete)"` per service. Grouped highlights below.

## Authentication & Identity (auth service, :8180)

| Method | Path | Auth | Purpose | Status |
|---|---|---|---|---|
| POST | `/api/v1/auth/register` | None (IP rate-limited) | Self-registration | ✅ Working |
| POST | `/api/v1/auth/login` | None (IP rate-limited) | Password login | ✅ Working |
| POST | `/api/v1/auth/logout` | Session | Revoke session | ✅ Working |
| GET | `/api/v1/auth/me` | Session | Current user | ✅ Working |
| POST | `/api/v1/auth/mfa/{email,sms,push}/{start,verify}` | Session | MFA challenge | ✅ Working |
| POST | `/api/v1/auth/webauthn/{login,registration}/{begin,finish}` | Mixed | Passkey enroll/auth | ✅ Working |
| GET/POST | `/api/v1/auth/saml/{tenant_slug}/{login,acs,metadata}` | None | SAML 2.0 flow | ✅ Working |
| GET | `/api/v1/auth/oidc/{tenant_slug}/{login,callback}` | None | OIDC flow | ✅ Working |
| GET/POST/DELETE | `/api/v1/auth/api-keys` | Session + role | Scoped API key CRUD | ✅ Working |
| GET/POST | `/api/v1/admin/users/*` | Session + admin | Tenant user admin | ✅ Working |
| GET/POST | `/api/v1/admin/groups/*` | Session + admin | Group admin | ✅ Working |
| GET/POST | `/api/v1/admin/sso-configs/*` | Session + admin | SSO config | ✅ Working |
| GET/POST | `/api/v1/admin/ldap/*` | Session + admin | LDAP/AD config + sync | ✅ Working |
| GET/POST | `/api/v1/admin/mfa/policy` | Session + admin | Tenant MFA policy | ✅ Working |
| GET/PUT/DELETE/POST | `/api/v1/admin/notifications/twilio` + `/twilio/test` | Session + admin | Per-tenant Twilio (this session) | ✅ Working |
| GET/PUT/DELETE/POST | `/api/v1/admin/notifications/smtp` + `/smtp/test` | Session + admin | Per-tenant SMTP (this session) | ✅ Working |
| GET/POST/DELETE | `/scim/v2/{tenant_slug}/*` | Bearer token | SCIM 2.0 provisioning | ✅ Working |

## Documents & Content (document service, :8182)

| Method | Path | Auth | Purpose | Status |
|---|---|---|---|---|
| POST/GET/PATCH/DELETE | `/api/v1/documents` + `/{id}` | Session + perm | Document CRUD | ✅ Working |
| GET | `/api/v1/documents/{id}/versions` | Session + perm | List versions | ✅ Working |
| POST | `/api/v1/documents/{id}/versions` | Session + perm | New version | ✅ Working |
| POST | `/api/v1/documents/{id}/versions/{vid}/restore` | Session + perm | Restore old version | ✅ Working |
| GET/POST | `/api/v1/documents/{id}/comments` | Session + perm | Threaded comments | ✅ Working |
| POST/PATCH/DELETE | `/api/v1/comments/{cid}/*` | Session + author | Reply/edit/resolve/react | ✅ Working |
| POST | `/api/v1/documents/{id}/versions/{vid}/annotations` | Session + perm | Layer annotations | ✅ Working |
| GET/POST | `/api/v1/documents/{id}/tag-suggestions` | Session + perm | Auto-tag review | ✅ Working |
| POST | `/api/v1/documents/{id}/classify/correct` | Session + perm | Classification feedback | ✅ Working |
| GET | `/api/v1/documents/{id}/compliance` | Session + perm | PII/PHI findings | ✅ Working |
| POST | `/api/v1/documents/{id}/versions/{vid}/qa` | Session + perm | Document Q&A | ✅ Working |
| POST | `/api/v1/documents/{id}/translate` | Session + perm | Translation | ✅ Working |
| GET/POST | `/api/v1/workspaces` + `/{id}/folders` | Session + perm | Workspace + folder mgmt | ✅ Working |
| POST/GET/DELETE | `/api/v1/shared/{token}` | Token | Public share-link view | ✅ Working |
| GET/POST | `/api/v1/tasks/mine` + `/api/v1/tasks/{id}/*` | Session | Lightweight tasks (ADR 0068) | ✅ Working |
| POST | `/api/v1/admin/bulk/{import,export}` | Session + admin | NDJSON bulk migrate (ADR 0075) | ✅ Working |
| GET | `/api/v1/integrations/triggers/documents` | API key | iPaaS poll for new docs (this session) | ✅ Working |

## Storage (storage service, :8183, gRPC primarily)

| Method | Path | Auth | Purpose | Status |
|---|---|---|---|---|
| gRPC | `StorageService/InitiateUpload` | mTLS (internal) | Get presigned PUT URL | ✅ Working |
| gRPC | `StorageService/CompleteUpload` | mTLS | Scan + persist blob | ✅ Working |
| gRPC | `StorageService/GetDownloadURL` | mTLS | Presigned GET URL | ✅ Working |
| POST | `/api/v1/storage/downloads/{docId}/{verId}` (via doc service) | Session + perm | Download a version | ✅ Working |

## Search (search service, :8184)

| Method | Path | Auth | Purpose | Status |
|---|---|---|---|---|
| POST | `/api/v1/search` | Session + perm | Full-text + facets + filters | ✅ Working |
| GET | `/api/v1/search/autocomplete` | Session | Type-ahead (ADR 0084) | ✅ Working |
| GET/POST/PATCH | `/api/v1/saved-searches` | Session | Saved query CRUD (ADR 0085) | ✅ Working |
| POST | `/api/v1/saved-searches/{id}/subscribe` | Session | Alert subscription | ✅ Working |
| POST | `/api/v1/platform/search/federated` | Platform admin | Cross-tenant support search (ADR 0069) | ✅ Working |
| GET | `/api/v1/admin/permission-propagation-stats` | Session + admin | Index lag SLI (ADR 0083) | ✅ Working |

## Signature (signature service, :8188)

| Method | Path | Auth | Purpose | Status |
|---|---|---|---|---|
| POST | `/api/v1/signatures/requests` | Session + perm | Create signature request | ✅ Working |
| GET | `/api/v1/signatures/document/{docId}` | Session + perm | List signatures on doc | ✅ Working |
| POST | `/api/v1/signatures/requests/{id}/sign/{signerId}` | Session/token | Record signature (internal) | ✅ Working |
| POST | `/api/v1/signatures/esign/send` | Session | Hand off to DocuSign/Adobe | ✅ Working |
| GET | `/api/v1/signatures/esign/connections` | Session + admin | List vendor connections | ✅ Working |
| POST | `/api/v1/signatures/esign/oauth/start` | Session + admin | Begin vendor OAuth | ✅ Working |
| GET | `/api/v1/signatures/esign/oauth/callback` | Vendor (whitelisted) | OAuth return | ✅ Working |
| POST | `/api/v1/signatures/esign/webhook/{provider}/{tenant}` | HMAC | Vendor webhook | ✅ Working |
| GET/PUT/DELETE | `/api/v1/signatures/esign/provider-config/{provider}` | Session + admin | Per-tenant client creds (this session) | ✅ Working |
| GET | `/api/v1/signatures/verify/{docId}` | Session + perm | PAdES LTV verify (ADR 0072) | ✅ Working |
| GET | `/api/v1/integrations/triggers/signatures/completed` | API key | iPaaS poll (this session) | ✅ Working |

## Connectors & iPaaS (connector service, :8190)

| Method | Path | Auth | Purpose | Status |
|---|---|---|---|---|
| GET | `/api/v1/connectors` | Session + admin | List native connectors | ✅ Working |
| GET | `/api/v1/connectors/{provider}` | Session + admin | Connector status | ✅ Working |
| GET | `/api/v1/connectors/{provider}/auth-url` | Session + admin | OAuth start (Google only today) | ✅ Working |
| GET | `/api/v1/connectors/oauth/callback` | Vendor | Unified OAuth callback | ✅ Working |
| PUT | `/api/v1/connectors/google/config` | Session + admin | Save Google creds (this session) | ✅ Working |
| POST | `/api/v1/connectors/google/disconnect` | Session + admin | Clear Google tokens | ✅ Working |
| GET/POST/PUT/DELETE | `/api/v1/webhooks*` | Session + admin | Customer webhooks (ADR 0076) | ✅ Working |
| GET/POST/PUT | `/api/v1/admin/email-configs/*` | Session + admin | Email ingestion (ADR 0087) | ✅ Working |
| GET/POST | `/api/v1/admin/intake/*` | Session + admin | Watched-folder intake (ADR 0088) | ✅ Working |
| GET/POST | `/api/v1/admin/event-stream/*` | Session + admin | Per-tenant event firehose (ADR 0077) | ✅ Working |
| POST | `/api/v1/mcp` | API key | MCP server SSE | 🟡 Stub (returns `{status:"ok"}` for tool calls) |

## Workflow (workflow service, :8186)

| Method | Path | Auth | Purpose | Status |
|---|---|---|---|---|
| POST/GET/DELETE | `/api/v1/workflows/*` | Session + perm | Workflow definitions & instances | ✅ Working |
| POST | `/api/v1/workflows/{id}/decisions/{taskId}` | Session + assignee | Approve/reject | ✅ Working |
| GET | `/api/v1/integrations/triggers/workflows/completed` | API key | iPaaS poll (this session) | ✅ Working |

## Notification, Billing, Audit, Policy, GraphQL

- Notification: `/api/v1/notifications` list/mark/preferences — ✅ Working
- Billing (internal, `/internal/v1/*`): tenant provision, Stripe webhook, plans, features — ✅ Working
- Audit: list, export, integrity verify, DSR — ✅ Working
- Policy: gRPC only — ✅ Working
- GraphQL: `POST /api/v1/graphql` (ADR 0074) — ✅ Working, doc-detail consumer

---

# 7. Frontend Pages & Routes

**68 routes total** — file-based via TanStack Router. Auth-gated routes sit under `_authenticated/`. Most use shared `AppLayout` (sidebar + topbar).

## Public routes

| Route | Purpose | Components Used | Status |
|---|---|---|---|
| `/login` | Login + MFA | `AuthShell`, `StepUpDialog` | ✅ |
| `/register` | Self-register | `AuthShell` | ✅ |
| `/forgot-password` | Reset flow | `AuthShell` | ✅ |
| `/accept-invite` | Accept tenant invite | `AuthShell` | ✅ |
| `/shared/$token` | Public share-link preview (no auth) | `DocumentViewer` | ✅ |

## Authenticated app routes

| Route | Purpose | Components Used | Status |
|---|---|---|---|
| `/` | Dashboard (KPIs, tasks, notifications) | `Card`, `EmptyState` | ✅ |
| `/workspaces` | Workspace list | `WorkspaceSelector`, `Card` | ✅ |
| `/workspaces/$workspaceId` | Workspace home (folders, recent docs) | `FolderTree`, `DocumentList` | ✅ |
| `/workspaces/$workspaceId/documents/$documentId` | Doc detail with viewer | `DocumentViewer`, `CommentsPanel`, `MetadataPanel`, `VersionHistory`, `SignaturesPanel`, `EntitiesPanel`, `CompliancePanel`, `OcrQualityPanel`, `TranslationPanel`, `AnnotationToolbar`, `RedactionTool`, `CoauthorEditor`, … | ✅ |
| `/search` | Hybrid search w/ facets | `SearchInput`, `DocumentCard`, faceted filter UI | ✅ |
| `/ask` | RAG chat over corpus | `AIChatPanel`, `CitationHighlight`, `SuggestedQuestions` | ✅ |
| `/tasks` | Task inbox | `DataTable` | ✅ |
| `/notifications` | Activity feed | `DataTable` | ✅ |
| `/saved-searches` | Saved queries | `DataTable` | ✅ |
| `/trash` | Soft-deleted documents | `DocumentList`, `BulkActionBar` | ✅ |
| `/settings/security` + `/mfa` + `/mfa/recovery` | Security settings | `StepUpDialog` | ✅ |
| `/settings/notifications` | Channel prefs | Form components | ✅ |
| `/signatures/send/$documentId` | New signature request | form, recipients editor | ✅ Fixed this session (version_id bug) |
| `/sign/$requestId/$signerId` | Sign document | `SignaturePad`, `DocumentViewer` | ✅ |
| `/sign/in-person/$requestId` | In-person sequential signing (ADR 0073) | `SignaturePad` | ✅ |
| `/sign/done` | Signature completion | static | ✅ |
| `/workflows/designer` | Visual workflow builder | `WorkflowGraph` | ✅ |
| `/workflows/instances/$instanceId` | Workflow run detail | `WorkflowGraph` | ✅ |

## Admin routes

| Route | Purpose | Status |
|---|---|---|
| `/admin` | Admin tile-grid hub (26+ tiles) | ✅ |
| `/admin/users` | User CRUD, role, suspend | ✅ |
| `/admin/groups` | Group mgmt | ✅ |
| `/admin/permissions` + `/permission-lag` | RBAC matrix + propagation stats | ✅ |
| `/admin/api-keys` | Service token mgmt | ✅ |
| `/admin/audit-log` | Append-only audit ledger | ✅ |
| `/admin/tags`, `/metadata-schema` | Taxonomy + custom fields | ✅ |
| `/admin/retention`, `/legal-holds` | Lifecycle policy | ✅ |
| `/admin/share-links` | Tenant-wide share link view | ✅ |
| `/admin/compliance`, `/privacy`, `/residency` | Compliance dashboards | ✅ |
| `/admin/billing`, `/settings` | Plan + tenant config | ✅ |
| `/admin/bulk` | NDJSON import/export | ✅ |
| `/admin/sso` | SAML/OIDC config | ✅ |
| `/admin/webhooks` | Webhook subscriptions | ✅ |
| `/admin/workflows` | Workflow templates | ✅ |
| `/admin/connectors` | Storage/email/identity connectors | ✅ |
| `/admin/integrations` (index) | eSign + Twilio + SMTP + Connectors tabs | ✅ Restructured this session |
| `/admin/integrations/events` | NATS event streaming console (ADR 0077) | ✅ |
| `/admin/integrations/email` | Email ingestion (ADR 0087) | ✅ |
| `/admin/integrations/ipaas` | Zapier/Make/n8n API keys + trigger URLs (ADR 0090) | ✅ Added this session |
| `/admin/tenant/ai`, `/tenant/identity/ldap` | Tenant LLM + LDAP config | ✅ |
| `/admin/intelligence/*` (11 sub-pages) | ML/intel admin: models, usage, compliance, NER, OCR, tags, anomalies, filing-analytics, routing-rules | ✅ |
| `/admin/platform/support-search` | Platform-admin cross-tenant search | ✅ |

---

# 8. Components Inventory

**83 custom components + 24 shadcn primitives.** Full list in agent report; highlights below grouped by domain. Props vary — TypeScript prop interfaces live alongside each component file.

| Component | Location | Used in | Complexity | Status |
|---|---|---|---|---|
| `AppLayout` | `components/layout/app-layout.tsx` | All `_authenticated` | M | ✅ |
| `AppSidebar` | `components/layout/app-sidebar.tsx` | `AppLayout` | L | ✅ |
| `DocumentViewer` | `components/viewer/DocumentViewer.tsx` | doc-detail, sign, shared | L | ✅ |
| `PDFViewer` / `PDFLayoutViewer` | `components/viewer/` | `DocumentViewer` | L | ✅ |
| `ImageViewer`, `VideoPlayer`, `TextViewer`, `UnsupportedFormat` | `components/viewer/` | `DocumentViewer` | M/S | ✅ |
| `CoauthorEditor` | `components/viewer/CoauthorEditor.tsx` | doc-detail (when collab on) | L | ✅ |
| `AnnotationToolbar` + `ImageAnnotationLayer` + `VideoAnnotationLayer` | `components/viewer/` | doc-detail | M | ✅ |
| `DocumentUpload` + `UploadProgress` + `BulkActionBar` | `components/documents/` | doc list, workspace | M/L | ✅ |
| `DocumentList` + `DocumentCard` | `components/documents/` | workspace, search, trash | M | ✅ |
| `MetadataPanel`, `TagEditor`, `VersionHistory`, `SignaturesPanel`, `CommentsPanel`, `ShareDialog` | `components/documents/` | doc-detail | M/L | ✅ |
| `AIChatPanel`, `DocQAChat`, `SummaryButton` | `components/intelligence/` | doc-detail, `/ask` | L | ✅ |
| `EntitiesPanel` + `HighlightedText` + `CitationHighlight` | `components/intelligence/` | doc-detail, `/ask` | M | ✅ |
| `CompliancePanel` + `ComplianceBadge`, `OcrQualityPanel` + `OcrQualityBadge`, `RedactionReviewPanel`, `RedactionTool`, `TranslationPanel` + `TranslationViewer`, `LanguageBadge`, `ClassificationBadge`, `CorrectClassificationButton`, `SuggestedQuestions` | `components/intelligence/` | doc-detail, admin/intelligence | M | ✅ |
| `WorkspaceAISettings` | `components/intelligence/` | admin/tenant/ai | M | ✅ |
| `AuditLogTable`, `UserTable`, `ComplianceDashboard` | `components/admin/` | admin pages | M/L | ✅ |
| `ESignCredentialsModal`, `TwilioCredentialsModal`, `SMTPCredentialsModal`, `GoogleWorkspaceModal` | `components/admin/` | `/admin/integrations` | M | ✅ Added this session |
| `StepUpDialog` | `components/auth/` | login, settings | M | ✅ |
| `SignaturePad`, `SignatureValidityBadge` | `components/signatures/` | `/sign`, doc-detail | M/S | ✅ |
| `CommandPalette` | `components/shared/` | `AppLayout` (Ctrl+K) | L | ✅ |
| `WorkflowGraph` | `components/shared/` | workflow pages | L | ✅ |
| `PageHeader`, `ErrorBoundary`, `LoadingScreen`, `Spinner`, `EmptyState`, `ErrorState`, `DataTable`, `SearchInput`, `Skeleton`, `Card`, `Dialog`, `FileIcon`, `Form`, `Label`, `Breadcrumb` | `components/shared/` + `components/ui/` | many | S/M | ✅ |
| **24 shadcn primitives** | `components/ui/shadcn/` | many | S | ✅ |

Component status is uniformly ✅ — no `// TODO` or `// FIXME` comments found anywhere in `web/src`.

---

# 9. Features Status

## ✅ Completed Features

- **User auth — signup, login, logout, password reset, 2FA** (auth service, `/services/auth/internal/`, ADR 0061/0063)
- **MFA: TOTP, SMS, email OTP, passkeys** (ADR 0063, 0061; per-tenant Twilio added this session)
- **SSO — SAML 2.0 + OIDC** per tenant
- **SCIM 2.0 provisioning** (auth)
- **LDAP/AD direct-bind sync** (ADR 0062)
- **Roles & permissions** — owner / admin / member / guest / compliance_officer; per-doc/folder/workspace grants via `policy` service
- **Document upload — single, bulk, drag-drop, chunked, resumable** (`components/documents/DocumentUpload.tsx` + storage service)
- **File-type validation + ClamAV virus scan** (`services/storage/internal/scanner/`)
- **Folder hierarchy + workspaces** (document service)
- **Tags + custom metadata (JSON Schema)** (document service)
- **Full-text + faceted + autocomplete + semantic (Qdrant) hybrid search** (search service; ADR 0082, 0083, 0084)
- **Saved searches with email alerts** (ADR 0085)
- **Federated cross-tenant support search** (ADR 0069)
- **Document preview — PDF, images, video, text, code** (viewer components)
- **Version history + rollback** (document service)
- **Threaded comments + reactions** (ADR 0066)
- **Annotations (highlight, redact, comment) on PDF/image/video** (ADR 0067)
- **Real-time co-authoring** (collaboration service + Yjs; ADR 0065)
- **Sharing — public links, expiry, password, view count** (document service)
- **Per-document/folder permissions** (policy service)
- **Audit log + DSR export + integrity verification (Merkle)** (audit service; ADR 0024)
- **Notifications — in-app + email + digest + preferences** (notification service; ADR 0086)
- **Soft-delete + trash + restore** (document service)
- **Bulk actions — tag, delete, export NDJSON** (ADR 0075)
- **Export — single download + ZIP** (storage)
- **OCR + quality scoring + page-level review** (intelligence service; ADR 0057)
- **NER + classification + active learning** (ADR 0078, 0059, 0060)
- **PII/PHI compliance detection + review workflow** (ADR 0054)
- **Auto-tagging + smart routing** (ADR 0052, 0053)
- **Document Q&A (RAG) over corpus** (ADR 0055, 0080)
- **Anomaly detection + review** (ADR 0058)
- **Translation pipeline** (ADR 0056)
- **Redaction review workflow** (ADR 0079)
- **E-signature — internal (in-app) + DocuSign + Adobe Sign + 3× QES TSPs** (ADR 0070, 0071)
- **PAdES LTV verification** (ADR 0072)
- **In-person + mobile signing** (ADR 0073)
- **Approval workflows (Temporal-backed)** (ADR 0064)
- **Lightweight tasks** (ADR 0068)
- **Customer webhooks** with HMAC signing + redelivery (ADR 0076)
- **Per-tenant event streaming (NATS firehose)** (ADR 0077)
- **GraphQL read API** (ADR 0074)
- **Bulk import/export NDJSON** (ADR 0075)
- **Email ingestion** — IMAP/Gmail/M365 → documents (ADR 0087)
- **Watched-folder intake** (ADR 0088)
- **API keys with scopes** (auth + middleware)
- **Per-tenant Stripe billing + usage tracking** (billing service)
- **Legal holds** (document service)
- **Data residency** (ADR 0007 region pinning)
- **Dark mode** (`components/layout/theme-provider.tsx`, full app)
- **Settings page** (`/settings/*` + `/admin/settings`)
- **Mobile-responsive** (Tailwind responsive classes throughout)
- **Per-tenant eSign provider config UI** (ADR 0071 extension — this session)
- **Per-tenant Twilio + SMTP config UI** (this session)
- **Google Workspace OAuth connector (handshake only)** (ADR 0089 — this session)
- **iPaaS (Zapier/Make/n8n) trigger endpoints + API key admin UI** (ADR 0090 — this session)

## 🟡 In Progress / Partial

- **Native connector — Google Workspace**: OAuth handshake works; **Drive/Gmail import action not yet built** (this session, deferred). Files: `services/connector/internal/providers/google/google.go`, `services/connector/internal/service/google.go`.
- **MCP server** for AI agents: routes exist at `POST /api/v1/mcp`, tool handlers return `{"status":"ok"}` stub. File: `services/connector/internal/mcp/server.go`.
- **Per-tenant KEK**: code currently uses one deployment-wide KEK; per-tenant derivation tracked in `docs/tech-debt/per-tenant-kek.md`, code TODO at `services/storage/cmd/server/main.go:137`.
- **DSS sidecar PAdES signer**: shell client compiles but returns `ErrNotConfigured`; mock signer is the runtime default. File: `services/signature/internal/signer/factory.go:34`.
- **WebAuthn (passkeys)**: routes wired, env propagation done, but the service methods return 501 — `services/auth/internal/service/webauthn.go` is a stub package.
- **Workflow signature handler**: `services/workflow/internal/workflows/signature_stub.go` is a placeholder that creates tasks and waits — full PAdES sealing deferred.
- **Vector search**: `services/search/internal/service/service.go:87` notes the vector path returns an empty list — hybrid mode degrades to lexical-only until Qdrant query side is wired.
- **i18n**: no i18n library — all UI text hardcoded English. Single locale.

## 🔴 Planned / Not Started

- **Salesforce, M365, SAP ArchiveLink, ServiceNow, Workday, NetSuite, QuickBooks, Xero connectors** — placeholder cards on `/admin/integrations` Connectors tab. ADR 0089 frames the work; only Google is wired.
- **Zapier app build** (on zapier.com/developer/builder) — backend surface ready, vendor-side work pending.
- **Make module** (on make.com Apps Developer panel) — same.
- **n8n community node** (separate `n8n-nodes-vaultdms` npm package) — same.
- **Per-tenant rate limiting buckets** — `pkg/middleware/ratelimit.go` exists with shared bucket; per-tenant + per-route bucketing not started.
- **Sync health dashboard** for connectors (last-sync timestamps, error rate, queue depth visibility) — fields exist (`connector_configs.last_sync_at`, `sync_status`) but no dedicated UI page.
- **Playwright per-connector tests** — only `53-esign-connectors.spec.ts` exists.

---

# 10. Integrations

| Service | Purpose | Status | Wired in |
|---|---|---|---|
| **DocuSign** | eSign — envelopes, recipients, webhook | ✅ Wired + tested live in this session | `services/signature/internal/handler/esign.go`, `pkg/esign/docusign.go` |
| **Adobe Sign** | eSign | ✅ Wired (untested with real account) | `pkg/esign/adobesign.go` |
| **Swisscom / Intesi / InfoCert QES** | eIDAS qualified e-sig (TSP) | ✅ Wired (sandbox tests pass) | `pkg/signing/tsp/`, `services/signature/internal/signer/qes.go` |
| **Twilio Verify** | SMS MFA | ✅ Wired; per-tenant UI added this session | `pkg/notifications/sms.go`, `services/auth/internal/service/twilio_admin.go` |
| **SMTP (any provider)** | Email — OTP, invites, signatures, notifications | ✅ Wired; per-tenant UI added this session | `services/notification/internal/service/smtp.go`, `services/auth/internal/service/smtp_admin.go` |
| **Stripe** | SaaS billing + webhook | ✅ Wired | `services/billing/internal/handler/handler.go` |
| **Google Workspace** | OAuth + Drive/Gmail (action deferred) | 🟡 OAuth only | `services/connector/internal/providers/google/`, `services/connector/internal/service/google.go` |
| **SAML 2.0 IdPs** (Okta, Azure AD, OneLogin) | Enterprise SSO | ✅ Wired | `services/auth/internal/sso/saml.go` |
| **OIDC IdPs** | Enterprise SSO | ✅ Wired | `services/auth/internal/sso/oidc.go` |
| **SCIM 2.0 IdPs** (Okta, Azure AD) | User/group sync | ✅ Wired | `services/auth/internal/scim/handler.go` |
| **LDAP / Active Directory** | Direct bind + sync | ✅ Wired (ADR 0062) | `services/auth/internal/ldap/` |
| **OpenSearch 2.12** | Lexical + faceted search | ✅ Wired | `services/search/internal/service/` |
| **Qdrant 1.7** | Vector search | 🟡 Wired for ingest only; query stub | `services/search/internal/service/service.go:87` |
| **MinIO / S3** | Object storage | ✅ Wired | `pkg/storage/`, `services/storage/internal/` |
| **NATS JetStream 2.10** | Event backbone | ✅ Wired | `pkg/events/`, all services |
| **Temporal** | Workflow orchestration | ✅ Wired | `services/workflow/cmd/worker/main.go` |
| **ClamAV** | Malware scanning | ✅ Wired (toggleable per env) | `services/storage/internal/scanner/` |
| **OnlyOffice / Collabora** | CRDT co-authoring | ✅ Wired | `services/collaboration/` |
| **LiteLLM** | LLM provider routing (OpenAI, Anthropic, …) | ✅ Wired | `services/intelligence/app/llm_routing.py` |
| **AWS KMS / Vault** | Production key management | 🟡 Stubbed (`pkg/crypto/kms.go` adapters present, prod wiring incomplete) | `pkg/crypto/` |
| **OPA** | Policy-as-code | 🔴 Not started (mentioned in CLAUDE.md but no code) | — |
| **Mailtrap** | Dev SMTP relay | ✅ Compatible (any SMTP works; documented in `.env` template) | — |

---

# 11. Background Jobs / Queues

## NATS JetStream subscriptions (ADR 0077)
Topic taxonomy: `dms.{domain}.{action}.v1`. ~60 event types. Producers via per-service `outbox` table + polling publisher. Consumers across services.

| Stream | Producer | Consumers |
|---|---|---|
| `dms.version.uploaded.v1` | document service | search-indexer, intelligence-worker (OCR/NER/classify), audit, notification |
| `dms.document.created/updated/deleted.v1` | document | search-indexer, audit, webhooks |
| `dms.signature.requested/completed.v1` | signature | document (new version on completion), audit, notification, webhooks |
| `dms.compliance.completed.v1` | intelligence | notification, document UI |
| `dms.classify.corrected.v1` | document | intelligence (training-collector) |
| `dms.model.promoted.v1` | intelligence | intelligence (cache evict) |
| `dms.notification.send.v1` | many | notification consumer → SMTP/in-app |
| `dms.webhook.delivery.v1` | connector | connector delivery worker |

## Temporal workflows (ADR 0023)
- **Approval workflows** — multi-step routing with delegation, recall (ADR 0064)
- **DSR execution** — export + anonymization pipeline (ADR 0024)
- **Retention enforcement** — periodic sweep against lifecycle states
- **Saved-search alerting** — periodic re-evaluation (ADR 0085)
- **Signature reconciler** — 5-min poll against DocuSign/Adobe Sign for envelopes missed by webhook
- Worker entrypoint: `services/workflow/cmd/worker/main.go`

## In-process tickers
- **Outbox publisher** — every service has one, polls own `outbox` table at 1 s, publishes to NATS
- **Webhook delivery worker** — `services/connector/internal/webhook/delivery_worker.go`, exponential backoff
- **OCR-quality periodic scan** — intelligence
- **Anomaly detection** — intelligence
- **LDAP sync** — auth, 15-min ticker with ±60s jitter
- **Email-ingestion polling** — connector (when IMAP source configured)
- **Watched-folder intake** — connector (filesystem watcher)

## Python intelligence-worker tasks
Run by `services/intelligence/app/worker.py`, Celery-style queue consumed off NATS:
`app.tasks.ocr`, `app.tasks.classify`, `app.tasks.ner`, `app.tasks.extract`, `app.tasks.embed`, `app.tasks.duplicate`, `app.tasks.dup_embedding`, `app.tasks.auto_tag`, `app.tasks.smart_route`, `app.tasks.compliance_scan`, `app.tasks.rag.stream_ask`, `app.tasks.lang_detect`, `app.tasks.translate`, `app.tasks.ocr_quality`, `app.tasks.anomaly_detect.run`, `app.tasks.training_collector`, `app.tasks.model_retrain`, `app.tasks.model_evaluate`, `app.tasks.summarize`, `app.tasks.redact` — all `acks_late=True`, ≤3 retries, dedupe via `intel_processed_events`, DLQ on terminal failure.

---

# 12. Tests

| Layer | Framework | File count | Coverage assessment |
|---|---|---|---|
| Go unit | `testing` + `testify` | **82 files** | Strong on document (27), search (13), workflow (8), auth (7), signature (7), notification (5), storage (4); thin on connector (2), audit (2), billing (2), policy (2), graphql-gateway (3) |
| Go integration | `testcontainers` (build tag `integration`) | Various | Per-service, run with `go test -tags integration` |
| Python | pytest | 0 found in `services/intelligence/tests/` per the audit (gap) | **ZERO tests** |
| Node | none in `services/collaboration/` | 0 | **ZERO tests** |
| Frontend unit | Vitest 4.1 | **9 files** (`web/src/**/__tests__/`) | Thin — focused on auth + a11y + utility |
| Frontend E2E | Playwright 1.45 | **27 specs** in `web/e2e/` | Broad — covers MFA, search, comments, annotations, eSign, QES, co-authoring, RAG, webhooks, event streaming, email ingestion |

**Services with ZERO tests:** `collaboration`, `intelligence`, `preview`, `signature-signer`

**Recent test status:** Commit `7bb3bf8` notes "test: green up the four pre-existing failures before v0.5 cut" — a hint at recent instability; current `go build ./...` is clean, full test run untested in this audit.

---

# 13. Known Bugs

## Explicit TODO/FIXME/HACK markers (4 total)

| File:Line | Comment |
|---|---|
| `services/storage/cmd/server/main.go:159` | `TODO(per-tenant-kek): Single KEK across all tenants` |
| `services/signature/internal/signer/factory.go:34` | `TODO(Wave 9.2b): real gRPC client. Today we return the shell client so imports compile; Sign returns ErrNotConfigured` |
| `services/intelligence/app/events/publisher.py:9` | `The durability story for publish failures is documented as a TODO` |
| `docs/tech-debt/per-tenant-kek.md:122` | reference to the storage TODO |

## Functional issues discovered this session (some fixed)

| Issue | Status |
|---|---|
| Connector repository SQL referenced columns that don't exist (`provider`, `config` vs real `connector_type`, `config_encrypted`) | ✅ Fixed migration 000045 + repo update |
| Signature `/esign/connections` and `/esign/envelopes` returned **500** on missing `X-Auth-Tenant-ID` instead of 401 | ✅ Hardened with header validation |
| Connector `/connectors/{provider}` masked errors as 404 | ✅ Hardened to distinguish 401/500/404 |
| DocuSign OAuth callback rejected with "provider, code, state required" — vendors don't send `?provider=`; provider must ride inside state | ✅ Fixed state format `<tenantID>.<provider>.<hmac>` |
| DocuSign token row had empty `account_id` + `base_uri` (no userinfo call after token exchange) — would have blocked actual Send | ✅ Added `FetchAccountInfo` in `pkg/esign/oauth.go` |
| `docker-compose.yml` `x-go-env` anchor didn't propagate `SEDOC_ESIGN_*` / `_TWILIO_*` / `_SMTP_*` / `_WEBAUTHN_*` envs to containers | ✅ Added all four blocks |
| Reload-race: authStore empty on hard refresh → first batch of requests went out without `X-Auth-Tenant-ID` headers | ✅ Earlier session — `ensureHydrated()` interceptor |
| WebAuthn methods return 501 | 🔴 Still stubbed |
| MCP server tool handlers return `{"status":"ok"}` placeholders | 🔴 Still stubbed |
| Vector search returns empty list, hybrid degrades to lexical-only | 🔴 Still stubbed |

---

# 14. Tech Debt

## Pre-existing
- **Per-tenant KEK** — single KEK in dev; per-tenant derivation pending. Tracked in `docs/tech-debt/per-tenant-kek.md`.
- **DSS sidecar PAdES signing** — `signer/factory.go` defaults to mock; real signer is a shell. Wave 9.2b.
- **Vector search query** — half-built (ingest writes Qdrant, search doesn't read it). `services/search/internal/service/service.go:87`.
- **WebAuthn service methods** — 501 stubs in `services/auth/internal/service/webauthn.go`.
- **MCP server** — stub tool implementations.
- **`SEDOC_SMTP_USER` vs `SEDOC_SMTP_USERNAME` inconsistency** — auth reads `USER`, notification reads `USERNAME`. Both should converge. Flagged in `.env` template.
- **i18n** — no library, English hardcoded everywhere.

## Introduced/uncovered this session
- **Connector repo / schema mismatch** — Fixed via migration 000045 + repo SQL rewrite. Tracked.
- **Multiple uncommitted features on `feat/upload-scan-toggle-progress`** — branch carries eSign per-tenant + Twilio/SMTP + Google connector + iPaaS triggers. Should be split into separate PRs before merge.

## Other observed risks
- **No central rate-limiting story** for the new iPaaS trigger endpoints. Anyone with a valid API key can poll at any rate today. The shared `pkg/middleware/ratelimit.go` exists but isn't applied to these routes.
- **Per-service test gaps** on `connector`, `audit`, `billing`, `policy` — handler-only coverage. Service layer logic untested.
- **In-flight uncommitted changes** — 25+ modified/new files on the branch; risky to accumulate further before commits.

---

# 15. Security Checklist

| Check | Status | Evidence |
|---|---|---|
| Auth on `/api/v1/admin/*` routes | ✅ Enforced | `services/auth/internal/handler/router.go:121–178` — every admin sub-route wraps in `AuthMiddleware` + `RequireRole("admin", "owner")` + CSRF |
| Input validation | ✅ Enforced | Handlers parse JSON into typed structs; field validators in `pkg/validation/` and per-handler. Signature handler hardened this session against empty inputs. |
| File-type validation on uploads | ✅ Enforced | `services/storage/internal/scanner/mimecheck.go` — ExecutableMIMEBlocklist (30+ types) + ExecutableExtensions, plus ClamAV scan post-upload |
| Size limit on uploads | ✅ Enforced | `cfg.MaxUploadSize` per-service; storage's single-PUT ceiling (multipart pending) |
| Rate limiting | 🟡 Partial | `pkg/middleware/ratelimit.go` (token-bucket, Redis-backed) applied to `/auth/*` routes; **NOT applied to iPaaS trigger endpoints** (gap) |
| CORS config | 🟡 Visibility gap | No `cors` package import found in services; relies on Kong gateway config (`deploy/gateway/kong.yaml` — not inspected this audit) |
| Secrets in env (not hardcoded) | ✅ Clean | No password/token literals in source. The previously-shipped fallback `dev-only-gateway-secret-rotate-in-prod` was removed across compose / scripts / Node services 2026-05-21; `SEDOC_GATEWAY_SECRET` must now be explicitly exported (Vite dev proxy, compose, run-all-services.sh, restart-dev.ps1, yjs-server.js, and k6 load tests all fail-fast if unset). |
| SQL injection protection | ✅ Enforced | All repos use parameterized `$1, $2` queries via pgx. Zero string-concat in WHERE clauses spotted. |
| XSS protection | ✅ Enforced | React's auto-escaping; no `dangerouslySetInnerHTML` usage outside of intentional PDF/preview content |
| CSRF protection | ✅ Enforced | `pkg/middleware/csrf.go` double-submit cookie pattern on every mutating session-cookie route. API-key callers exempt by design. |
| Tenant isolation (RLS) | ✅ Enforced | All ~50 tenant tables `ENABLE ROW LEVEL SECURITY` + `FORCE`. `pkg/database.WithTenantTx` opens tx + `SET LOCAL app.current_tenant`. App role is `NOBYPASSRLS` in prod. |
| Secret logging | ✅ Clean | Grep for `log.*token`/`log.*password` → 0 hits. Auth middleware doesn't log bearer values. |
| Encryption at rest | ✅ Enforced | Per-blob DEK wrapped by per-tenant KEK (envelope encryption); pluggable KMS (`local`/`vault`/`aws`). Single dev KEK is the noted tech debt. |
| Encryption in transit | 🟡 Mixed | TLS at gateway in prod (Kong); internal gRPC currently uses `insecure.NewCredentials()` in dev (`services/*/cmd/server/main.go`) — typical for cluster-local mTLS-by-sidecar setups |
| Audit logging on sensitive ops | ✅ Enforced | `audit_events` time-partitioned, append-only with Merkle integrity (ADR 0024). Most domain writes emit `dms.audit.*` events. |

---

# 16. Next Steps (Prioritized)

1. **Commit and split the in-flight branch.** ~25 modified/new files mix 4 distinct features (eSign per-tenant, Twilio/SMTP modals, Google connector, iPaaS surface). Split into 4 PRs before further work. **S**
2. **Build Google Drive import action** to make the OAuth slice useful. `POST /api/v1/connectors/google/drive/import?folder_id=...` calling `DocumentClient.MaterialiseFile`. **M**
3. **Wire WebAuthn service methods** with `go-webauthn` library. Routes + env propagation are done; ~150 LOC per method. **M**
4. **Per-tenant rate limiting** on iPaaS trigger endpoints. Apply existing `pkg/middleware/ratelimit.go` with a `integrations:` group per tenant. **S**
5. **Vector search query side** — wire Qdrant client into `services/search/internal/service/hybrid.go`. Embedding pipeline already runs on ingest; just need the query lookup. **M**
6. **Add Salesforce + M365 connector wrappers** — provider classes already exist; ~2h each to add the Save/Start/Callback service methods + frontend modal. **M**
7. **Per-tenant KEK derivation** for blob encryption. Tracked at `docs/tech-debt/per-tenant-kek.md`; storage's TODO points at line 137. Crypto-shredding by KEK delete becomes truly per-tenant. **L**
8. **Add tests to zero-coverage services** — collaboration, intelligence (pytest), preview, signature-signer. **L**
9. **Fix `SEDOC_SMTP_USER` / `_USERNAME` inconsistency** — converge on one name across `pkg/config/config.go` and `services/auth/cmd/server/main.go`. **S**
10. **Drive/Gmail import + sync health dashboard** — once Drive works, add a "Connectors" tab admin page showing per-vendor sync status + error rate + last poll. **M**

---

# 17. Open Questions

1. **Prod deploy of in-flight features.** Should the four new things on `feat/upload-scan-toggle-progress` ship as a single v0.6, or split into four releases? My recommendation: split.
2. **Google connector scope.** Drive vs Gmail vs both — and is the goal *import existing files into VaultDMS as documents*, or *two-way sync*? The current code only reads scopes; no write back to Drive is planned.
3. **Connector roadmap priority.** Of the 7 remaining §12.4 connectors, which one matters first? Customer signal vs internal pet-feature isn't clear from the codebase.
4. **WebAuthn finishing.** Is this a "ship in v0.6" feature or a "future quarter" item? Env vars exist but the service is 501.
5. **MCP server scope.** Is the stub a placeholder for real LLM-agent integration (Claude Desktop, Cursor)? If so, what's the use case?
6. **Mock signer in production.** `signer/factory.go` defaults to mock when `SEDOC_SIGNER` is unset. Should startup fail-loudly in prod (require explicit `SEDOC_SIGNER=dss`) to prevent accidentally shipping the mock?
7. **CORS configuration.** Is Kong supplying CORS headers, or do we need explicit per-service config? I couldn't inspect `deploy/gateway/kong.yaml` in this audit.
8. **Per-tenant KEK migration plan.** Once the per-tenant derivation lands, what's the strategy for re-keying existing blobs sealed with the single dev KEK?
9. **iPaaS rate limiting policy.** What rate is acceptable? Zapier polls every 1-15 min by default. Make can poll faster. Need a baseline (e.g. 60/min per key).
10. **i18n decision.** Is single-locale English acceptable for v1, or does this need react-i18next before GA?

---

*End of audit.*
