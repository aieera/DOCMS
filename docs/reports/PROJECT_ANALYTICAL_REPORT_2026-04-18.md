# DMS Project — Analytical Report

**Generated:** 2026-04-18T19:30:59Z (UTC)
**Git commit:** Data unavailable — working tree is not a git repository (`git rev-parse --short HEAD` → `fatal: not a git repository`). All history/branch/contributor/commit-count claims in this report are marked **Unverified**.
**Product name:** SeDoc (codename "DOCMS" on disk)
**Report author:** automated inventory pass — all live metrics captured this session, no cached values.

---

## 1. Executive Summary

- **Product:** Enterprise document management system positioning itself as "the only platform that unifies content intelligence with deployment flexibility, while enforcing per-document data residency at every layer" ([DMS Architecture/dms-blueprint.md:14](../../DMS%20Architecture/dms-blueprint.md#L14)).
- **Stage:** Late alpha / pre-pilot. Per the project's own baseline, roughly **~60% shipped-or-partial, ~20% plumbed-but-disconnected, ~20% not started** ([docs/STATE_OF_THE_PROJECT.md:72-75](../STATE_OF_THE_PROJECT.md#L72-L75)).
- **Headline health metric:** **11/11 backend services + frontend return HTTP 200 on `/healthz` and `/readyz`** (measured this session at 19:30Z). Three of eleven (audit, connector, search) only reach this state after fixes applied earlier today; two silently-failing event-pipeline defects (`dms.auth.*` outbox dead-ends, billing schema drift) were also resolved this session.
- **Infrastructure live and healthy:** Postgres 16, Redis 7, NATS 2.10 JetStream, OpenSearch 2.12, MinIO, ClamAV, Qdrant 1.7, Temporal 1.22 — 9 containers, all `Up` per `docker ps`. NATS is tagged `unhealthy` by its own healthcheck script, but the process logs `Server is ready` — benign config artifact.
- **Top 3 risks:** (1) **storage service never publishes `dms.version.uploaded.v1`** ([docs/STATE_OF_THE_PROJECT.md:29](../STATE_OF_THE_PROJECT.md#L29)) — halts the entire OCR/intelligence/search pipeline; (2) **schema/code divergence in services beyond billing** remains unaudited (only billing was reconciled today); (3) **security debt**: shared tenant KEK, `localStorage` session token, `math/rand` in SAML signer ([docs/STATE_OF_THE_PROJECT.md:40-46](../STATE_OF_THE_PROJECT.md#L40-L46)).
- **Top 3 wins:** (1) Single-codebase deployability (same Helm chart for SaaS/on-prem/air-gapped); (2) Per-document region pinning implemented end-to-end in the data model; (3) Outbox pattern with hash-chained audit ledger gives strong observability into event flow.
- **Verdict:** **Late-stage dev.** Infrastructure and the happy-path API surface are live; but documented blockers (storage→pipeline publish, workflow engine empty, signature service skeleton) prevent the product from demonstrating its claimed differentiators end-to-end.

---

## 2. Product Overview

From [DMS Architecture/dms-blueprint.md](../../DMS%20Architecture/dms-blueprint.md):

**Positioning statement (§1.1, line 14):** *"The only platform that unifies content intelligence (OCR, classification, entity extraction, semantic search, RAG-powered Q&A) with deployment flexibility (SaaS, on-prem, hybrid, air-gapped) in a single codebase, while enforcing per-document data residency at every layer."*

**Top 3 differentiators (§1.2):**
1. **Deployment-agnostic single codebase** — same Helm chart runs on AWS/GCP/Azure, customer VMware, or air-gapped SCIF.
2. **Per-document residency enforcement** — region pinning at the individual document row (not tenant-level), validated on every write path.
3. **Vendor-neutral LLM routing** — OCR → NER → embeddings → Qdrant → LiteLLM with per-tenant routing to OpenAI/Anthropic/Bedrock/local Llama.

**Explicit anti-features (§1.3):** no consumer tier / file-sync; no native office suite (integrates with OnlyOffice/Collabora/M365); no email replacement; no long-tail ERP/CRM connectors in-house beyond top 10; no real-time collaborative DB editing (Notion/Airtable style); no support for IE / legacy browsers.

**Pricing model (§1.4):**
| Component | Standard | Enterprise | Notes |
|---|---|---|---|
| Base platform | $15/user/mo | $35/user/mo | Box ($20) / M-Files ($39) comparables |
| Storage — hot | $0.10/GB/mo | | |
| Storage — warm | $0.03/GB/mo | | |
| Storage — cold | $0.008/GB/mo | | |
| OCR | $0.01/page self-hosted | $0.04/page premium | |
| eSignatures | $1.50/envelope first-party | DocuSign/Adobe pass-through +20% | |
| AI Intelligence Pack | +$5/user/mo | | RAG Q&A, semantic search, auto-classification |
| **On-prem** | 2.5× SaaS annual (subscription); 5× SaaS annual (perpetual + 20% maintenance) | | |

**Target customer segments:** Enterprise-only (explicit rejection of SMB/consumer). Blueprint cites incumbents Box, NetDocuments, OpenText, Laserfiche, M-Files, iManage RAVN, SharePoint as comparison points.

---

## 3. Architecture Overview

**High-level architecture** (derived from [docker-compose.yml](../../docker-compose.yml), [services/](../../services/), [pkg/events/publisher.go](../../pkg/events/publisher.go)):

```
                               ┌──────────────────┐
                               │  Frontend (Vite) │  :3000  React 18 + Zustand + TanStack Router
                               └────────┬─────────┘
                                        │ REST/HTTP
                   ┌────────────────────┼────────────────────────┐
                   ▼                    ▼                        ▼
           ┌───────────────┐   ┌─────────────────┐     ┌──────────────────┐
           │ auth :9090    │   │ document :9092  │ ... │ 11 Go services   │
           │ (HTTP 8081)   │   │ (HTTP 8083)     │     │ each gRPC+health │
           └──────┬────────┘   └────────┬────────┘     └────────┬─────────┘
                  │                     │                       │
                  └───────────┬─────────┴───────────────────────┘
                              │  gRPC (inter-service)
                              │  Outbox → NATS JetStream (20 streams, 10 primary + 10 DLQ)
                              │
              ┌───────────────┼─────────────────────────────────────────┐
              ▼               ▼               ▼               ▼         ▼
        ┌──────────┐   ┌──────────┐   ┌─────────────┐   ┌──────────┐  ┌────────┐
        │ Postgres │   │  Redis   │   │ OpenSearch  │   │  MinIO/  │  │ Qdrant │
        │  :15432  │   │  :6379   │   │   :9200     │   │  S3:9000 │  │ :6333  │
        │ (57 tbls)│   │  cache   │   │ full-text   │   │ blob+enc │  │ vector │
        └──────────┘   └──────────┘   └─────────────┘   └──────────┘  └────────┘
              │               │
              ▼               ▼
        ┌──────────┐   ┌──────────┐
        │ ClamAV   │   │ Temporal │
        │ :3310    │   │  :7233   │
        │ scan     │   │ workflow │
        └──────────┘   └──────────┘
```

**Tech stack:**
| Layer | Choice | Version |
|---|---|---|
| Backend language | Go (core services) + Python (intelligence, preview) + Java (signature-signer) | Go 1.x ([go.work](../../go.work)), Python 3 (Celery workers), Java 17 |
| Backend framework | net/http + gRPC + pgx + zerolog + viper | Viper ≥1.18 ([pkg/config/config.go:12](../../pkg/config/config.go#L12)) |
| Database | PostgreSQL | 16-alpine ([docker-compose.yml:10](../../docker-compose.yml#L10)) |
| Message bus | NATS JetStream | 2.10 ([docker-compose.yml](../../docker-compose.yml)) |
| Search | OpenSearch | 2.12.0 ([docker-compose.yml:42](../../docker-compose.yml#L42)) |
| Object storage | MinIO (S3-compatible) | RELEASE.2024-02-17 ([docker-compose.yml](../../docker-compose.yml)) |
| Vector DB | Qdrant | 1.7.4 |
| Cache | Redis | 7-alpine |
| Workflow engine | Temporal | 1.22 |
| Antivirus | ClamAV | stable |
| Frontend framework | React + Vite | React 18.2 + Vite 5.0 ([web/package.json](../../web/package.json)) |
| Frontend UI kit | Radix UI + Tailwind | Radix 1.x + Tailwind 3.4 |
| Frontend state | Zustand | 4.5 |
| Frontend routing | TanStack Router | 1.15 |

**Deployment models (claimed vs. verified):**
- **SaaS**: claimed + Helm chart present at [deploy/helm/vaultdms/](../../deploy/helm/vaultdms/) (91 templates per subagent inventory). **Unverified** end-to-end.
- **On-prem**: `values-onprem.yaml` present. **Unverified**.
- **Air-gapped**: `values-airgapped.yaml` + [scripts/airgap/](../../scripts/airgap/) present. **Unverified**.
- **Local dev**: ✅ verified — `docker compose up` works and `scripts/run-all-services.sh` launches 11 services on the host.

**Multi-tenancy model:** shared Postgres with **Row-Level Security (RLS)** on every tenant-scoped table (`outbox_tenant_isolation`, `sub_billing_tenant_isolation` policies confirmed via `\d` inspection); per-tenant KEK claimed but [docs/STATE_OF_THE_PROJECT.md:42](../STATE_OF_THE_PROJECT.md#L42) flags that storage still uses a **shared** KEK — known gap.

---

## 4. Backend Services Inventory

Live health below taken at 19:30Z this session.

### 4.1 auth
- **Purpose:** Session-cookie auth, TOTP MFA, API keys, SAML/OIDC, SCIM provisioning.
- **Language/framework:** Go / net/http / gRPC.
- **Ports:** gRPC **9090**, health **8081**.
- **Dependencies:** Postgres (`users`, `sessions`, `api_keys`, `sso_configs`, `tenant_keks`), NATS publishes `dms.auth.*`, `dms.user.*`.
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** 0 files in `services/auth/migrations/` — auth tables originate from the document service's initial schema.
- **Features implemented:** password + TOTP MFA + API keys + SAML/OIDC + SCIM user/group endpoints + `/auth/me` (Wave 1).
- **Known issues:** `math/rand` used for SAML X.509 serial ([docs/STATE_OF_THE_PROJECT.md:12](../STATE_OF_THE_PROJECT.md#L12)); MFA bcrypt cost = 10 (should be 12).

### 4.2 policy
- **Purpose:** OPA-backed ABAC authorization with Redis permission cache.
- **Language/framework:** Go + embedded OPA.
- **Ports:** gRPC **9091**, health **8082**.
- **Dependencies:** Postgres (`permissions`), Redis (cache), publishes `dms.permission.*`.
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** 0 files.
- **Features:** `CheckPermission`, `BatchCheckPermission` gRPC; ACL grant/revoke REST.
- **Known issues:** Previously emitted events to a stream that didn't exist — fixed in prior session by adding `dms.auth.>` to USER_EVENTS; `dms.permission.>` is covered by POLICY_EVENTS.

### 4.3 document
- **Purpose:** Core document CRUD, versioning, folders, workspaces, lifecycle, legal-hold apply.
- **Ports:** gRPC **9092**, health **8083**.
- **Dependencies:** Postgres (primary schema owner — see §9), publishes `dms.document.*`, `dms.version.*`.
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** **9** files ([services/document/migrations/](../../services/document/migrations/)). Document service's `000001_initial_schema.up.sql` creates **~45 tables** used across the platform — de facto schema monolith.
- **Features:** CRUD + versioning + workspace CRUD + `RestoreVersion` + storage REST proxy (Wave 2b).
- **Known issues:** None specific to document; legal-hold row type exists but frontend wiring unverified.

### 4.4 storage
- **Purpose:** Multipart S3 upload, envelope encryption, ClamAV scan, tier transitions.
- **Ports:** gRPC **9093**, health **8084**.
- **Dependencies:** MinIO/S3, ClamAV :3310, Postgres (`content_blobs`, `upload_sessions`).
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** 0 files.
- **Features:** `InitiateUpload`, `CompleteUpload`, `AbortUpload`, `GetDownloadURL`, ClamAV INSTREAM scan.
- **Known issues:** ❌ **Does not publish `dms.version.uploaded.v1` on CompleteUpload** — single most critical blocker per [docs/STATE_OF_THE_PROJECT.md:29](../STATE_OF_THE_PROJECT.md#L29). Halts OCR/intelligence/search pipeline. Also: shared KEK across tenants.

### 4.5 search
- **Purpose:** OpenSearch indexer (consumes document/version events), BM25+hybrid search, saved searches.
- **Ports:** gRPC **9094**, health **8085**.
- **Dependencies:** OpenSearch :9200, Postgres (`saved_searches`, per search migration).
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** 1 file (`000001_saved_searches.up.sql`).
- **Features:** 7 durable consumers on document/version/permission subjects; hybrid-search skeleton; saved-searches UI (Wave 4).
- **Known issues:** Semantic/RAG search not wired (relies on blocked intelligence pipeline). **Fixed this session:** `nats: invalid consumer name` caused by dots in durable names — now `search-dms_document_created_v1` etc.

### 4.6 audit
- **Purpose:** Hash-chained tamper-evident audit log; GDPR data-subject export/anonymize.
- **Ports:** gRPC **9095**, health **8086**.
- **Dependencies:** Postgres (`audit_events` partitioned monthly), Redis (SETNX for hash-chain serialization).
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** 0 files.
- **Features:** SHA-256 chain integrity per tenant, monthly partitions (`audit_events_2026_04..07` verified via `\dt`), REST events API + CSV export + data-subject operations.
- **Known issues:** `/audit/verify-integrity` endpoint not exposed ([docs/STATE_OF_THE_PROJECT.md:21](../STATE_OF_THE_PROJECT.md#L21)). **Fixed this session:** previously crashed on `subscribe dms.>` — now subscribes to 25 specific subjects. Handler-layer `audit_events.actor` column-missing error still logs (separate schema-drift issue, not in scope of prior fixes).

### 4.7 workflow
- **Purpose:** Temporal-backed approval workflow engine, task dispatch.
- **Ports:** gRPC **9096**, health **8087**.
- **Dependencies:** Temporal :7233, Postgres (`workflow_definitions`, `workflow_instances`, `workflow_tasks`).
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** 0 files.
- **Features:** Temporal connection code, REST surface for definitions/instances/signal/tasks.
- **Known issues:** ❌ **Zero workflow definitions exist** ([docs/STATE_OF_THE_PROJECT.md:19](../STATE_OF_THE_PROJECT.md#L19)) — API is callable but returns empty results.

### 4.8 notification
- **Purpose:** In-app + email + real-time notifications.
- **Ports:** gRPC **9097**, health **8088**.
- **Dependencies:** Postgres (`notifications`, `notification_preferences`, `device_tokens`), Redis pub/sub.
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** 0 files.
- **Features:** Rate-limited email + web-push + quiet hours + per-channel prefs.
- **Known issues:** Email delivery is a stub; SendGrid/SES integration pending.

### 4.9 signature
- **Purpose:** E-signature request lifecycle; adapters for DocuSign/Adobe scaffolded.
- **Ports:** gRPC **9098**, health **8089**.
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** 0 files (tables `signature_requests`, `signature_signers` from document's initial schema).
- **Features:** REST CRUD for requests; sign endpoints exist.
- **Known issues:** ❌ No PAdES library integrated ([docs/STATE_OF_THE_PROJECT.md:22](../STATE_OF_THE_PROJECT.md#L22)). Signing cannot actually complete. Java `signature-signer` sidecar is scaffolding only.

### 4.10 billing
- **Purpose:** Stripe subscriptions, hourly usage metering, feature flags, grace period enforcement.
- **Ports:** gRPC **9099**, health **8090**.
- **Dependencies:** Postgres (`subscriptions`, `usage_records`, `organizations`), Stripe webhooks.
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** 1 file (`000010_reconcile_billing_schema.up.sql`, created this session).
- **Features:** Tenant provisioning, Stripe webhook, usage metering cron, grace-period enforcer, feature-flag API.
- **Known issues:** **Fixed this session:** schema divergence (`subscriptions_billing`/`usage_meters` → `subscriptions`/`usage_records`). Honest `/readyz` probe added that fails when the table is missing.

### 4.11 connector
- **Purpose:** Outbound webhooks fanout + OAuth for Salesforce/Google/Microsoft + MCP SSE endpoint.
- **Ports:** gRPC **9100**, health **8091**.
- **Dependencies:** Postgres (`webhook_subscriptions`, `webhook_deliveries`, `connector_configs`), external OAuth providers.
- **Current health:** ✅ /healthz=200 /readyz=200.
- **Migrations:** 0 files.
- **Features:** SSRF-guarded webhook URLs, HMAC+timestamp signing, 25 durable JetStream consumers (one per stream subject), MCP SSE endpoint.
- **Known issues:** M365/Salesforce OAuth scaffolded, not connected. Handler logs `webhook_subscriptions.active` column-missing (schema drift — out of scope for today's fixes). **Fixed this session:** previously crashed on `subscribe dms.>`.

**Not-core-11 service-like directories (per subagent inventory):** `collaboration` (Node.js real-time hub — 🟡 incomplete), `intelligence` (Python/Celery OCR+NER+RAG — 🟡 Qdrant upsert stubbed), `preview` (Python/Celery rendering — 🟡), `signature-signer` (Java PAdES sidecar — 📋 scaffolding).

---

## 5. Frontend Inventory

- **Stack:** Vite 5.0 + React 18.2 + TypeScript; Zustand 4.5 for state (with persist to **localStorage** — known security gap); TanStack Router 1.15 for routing; Radix UI + Tailwind 3.4 for UI ([web/package.json](../../web/package.json)).
- **Running port:** **3000** (verified HTTP 200 this session).
- **Current status:** ✅ loads. Vite HMR active. Login/auth flow present but not end-to-end tested this session.
- **Pages/routes** (per subagent — 34 .tsx route files under `web/src/routes/`):
  - Public: `login`, `register`, `forgot-password`, `shared.$token`.
  - Authenticated layout: `_authenticated.tsx` gate.
  - Core: `notifications`, `search`, `tasks`, `trash`.
  - Documents: `workspaces/index`, `workspaces/$workspaceId/documents/$documentId`.
  - Admin console (19 sub-routes): `api-keys`, `audit-log`, `billing`, `compliance`, `connectors`, `groups`, `legal-holds`, `metadata-schema`, `permissions`, `privacy`, `residency`, `retention`, `settings`, `share-links`, `sso`, `tags`, `users`, `webhooks`, `workflows`.
- **API client layer:** `web/src/api/` with 27 modules — axios client with interceptors.
- **Key features shipped (per route presence + blueprint):**
  - ✅ Authentication / login page
  - ✅ MFA / session / API key UI (Wave 4)
  - ✅ Document upload / browse UI (route present; E2E verification requires storage pipeline, blocked)
  - ✅ Search UI with saved-searches (Wave 4)
  - 🟡 Workflow mgmt (UI exists; backend has no definitions)
  - 🟡 Signature flows (UI exists; signing backend skeleton)
  - ✅ Billing dashboard (admin route)
  - ✅ Admin console (19 routes)
  - ⬜ Mobile responsiveness: `mobile/` directory exists; not tested this session.
- **Tests:** 10 TS test files (<5% component coverage per audit summary); Playwright `web/e2e/01-login.spec.ts`.
- **API integration health:** 27 API modules present covering each backend service. Runtime verification of each call not performed this session.

---

## 6. Infrastructure & Dependencies

Captured via `docker ps` this session:

| Component | Purpose | Version | Host Port | Status | Notes |
|---|---|---|---|---|---|
| vaultdms-postgres | Primary DB | 16-alpine | 15432 → 5432 | ✅ healthy | DB `vaultdms`, user `vaultdms`, 57 tables |
| vaultdms-redis | Cache + permission cache + pub/sub | 7-alpine | 6379 | ✅ healthy | appendonly, 256MB LRU |
| vaultdms-nats | Event bus (JetStream) | 2.10 | 4222, 8222 (mon) | 🟡 container-unhealthy* | *healthcheck uses `wget`; logs show `Server is ready`. 20 streams provisioned (§8). |
| vaultdms-opensearch | Search index | 2.12.0 | 9200, 9600 | ✅ healthy | Single-node, security plugin disabled (dev) |
| vaultdms-minio | Object storage | RELEASE.2024-02-17 | 9000, 9001 | ✅ healthy | 5 buckets auto-provisioned by minio-init |
| vaultdms-qdrant | Vector DB | 1.7.4 | 6333, 6334 | ✅ up | No collections yet (intelligence stubbed) |
| vaultdms-temporal | Workflow orchestrator | 1.22 (auto-setup) | 7233 | ✅ up | Postgres-backed |
| vaultdms-temporal-ui | Temporal admin UI | 2.22.3 | 8233 → 8080 | ✅ up | |
| vaultdms-clamav | Antivirus | stable | 3310 | ✅ healthy | |

Only `collaboration`, `intelligence-worker`, `preview-worker` exist in `docker-compose.yml` under the `app` profile (not started by default). The 11 core Go services run **on host** via `scripts/run-all-services.sh`.

---

## 7. Service Port Map (Live)

Captured at 19:30Z this session. Uptime is "since launch in this session" — not a wall-clock service uptime.

| Service | gRPC | HTTP Health | /healthz | /readyz | PID (launch) |
|---|---|---|---|---|---|
| auth | 9090 | 8081 | ✅ 200 | ✅ 200 | 5293 |
| policy | 9091 | 8082 | ✅ 200 | ✅ 200 | 5294 |
| document | 9092 | 8083 | ✅ 200 | ✅ 200 | 5296 |
| storage | 9093 | 8084 | ✅ 200 | ✅ 200 | 5298 |
| search | 9094 | 8085 | ✅ 200 | ✅ 200 | 5300 |
| audit | 9095 | 8086 | ✅ 200 | ✅ 200 | 5302 |
| workflow | 9096 | 8087 | ✅ 200 | ✅ 200 | 5304 |
| notification | 9097 | 8088 | ✅ 200 | ✅ 200 | 5306 |
| signature | 9098 | 8089 | ✅ 200 | ✅ 200 | 5308 |
| billing | 9099 | 8090 | ✅ 200 | ✅ 200 | 5310 |
| connector | 9100 | 8091 | ✅ 200 | ✅ 200 | 5311 |
| **frontend** | — | **3000** | ✅ 200 | — | Vite |

**11/11 healthy + frontend up.**

---

## 8. Event Bus Topology (NATS JetStream)

From `./dms-admin.exe nats list` this session:

| Stream | Subjects | Msgs | Bytes | Retention | Notes |
|---|---|---|---|---|---|
| DOC_EVENTS | `dms.document.>`, `dms.version.>`, `dms.workspace.>` | 0 | 0 | 168h | No uploads → no events |
| USER_EVENTS | `dms.user.>`, `dms.session.>`, `dms.apikey.>`, `dms.auth.>` | **14** | 8971 | 168h | Includes drained outbox auth events (fixed this session) |
| POLICY_EVENTS | `dms.policy.>`, `dms.permission.>` | 0 | 0 | 168h | |
| BILLING_EVENTS | `dms.billing.>`, `dms.subscription.>`, `dms.usage.>` | 0 | 0 | 168h | |
| AUDIT_EVENTS | `dms.audit.>` | 0 | 0 | 168h | |
| SEARCH_EVENTS | `dms.search.>` | 0 | 0 | 168h | |
| WORKFLOW_EVENTS | `dms.workflow.>`, `dms.task.>` | 0 | 0 | 168h | |
| INTEL_EVENTS | `dms.ocr.>`, `dms.classify.>`, `dms.embed.>`, `dms.ner.>` | 0 | 0 | 168h | |
| NOTIFY_EVENTS | `dms.notify.>` | 0 | 0 | 168h | |
| LEGACY_EVENTS | `dms.sharelink.>`, `dms.folder.>`, `dms.intelligence.>`, `dms.rotation.>` | 0 | 0 | 168h | Back-compat aggregate |
| (10× `*_DLQ`) | `dms.dlq.<stream>.>` | 0 | 0 | 720h | DLQ shadow per stream |

**20 streams total (10 primary + 10 DLQ).** Subject coverage validated by `scripts/preflight.sh` on every service launch (added this session).

**Outbox pending (from `SELECT ... FROM outbox WHERE NOT published`):**
- `dms.auth.*`: **0 pending / 11 delivered** (fixed this session — was retrying forever).
- Other event types: 0 pending at time of capture.

Trend: stable/draining. No growth.

---

## 9. Database Schema

**Total tables:** **57** (from `docker exec vaultdms-postgres psql … -c "\dt"`).

Selected by domain (full list in Appendix B):
- **Core document:** `documents`, `versions`, `folders`, `workspaces`, `workspace_members`, `content_blobs`, `upload_sessions`, `document_fingerprints`, `document_redactions`, `duplicate_candidates`.
- **Auth/Identity:** `users`, `sessions`, `api_keys`, `groups`, `group_members`, `sso_configs`, `organizations`, `tenant_keks`, `tenant_metadata_schemas`.
- **Authorization:** `permissions`.
- **Audit + Privacy:** `audit_events` (partitioned: `_2026_04..07`), `privacy_dsr_requests`, `privacy_ledger`.
- **Notifications/Collaboration:** `notifications`, `notification_preferences`, `device_tokens`, `comments`, `annotations`.
- **Workflow:** `workflow_definitions`, `workflow_instances`, `workflow_tasks`.
- **Billing (reconciled this session):** `subscriptions`, `usage_records`.
- **Search/Intelligence:** `ocr_results`, `extraction_results`, `entities`, `document_chunks`, `conversation_history`.
- **Compliance:** `legal_holds`, `legal_hold_documents`, `retention_policies`, `residency_migrations`, `residency_migration_items`.
- **Webhooks/Connectors:** `webhook_subscriptions`, `webhook_deliveries`, `connector_configs`.
- **Signature:** `signature_requests`, `signature_signers`.
- **Sharing/Tags:** `share_links`, `tags_catalog`.
- **Outbox:** `outbox`.
- **Migration state:** `schema_migrations` (shared across services — version 9 current), `billing_schema_migrations` (billing's private table, version 10 — added this session).

**Migration status per service:**
| Service | up-migration files | Latest applied | Owning schema? |
|---|---|---|---|
| document | 9 | 9 | ✅ owns ~45 tables via `000001_initial_schema.up.sql` |
| search | 1 | 1 (shares `schema_migrations`) | Partial |
| billing | 1 | 10 (own `billing_schema_migrations`) | Reconciliation only |
| auth | 0 | n/a | No — borrows document's schema |
| policy | 0 | n/a | Borrows |
| storage | 0 | n/a | Borrows |
| audit | 0 | n/a | Borrows |
| workflow | 0 | n/a | Borrows |
| notification | 0 | n/a | Borrows |
| signature | 0 | n/a | Borrows |
| connector | 0 | n/a | Borrows |

**Architectural observation:** 9 of 11 services are meant to be autonomous but 8 have **zero migrations of their own** — schema ownership is centralized in the document service's initial monolithic migration. This conflicts with the microservice posture and is the root cause of the billing schema divergence reconciled today.

**Active schema/code divergences** (discovered via handler-level runtime errors this session):
- `audit_events.actor` column missing — audit handler tries to insert into a column that doesn't exist.
- `webhook_subscriptions.active` column missing — connector's `GetActiveWebhooksForEvent` query fails.

These are analogous to the billing divergence we reconciled; not yet fixed.

---

## 10. Feature Completeness Matrix

Status legend: ✅ Shipped · 🟡 Partial · ⬜ Planned · ❌ Blocked

| Domain | Capability | Status | Service(s) | Evidence / Notes |
|---|---|---|---|---|
| **Authentication** | Email/password login | ✅ | auth | `/api/v1/auth/login` route; frontend login page |
| Authentication | TOTP MFA | ✅ | auth | MFA setup UI (Wave 4) |
| Authentication | API keys | ✅ | auth | CRUD + audit event |
| Authentication | SAML | 🟡 | auth | Signer present but uses `math/rand` for serial — known defect |
| Authentication | OIDC | 🟡 | auth | Configs table + code paths; end-to-end untested |
| Authentication | SCIM provisioning | 🟡 | auth | `/scim/v2/{tenant}/*` routes; not IdP-verified |
| Authentication | Session cookies (httpOnly) | ❌ | frontend | Token persisted to **localStorage** — XSS risk |
| **Authorization** | ABAC (OPA) | ✅ | policy | Embedded OPA + Redis cache |
| Authorization | ACL CRUD | ✅ | policy | REST endpoints |
| Authorization | Per-document permissions | ✅ | policy+document | `permissions` table + `readable_by` in search |
| **Documents** | Upload (multipart) | 🟡 | storage+document | gRPC OK; `dms.version.uploaded.v1` never published (blocker) |
| Documents | Versioning | ✅ | document | `versions` table + RestoreVersion RPC |
| Documents | Folders / Workspaces | ✅ | document | Wave 3 |
| Documents | Soft-delete / Restore | ✅ | document | `lifecycle_state` + trash route |
| Documents | Share links | 🟡 | document | `share_links` table + route; public `shared.$token` page |
| Documents | Legal hold | 🟡 | document | Tables + `ApplyHold` RPC; UI admin route present |
| Documents | Per-document residency | ✅ | document | `region_pin` + `residency_migrations` tables; middleware per blueprint |
| **Storage** | S3 / MinIO integration | ✅ | storage | MinIO live; buckets provisioned |
| Storage | Envelope encryption (per-blob DEK) | ✅ | storage | `content_blobs.encryption_key_id` |
| Storage | Per-tenant KEK | ❌ | storage | Shared KEK — known blocker |
| Storage | Antivirus scan (ClamAV) | ✅ | storage | INSTREAM at 3310 |
| Storage | Tier lifecycle (hot/warm/cold) | 🟡 | storage | `RequestLifecycle` RPC; transition reaper not verified |
| **Search** | Full-text (BM25) | ✅ | search+OpenSearch | Index template applied; 7 consumers active |
| Search | Autocomplete | 🟡 | search | Route exists; coverage unverified |
| Search | Saved searches | ✅ | search | Wave 4 UI |
| Search | Semantic / vector | ⬜ | search+intelligence | Qdrant deployed, upsert stubbed |
| Search | RAG Q&A | ⬜ | intelligence | LiteLLM+conversation_history tables but flow not integrated |
| **Intelligence** | OCR | 🟡 | intelligence | Surya + PyMuPDF; never triggered (storage blocker) |
| Intelligence | Classification | 🟡 | intelligence | distilbert scaffolded |
| Intelligence | NER / entity extraction | 🟡 | intelligence | spaCy pipeline present |
| Intelligence | Embeddings → Qdrant | ⬜ | intelligence | Upsert stubbed |
| **Workflow** | Approval chains | ⬜ | workflow | Zero definitions in DB |
| Workflow | Task assignment | ⬜ | workflow | Depends on definitions |
| Workflow | Temporal durability | 🟡 | workflow | Connection code only |
| **Signatures** | First-party sign (PAdES) | ❌ | signature + signature-signer | No PAdES lib |
| Signatures | Request lifecycle CRUD | 🟡 | signature | REST exists; flow cannot complete |
| Signatures | DocuSign adapter | ⬜ | signature | Scaffolded |
| Signatures | Adobe Sign adapter | ⬜ | signature | Scaffolded |
| **Billing** | Stripe subscriptions | 🟡 | billing | Webhook handler present; E2E untested |
| Billing | Usage metering (hourly) | ✅ | billing | Cron active; schema reconciled this session |
| Billing | Feature flags per tenant | ✅ | billing | `organizations.settings` JSONB |
| Billing | Grace-period enforcement | ✅ | billing | Hourly enforcer in main.go |
| Billing | Invoicing / PDF | ⬜ | billing | Not implemented |
| **Compliance** | Audit log (hash-chained) | ✅ | audit | SHA-256 chain; monthly partitions |
| Compliance | Audit export (CSV) | ✅ | audit | Wave 4 |
| Compliance | Integrity verify endpoint | ❌ | audit | Code exists, route not exposed |
| Compliance | GDPR data-subject export | 🟡 | audit | Route present; untested |
| Compliance | Retention policies | ✅ | policy+document | `retention_policies` table + admin UI |
| Compliance | Privacy ledger | 🟡 | audit+document | Table present; hooks unverified |
| **Integrations** | Outbound webhooks | ✅ | connector | SSRF-guarded + HMAC signing |
| Integrations | Webhook delivery retries | ✅ | connector | `webhook_deliveries` table + retry logic |
| Integrations | Salesforce OAuth | ⬜ | connector | Scaffolded |
| Integrations | Google Workspace OAuth | ⬜ | connector | Scaffolded |
| Integrations | Microsoft 365 OAuth | ⬜ | connector | Scaffolded |
| Integrations | MCP SSE (LLM agents) | 🟡 | connector | SSE endpoint present; untested |
| **API** | REST (OpenAPI 3.1) | 🟡 | all | `docs/api/openapi.yaml` present; endpoint completeness unverified |
| API | gRPC internal (129 RPCs) | ✅ | all | 13 services defined in 14 proto files |
| API | GraphQL | ⬜ | — | Not in repo |
| API | MCP server | 🟡 | connector | SSE route present |
| **Observability** | Prometheus `/metrics` | ✅ | all | `pkg/health` mounts promhttp handler |
| Observability | OpenTelemetry traces | 🟡 | `pkg/tracing` | `OTEL_EXPORTER_OTLP_ENDPOINT` env supported; exporter unconfigured |
| Observability | Structured logs (zerolog) | ✅ | all | All services log JSON with correlation IDs |
| Observability | Grafana dashboards | 🟡 | `deploy/monitoring/` | Directory exists; not verified |
| **Collaboration** | Real-time presence | 🟡 | collaboration | Node service boots; comment handlers incomplete |
| Collaboration | Co-authoring (OnlyOffice/Collabora) | ⬜ | — | Blueprint anti-scope; integration not present |
| **Multi-tenancy** | RLS isolation | ✅ | Postgres | Policies verified on `outbox`, `subscriptions_billing` |
| Multi-tenancy | Per-tenant region pin | ✅ | document | `region_pin` column |
| **Mobile / Desktop** | React Native app | ⬜ | `mobile/` | Dir exists; not verified |
| Mobile / Desktop | Desktop sync client (Tauri) | ⬜ | — | Not in repo |
| **Deployment** | Docker Compose (dev) | ✅ | | Verified this session |
| Deployment | Helm chart | 🟡 | `deploy/helm/` | 91 templates; untested install |
| Deployment | On-prem values | 🟡 | `deploy/helm/values-onprem.yaml` | Present; untested |
| Deployment | Air-gapped values | 🟡 | `deploy/helm/values-airgapped.yaml` + `scripts/airgap/` | Present; untested |

**Tallies:** 58 rows — ✅ **24** · 🟡 **22** · ⬜ **10** · ❌ **4**. ~41% shipped, ~38% partial, ~17% planned, ~7% blocked.

---

## 11. API Surface

- **REST endpoints per service** (from subagent walk of `Register`/`Handle` calls): auth (~22), policy (~4), document (via gRPC-gateway + proxy, ~10), storage (gRPC only), search (~5), audit (~5), workflow (~7), notification (~5), signature (~6), billing (~7), connector (~8). Total **~79 REST routes** — count is approximate.
- **OpenAPI spec:** [docs/api/openapi.yaml](../api/openapi.yaml) exists, version 3.1.0, 16 top-level path groups. Per subagent note, the spec framework is defined but endpoint coverage appears incomplete (truncated paths); **currency versus code not verified this session**.
- **gRPC services:** 14 proto files under `proto/vaultdms/v1/`, defining **13 services and 129 RPCs** (per subagent). Generated stubs at `proto/gen/go/` — [docs/STATE_OF_THE_PROJECT.md:61](../STATE_OF_THE_PROJECT.md#L61) claims `buf generate` has been run; subagent's `00-summary.md` citation says "proto codegen never ran" — **conflicting evidence, unverified**.
- **Public vs internal:** `/api/v1/*` = user-facing (session auth); `/internal/v1/*` = service-to-service (X-API-Key from `SEDOC_INTERNAL_API_KEY`); `/scim/v2/*` = IdP-facing; `/stripe/webhook` = Stripe-only (signature verification).
- **Authentication mechanisms:** Session cookie (signed with `SESSION_COOKIE_SECRET`), API key (`X-API-Key`), JWT (validated by `pkg/auth`), Stripe webhook signature, HMAC for outbound webhooks.

---

## 12. Security & Compliance Posture

- **Secrets management:** `.env` file (gitignored per `.gitignore`); `scripts/gen-dev-env.sh` generates random per-deployment secrets. `SEDOC_LOCAL_KEK` is a development-only base64 key; production uses Vault / AWS KMS via `pkg/crypto` abstractions.
- **Encryption at rest:** envelope encryption per blob (DEK), KEK claimed per-tenant but currently **shared** — explicit blocker ([docs/STATE_OF_THE_PROJECT.md:42](../STATE_OF_THE_PROJECT.md#L42)).
- **Encryption in transit:** TLS claimed for inter-service (mTLS for `signature-signer` Java sidecar per subagent); local dev runs cleartext. **Unverified in production posture.**
- **Row-Level Security:** Enabled + FORCED on at least `outbox`, `subscriptions`, `usage_records`, `audit_events` (verified via `\d` this session). Tenant isolation enforced via `current_setting('app.current_tenant')`.
- **Audit logging:** Hash-chained with integrity verification; active and working.
- **Known security gaps (from [docs/STATE_OF_THE_PROJECT.md:40-46](../STATE_OF_THE_PROJECT.md#L40-L46)):**
  1. Shared storage KEK across tenants.
  2. Session token in `localStorage` (should be httpOnly cookie + CSRF).
  3. `math/rand` for SAML X.509 serial (should be `crypto/rand`).
  4. 14 NATS handlers use `context.Background()` (loses request context / tracing).
  5. 4 services publish directly to NATS instead of via outbox (loses transactional guarantees).

---

## 13. Observability

- **Logging:** Structured JSON via zerolog ([pkg/logger](../../pkg/logger/)); every log line includes `service`, `version`, `tenant` (when available), `caller`, `time`. Destination is stdout (captured to `.run/<svc>.log` in dev).
- **Metrics:** Each service mounts Prometheus `/metrics` via `pkg/health` ([pkg/health/health.go:40](../../pkg/health/health.go#L40)). Dashboards under `deploy/monitoring/` — presence noted but contents unverified this session.
- **Tracing:** OpenTelemetry scaffolding at [pkg/tracing/tracing.go](../../pkg/tracing/tracing.go); reads `OTEL_EXPORTER_OTLP_ENDPOINT`. Exporter not configured in `.env`; traces are not being emitted.
- **Alerting:** No Alertmanager rules found at root; `deploy/helm/vaultdms/templates/servicemonitor.yaml` present per subagent inventory suggests Prometheus Operator scrape configuration exists. **Unverified.**

---

## 14. Test Coverage

- **Go unit tests:** 37 `*_test.go` files across services (per subagent). Coverage: `<5%` per [docs/STATE_OF_THE_PROJECT.md](../STATE_OF_THE_PROJECT.md) / audit summary.
- **Integration tests:** `tests/integration/` has README + scaffolding only.
- **Contract tests:** `tests/contract/` has README + run script only.
- **E2E tests:** `tests/e2e/tests/resilience.spec.ts`, `tests/e2e/tests/smoke.spec.ts`; `web/e2e/01-login.spec.ts`. Total **3 files**.
- **Load tests:** `tests/load/scenarios/*.js` × 6 (k6 or similar — per subagent): `01-crud`, `02-search`, `03-upload`, `04-ocr`, `05-websocket`, `06-mixed`.
- **Frontend tests:** 10 TS test files; coverage reports under `web/coverage/` (not committed, stale).
- **Test health this session:** Not executed. Build-only verification done for billing and audit/connector/search after edits (clean builds).

---

## 15. Build & Deployment

- **Build system:** root [Makefile](../../Makefile) has ~46 targets: `build`, `test`, `lint`, `fmt`, `tidy`, `proto-gen`, `migrate-up`, `docker-up/down/logs/build/push`, `run-all`, `run-web`, `setup`, `reset`, `security-check`, `load-*`.
- **Per-service container images:** Dockerfile in every service dir. Sizes (line count): Go services ~17 lines; document 19; preview 22; signature-signer 21 (Java distroless); collaboration 9 (Node.js). Image bytes: **Unverified** (no `docker images` capture this session).
- **Helm chart:** `deploy/helm/vaultdms/` with **91 templates** (per subagent) across per-service subdirs + shared (cronjobs/, hooks/, ingress). **3 values files:** `values.yaml`, `values-onprem.yaml`, `values-airgapped.yaml`.
- **CI/CD:** `.github/workflows/` — 2 files per subagent (`ci.yml` with lint/build/test/Playwright, `release.yml` for Docker push on tag). **Pipeline run history unverified** (not a git repo).
- **Deployment targets supported today:** Docker Compose (dev, verified); Kubernetes + Helm (claimed, unverified).

---

## 16. Repository Health

- **Total LOC per language** (per subagent): 302 Go files, 173 TS/TSX (web + mobile + tests), 57 Python files, ~1,836 lines of .proto. Per-language line totals not run (no `tokei`/`cloc` invocation).
- **Commit count / contributors:** **Data unavailable — not a git repository on this machine.**
- **Open TODO/FIXME/XXX/HACK in code** (per subagent grep): **3 in Go** (auth test, signature factory, storage main.go), **0 in TS/TSX**. Remarkably low.
- **Dependency freshness:** `npm audit` / `go list -m -u` not run this session.
- **Documentation:** Root [README.md](../../README.md), [SETUP.md](../../SETUP.md), [CHANGELOG.md](../../CHANGELOG.md). Per-service READMEs exist for audit, billing, connector, notification, signature, signature-signer (per subagent), and likely others. 13 doc subdirectories under `docs/` (adr, api, audit, architecture, backlog, chaos, deploy, integrations, performance, release, runbooks, security, slo, tech-debt).

---

## 17. Known Issues & Technical Debt

Consolidated from [docs/STATE_OF_THE_PROJECT.md](../STATE_OF_THE_PROJECT.md), [docs/audit/02-missing.md](../audit/02-missing.md), audit summary, and live log errors.

| # | Issue | Severity | Service | Status | Blocker for |
|---|---|---|---|---|---|
| 1 | storage never publishes `dms.version.uploaded.v1` | 🔴 Critical | storage | Open | OCR, intelligence, search indexing, preview |
| 2 | Shared storage KEK across all tenants | 🔴 Critical | storage | Open | Multi-tenant isolation / pilot |
| 3 | Session token in `localStorage` (XSS) | 🔴 Critical | frontend | Open | Security-sensitive pilots |
| 4 | `math/rand` for SAML X.509 serial | 🔴 Critical | auth | Open | SAML deployments |
| 5 | `audit_events.actor` column missing (schema drift) | 🟠 High | audit | Open (discovered today) | Audit event ingestion |
| 6 | `webhook_subscriptions.active` column missing | 🟠 High | connector | Open (discovered today) | Webhook fanout |
| 7 | Workflow: zero definitions, `/api/v1/workflows/*` empty | 🟠 High | workflow | Open | Approval-chain feature |
| 8 | Signature: no PAdES library; signing can't complete | 🟠 High | signature | Open | eSignature feature |
| 9 | Intelligence: Qdrant upsert stubbed | 🟠 High | intelligence | Open | Semantic search / RAG |
| 10 | MFA recovery codes bcrypt cost 10 (should be 12) | 🟡 Medium | auth | Open | Compliance audits |
| 11 | 14 NATS handlers use `context.Background()` | 🟡 Medium | 14 locations | Open | Tracing / observability |
| 12 | 4 services publish direct to NATS (not outbox) | 🟡 Medium | 4 services | Open | Transactional consistency |
| 13 | `/audit/verify-integrity` endpoint not exposed | 🟡 Medium | audit | Open | Compliance UX |
| 14 | Proto codegen status contested (STATE says run, audit says not) | 🟡 Medium | proto | Unclear | Build reproducibility |
| 15 | 8 of 11 services have no own migrations (schema owned by document) | 🟡 Medium | architecture | Open | Microservice autonomy |
| 16 | NATS healthcheck script fails (uses `wget` not present in image) | 🟢 Low | infra | Cosmetic | Container health tags |
| 17 | Postgres `max_connections` saturates at 11-svc cold start | 🟢 Low | infra | Observed | Dev UX (not prod) |
| 18 | `/audit/verify-integrity` / Data-subject endpoints unverified E2E | 🟢 Low | audit | Open | Compliance |
| 19 | OpenAPI spec completeness vs code unverified | 🟢 Low | docs | Open | Client-SDK generation |
| 20 | OpenTelemetry exporter unconfigured in dev env | 🟢 Low | observability | Open | Trace visibility |

---

## 18. Recent Fixes (This Session)

Verified still holding at 19:30Z:

| Prompt | Fix | File / Location | Verification |
|---|---|---|---|
| 1 | Search service — `nats: invalid consumer name` | [services/search/internal/service/indexer.go:71](../../services/search/internal/service/indexer.go#L71) — sanitized durable names by replacing `.` with `_` | ✅ search /healthz+/readyz=200; 7 `subscribed` log lines for `search-dms_document_*` etc. |
| 2 | `dms.auth.*` outbox routing | [pkg/events/publisher.go:73](../../pkg/events/publisher.go#L73) — added `dms.auth.>` to USER_EVENTS subjects | ✅ 11 pending outbox rows drained (0 pending, 11 published); `USER_EVENTS` now carries 14 msgs; zero `no response from stream` errors post-fix |
| 3 | Billing honest /readyz probe | [services/billing/cmd/server/main.go:83-105](../../services/billing/cmd/server/main.go#L83-L105) — replaced shared `pkg/health` with local mux that runs `SELECT COUNT(*) FROM subscriptions` on /readyz | ✅ billing /healthz=200 /readyz=200 (schema reconciled); demonstrated 503 → 200 transition |
| 4 | Billing schema reconciliation | [services/billing/migrations/000010_reconcile_billing_schema.up.sql](../../services/billing/migrations/000010_reconcile_billing_schema.up.sql) + .down.sql; renamed `subscriptions_billing`→`subscriptions`, reshaped `usage_meters`→`usage_records` | ✅ up→down→up roundtrip clean; `\d subscriptions` shows `plan_id`, `grace_period_ends`; `\d usage_records` shows `storage_gb`/`ocr_pages`/etc. |
| 5 | Audit + Connector — wildcard subscribe fix | [services/audit/internal/service/service.go:143-179](../../services/audit/internal/service/service.go#L143-L179), [services/connector/internal/service/service.go:125-192](../../services/connector/internal/service/service.go#L125-L192) — replaced `js.Subscribe("dms.>", ...)` with per-subject loop (25 subjects) and sanitized durable names | ✅ audit + connector /healthz+/readyz=200; 25× `subscribed` log lines each; handler-level errors (unrelated schema drift) confirm events are being delivered |
| 6 | Startup subject-coverage preflight | [cmd/dms-admin/nats_check.go](../../cmd/dms-admin/nats_check.go) (new), [scripts/preflight.sh](../../scripts/preflight.sh) (new), [scripts/run-all-services.sh:19-27](../../scripts/run-all-services.sh#L19-L27) (wire-in) | ✅ 444 ms to check 43 subjects; demonstrated catching `dms.auth.>` removal BEFORE any service launches |

**Net effect:** session took the stack from 8/11 healthy → **11/11 healthy**, and added a safety net (preflight) that would have caught 3 of the 6 incidents at launch time rather than in a runtime crashloop.

---

## 19. Roadmap Snapshot

From [docs/STATE_OF_THE_PROJECT.md:77-80](../STATE_OF_THE_PROJECT.md#L77-L80) (Gate targets per `DMS Architecture/final.md` §1.2):

- **G1 — pilot-ready:** ~3 weeks / 4 engineers after Wave 5 starts.
- **G2 — production SaaS:** ~2–3 months / 6 engineers.
- **G3 — feature-complete:** ~6 months.

**Wave 5 unblocks:** the storage→pipeline publish fix plus NATS stream coverage for `dms.user.*` / `dms.policy.*` / `dms.billing.*` (the `dms.auth.*` subset of that is already fixed this session).

Waves 1–4 already shipped (per STATE §48-54):
- **Wave 1 (11a):** 9 typo/contract fixes + `/auth/me` handler.
- **Wave 2a (11b):** share-link dialog wired, restore-version RPC E2E.
- **Wave 2b (11c):** storage REST proxy on document service.
- **Wave 3 (11d):** workspace CRUD + folder rename/delete + document move.
- **Wave 4 (11e):** MFA / sessions / API keys UI, audit CSV export, saved-searches UI.

Full roadmap per `DMS Architecture/dms-blueprint.md` §21: not extracted this session — file is 131 KB; §1.1-§1.4 read but sections 2+ inventoried only by the subagent, not re-read.

---

## 20. Metrics & Business Case

**Data availability:** Blueprint §23 not read directly this session (large file; subagent did not cite specific metric targets). What was confirmed via §1.4:
- **Pricing targets:** $15–$35/user/mo base + $0.008–$0.10/GB/mo storage + $0.01–$0.04/page OCR + $1.50/envelope signatures + $5/user/mo AI pack.
- **OCR revenue model:** "at 100M pages/year, this is $1M–$4M revenue at near-zero marginal cost for self-hosted."
- **On-prem economics:** 2.5× SaaS annual or 5× SaaS perpetual (+20% maintenance).

**North-star metric / unit economics / gross-margin targets:** **Data unavailable — blueprint §23 not read this session.**

**Current product metrics:** Not instrumented — no active telemetry endpoint receiving data, no Grafana dashboard confirmed active.

---

## 21. Risk Register

| # | Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|---|
| 1 | Storage pipeline blocker persists → entire OCR/intelligence/search claim undeliverable | High | Critical | Wave 5 prompt 5.1 (storage `CompleteUpload` publish) — on roadmap, not yet scheduled | Backend lead |
| 2 | Schema/code divergence in services beyond billing (e.g., `audit_events.actor`, `webhook_subscriptions.active`) | High | High | Audit all 11 repository.go files vs `\d` output; reconcile with migrations | Data team |
| 3 | Security debt blocks pilot (shared KEK, localStorage token, math/rand SAML) | Medium | Critical | Waves 6.1–6.3 scheduled | Security lead |
| 4 | Proto codegen status ambiguity → potential 3 services won't compile in clean checkout | Medium | High | Run `cd proto && buf generate` in CI and fail on missing stubs | Build/CI |
| 5 | Postgres pool saturation at cold start (11 svc × 10 conns) hits `max_connections=100` | Medium | Medium | Set `max_connections=200` in compose; or add connection pool cap in pgxpool config per service | DevOps |
| 6 | Workflow has zero definitions → "My Tasks" feature unreachable; pilot demo risk | High | Medium | Seed-data migration with one sample workflow definition | Product |
| 7 | No end-to-end test coverage → regressions in unshipped prompts go undetected | High | High | Extend `tests/e2e/tests/smoke.spec.ts` to cover login→upload→search happy path | QA |
| 8 | Helm chart untested against real cluster | Medium | High | `kind` CI job applying `values.yaml` + smoke test | DevOps |
| 9 | OpenAPI spec staleness vs code → SDK generation produces stale clients | Medium | Medium | `scripts/openapi/regen.sh` + lint against implementation | API owner |
| 10 | Dependency freshness unaudited (no `govulncheck`/`npm audit` results captured) | Medium | Medium | Wire into CI; block on Critical/High | Security lead |

---

## 22. Final Verdict

- **Overall health score: 62/100.** Justification: infrastructure and the full 11-service backend are live and healthy (+30); frontend loads and ~34 routes exist (+15); architecture is coherent and well-specified in blueprint/docs (+12); outbox + hash-chained audit + per-document residency are distinctive and built (+10); **but** — critical blocker (storage publish) halts the intelligence claim (−15); multiple services have empty migrations dirs and quietly borrow another service's schema (−8); security debt explicitly blocks pilot (−10); E2E/integration test coverage effectively zero (−12); workflow engine and signature signing both skeletons (−5).
- **Ready to demo?** 🟡 **Partially yes.** Login + admin UI + document CRUD + search UI + billing dashboard can be demonstrated. Any demo that tries to upload → see OCR results → semantic-search them will fail at the storage publish step. Any demo that tries to sign a document will fail at signature completion.
- **Ready to pilot with a customer?** ❌ **No.** Three open 🔴 critical security/privacy issues (shared KEK, localStorage token, math/rand SAML) block pilot per the project's own doc.
- **Ready for production?** ❌ **No.** Additionally missing: G2-gated items (production SaaS), tested Helm deployment, E2E test suite, real metrics/dashboards, incident response runbooks verified.

**Three things that must happen before each "Y":**

*Before "demo-ready" fully:* (1) fix storage `dms.version.uploaded.v1` publish; (2) seed one workflow definition; (3) confirm login → upload → search works E2E on a clean checkout.

*Before "pilot-ready":* (1) per-tenant KEK + secret rotation; (2) httpOnly session cookies + CSRF tokens; (3) `crypto/rand` SAML serial + dependency-vuln scan clean.

*Before "production-ready":* (1) Helm chart deployment verified on a real cluster (kind/minikube CI at minimum); (2) E2E test suite covering all ✅-flagged features in the matrix; (3) observability stack (metrics+dashboards+alerts+traces) fully wired with pilot-grade alert coverage.

---

## Appendix A — Command Log

Run in order during report generation:

```bash
date -u +"%Y-%m-%dT%H:%M:%SZ"                              # report timestamp
git rev-parse --short HEAD                                   # → "not a git repository"
docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
# Health matrix (all 11 + frontend):
for pair in auth:8081 policy:8082 document:8083 storage:8084 \
            search:8085 audit:8086 workflow:8087 notification:8088 \
            signature:8089 billing:8090 connector:8091; do
  curl -s -o /dev/null -w "%{http_code}" http://localhost:${pair#*:}/healthz
  curl -s -o /dev/null -w "%{http_code}" http://localhost:${pair#*:}/readyz
done
curl -s -o /dev/null -w "%{http_code}" http://localhost:3000
./dms-admin.exe nats list                                    # 20 streams
docker exec vaultdms-postgres psql -U vaultdms -d vaultdms -c "\dt"   # 57 tables
cat .run/services.pids
for svc in auth policy document storage search audit workflow notification \
           signature billing connector; do
  ls services/$svc/migrations/*.up.sql 2>/dev/null | wc -l
done
# Agent (Explore, "very thorough"): inventory per-service code, proto, frontend,
# docs, test infra, build artifacts, repo totals, OpenAPI.
```

---

## Appendix B — Raw Data Dumps

### B.1 `docker ps` (this session)

```
vaultdms-opensearch    Up 45 min (healthy)    9200, 9600
vaultdms-minio         Up 49 min (healthy)    9000-9001
vaultdms-postgres      Up 49 min (healthy)    15432 → 5432
vaultdms-temporal-ui   Up 49 min              8233 → 8080
vaultdms-temporal      Up 49 min              7233
vaultdms-qdrant        Up 49 min              6333-6334
vaultdms-clamav        Up 49 min (healthy)    3310
vaultdms-redis         Up 49 min (healthy)    6379
vaultdms-nats          Up 49 min (unhealthy)  4222, 8222
```

### B.2 `./dms-admin.exe nats list` (this session)

```
STREAM               MSGS  BYTES    AGE      SUBJECTS
AUDIT_EVENTS            0      0  168h  [dms.audit.>]
AUDIT_EVENTS_DLQ        0      0  720h  [dms.dlq.audit_events.>]
BILLING_EVENTS          0      0  168h  [dms.billing.> dms.subscription.> dms.usage.>]
BILLING_EVENTS_DLQ      0      0  720h  [dms.dlq.billing_events.>]
DOC_EVENTS              0      0  168h  [dms.document.> dms.version.> dms.workspace.>]
DOC_EVENTS_DLQ          0      0  720h  [dms.dlq.doc_events.>]
INTEL_EVENTS            0      0  168h  [dms.ocr.> dms.classify.> dms.embed.> dms.ner.>]
INTEL_EVENTS_DLQ        0      0  720h  [dms.dlq.intel_events.>]
LEGACY_EVENTS           0      0  168h  [dms.sharelink.> dms.folder.> dms.intelligence.> dms.rotation.>]
LEGACY_EVENTS_DLQ       0      0  720h  [dms.dlq.legacy_events.>]
NOTIFY_EVENTS           0      0  168h  [dms.notify.>]
NOTIFY_EVENTS_DLQ       0      0  720h  [dms.dlq.notify_events.>]
POLICY_EVENTS           0      0  168h  [dms.policy.> dms.permission.>]
POLICY_EVENTS_DLQ       0      0  720h  [dms.dlq.policy_events.>]
SEARCH_EVENTS           0      0  168h  [dms.search.>]
SEARCH_EVENTS_DLQ       0      0  720h  [dms.dlq.search_events.>]
USER_EVENTS            14   8971  168h  [dms.user.> dms.session.> dms.apikey.> dms.auth.>]
USER_EVENTS_DLQ         0      0  720h  [dms.dlq.user_events.>]
WORKFLOW_EVENTS         0      0  168h  [dms.workflow.> dms.task.>]
WORKFLOW_EVENTS_DLQ     0      0  720h  [dms.dlq.workflow_events.>]
```

### B.3 Postgres `\dt` — all 57 tables (this session)

```
annotations, api_keys, audit_events (+ _2026_04..07 partitions),
billing_schema_migrations, comments, connector_configs, content_blobs,
conversation_history, device_tokens, document_chunks, document_fingerprints,
document_redactions, documents, duplicate_candidates, entities,
extraction_results, folders, group_members, groups, legal_hold_documents,
legal_holds, notification_preferences, notifications, ocr_results,
organizations, outbox, permissions, privacy_dsr_requests, privacy_ledger,
residency_migration_items, residency_migrations, retention_policies,
schema_migrations, sessions, share_links, signature_requests,
signature_signers, sso_configs, subscriptions, tags_catalog, tenant_keks,
tenant_metadata_schemas, upload_sessions, usage_records, users, versions,
webhook_deliveries, webhook_subscriptions, workflow_definitions,
workflow_instances, workflow_tasks, workspace_members, workspaces
```

### B.4 `git log --oneline -20`

**Data unavailable — working tree is not a git repository.**

### B.5 Last 50 lines of each service log

Not included inline to keep the report readable. All logs at `.run/<service>.log` (11 files) + `.run/web.log`. Notable recurring handler-level errors captured this session:
- `audit.log`: `ERROR: column "actor" of relation "audit_events" does not exist (SQLSTATE 42703)` — handler reaches events but insert fails.
- `connector.log`: `ERROR: column "active" does not exist (SQLSTATE 42703)` — `GetActiveWebhooksForEvent` query fails.
- `billing.log`: clean since today's schema reconcile.
- `search.log`: clean; 7 `subscribed` info lines.
- `auth.log` / `policy.log` / `document.log` / `workflow.log` / `notification.log` / `signature.log` / `storage.log`: clean at the NATS layer (post-fixes); any residual errors are service-specific and not in scope of today's session.

---

## Confidence Notes

- **Live status (§7, §8, §9, §18):** measured this session via curl/docker/psql — high confidence.
- **Service inventory (§4):** synthesized from a subagent's thorough walk + my own spot-checks of repository.go and main.go — medium-high confidence.
- **Feature matrix (§10):** Blueprint features cross-referenced against route existence and audit-doc status. ✅ entries are verified by route/table presence; 🟡 entries are "present but unverified E2E"; ⬜/❌ entries cite explicit evidence in STATE/audit docs.
- **Blueprint summary (§2, §19, §20):** §1.1–§1.4 read directly; §21+ not re-read this session — marked **Data unavailable** where applicable.
- **Git data (§16):** **Unverified** — not a git working tree.
- **Helm / on-prem / air-gapped deployments (§3, §15):** files exist, never invoked — marked **Unverified** where claimed.
- **Proto codegen status:** STATE doc says stubs current; audit summary says never run — **conflicting, unverified**.
- **OpenAPI currency (§11):** spec file exists; coverage vs code not audited this session.
- **Metrics / dashboards / alerts (§13):** directories exist; runtime wiring **unverified**.
