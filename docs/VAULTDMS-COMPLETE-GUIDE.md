# VaultDMS — Complete Product & Engineering Guide

> **VaultDMS** (repo: `aieera/DOCMS`) is a multi-tenant, region-aware, policy-enforced
> **enterprise Document Management System**. This document is the single, end-to-end
> reference: what it is, how it is built, every feature it ships today, the API and
> event surface, how to run and deploy it, and the roadmap of features we can still add.
>
> - **Status:** Waves 5–14 of the build blueprint are structurally complete; Intelligence
>   features 01–10 shipped. The system is at the *Production-Ready SaaS → Feature-Complete*
>   gates (G2 → G3).
> - **License:** Proprietary © 2026 Raabyt. Licensed, not sold.
> - **Audience:** engineers, integrators, product, and onboarding.
> - **Source of truth:** `proto/` (API contracts), `docs/adr/` (decisions),
>   `docs/runbooks/` (operations), `docs/architecture.md` (design).

---

## Table of contents

1. [What VaultDMS is](#1-what-vaultdms-is)
2. [Technology stack](#2-technology-stack)
3. [Architecture](#3-architecture)
4. [Service catalog](#4-service-catalog)
5. [Shared libraries (`pkg/`)](#5-shared-libraries-pkg)
6. [Data, security & tenancy model](#6-data-security--tenancy-model)
7. [Feature catalog — Core DMS](#7-feature-catalog--core-dms)
8. [Feature catalog — Intelligence / AI](#8-feature-catalog--intelligence--ai)
9. [Feature catalog — Search](#9-feature-catalog--search)
10. [Feature catalog — Collaboration](#10-feature-catalog--collaboration)
11. [Feature catalog — Workflows & approvals](#11-feature-catalog--workflows--approvals)
12. [Feature catalog — Signatures / e-sign](#12-feature-catalog--signatures--e-sign)
13. [Feature catalog — Compliance, retention & governance](#13-feature-catalog--compliance-retention--governance)
14. [Feature catalog — Identity & security](#14-feature-catalog--identity--security)
15. [Feature catalog — Integrations & connectors](#15-feature-catalog--integrations--connectors)
16. [Clients: web, mobile, desktop add-ins, browser extension](#16-clients-web-mobile-desktop-add-ins-browser-extension)
17. [Admin console map](#17-admin-console-map)
18. [API surface (REST · gRPC · GraphQL · MCP)](#18-api-surface-rest--grpc--graphql--mcp)
19. [Event taxonomy & the intelligence pipeline](#19-event-taxonomy--the-intelligence-pipeline)
20. [Deployment topologies](#20-deployment-topologies)
21. [Local development guide](#21-local-development-guide)
22. [Observability, performance, chaos & DR](#22-observability-performance-chaos--dr)
23. [ADR register (decision log)](#23-adr-register-decision-log)
24. [Roadmap — current gaps & upcoming features](#24-roadmap--current-gaps--upcoming-features)
25. [Glossary](#25-glossary)

---

## 1. What VaultDMS is

VaultDMS is an enterprise DMS built as a **polyglot microservice platform**:

- **11 Go microservices** (`services/*`) — the transactional core and edge.
- **2 Python workers** — `intelligence` (OCR, NER, classification, embeddings, RAG, active
  learning) and `preview` (thumbnail/page rendering).
- **1 Node service** — `collaboration` (real-time WebSocket / Yjs co-editing, comments,
  annotations).
- **1 React/Vite web frontend** (`web/`), plus a **React Native mobile app** (`mobile/`),
  **Office add-ins** (`addins/outlook`, `addins/word`), and a **browser extension**
  (`extension/`).

Go modules are stitched with `go.work`; protobufs in `proto/` are the source of truth for
service APIs and generate into `proto/gen/go`.

**What it does, in one paragraph:** tenants upload documents; storage scans (ClamAV) and
envelope-encrypts each blob; the document service owns lifecycle, versioning, folders,
workspaces and permissions; a NATS-driven pipeline runs OCR → classification → NER →
embeddings → duplicate detection → auto-tag → smart-routing → compliance scan; search
indexes content (OpenSearch) and vectors (Qdrant) for hybrid + semantic + RAG question
answering; users collaborate (comments, annotations, co-authoring), route documents through
Temporal-backed approval workflows, e-sign them (PAdES), and govern them with retention,
legal hold, GDPR DSR and data-residency controls. Everything is multi-tenant-isolated at the
database layer and policy-enforced via OPA.

---

## 2. Technology stack

| Layer | Technology |
|---|---|
| Backend language | **Go 1.25** (toolchain 1.26.2), gRPC + REST (grpc-gateway) |
| AI / workers | **Python** (OCR, NER, classification, embeddings, RAG, model training) |
| Realtime collab | **Node.js** (WebSocket, **Yjs** CRDT) |
| Frontend | **React 18 + TypeScript + Vite**, **TanStack Router + Query**, **Zustand**, **Radix UI** + **shadcn**, **Tailwind CSS** (with RTL logical utilities) |
| Mobile | **React Native / Expo** |
| Database | **PostgreSQL 16** — Row-Level Security for tenant isolation, per-service migrations |
| Full-text search | **OpenSearch 2.12** |
| Vector DB | **Qdrant 1.7** |
| Cache / pub-sub | **Redis 7** |
| Object storage | **MinIO** (dev) / **S3 / GCS / Azure Blob / Dell ECS / Ceph** (prod) |
| Event bus | **NATS 2.10 + JetStream** (subjects `dms.{domain}.{action}.v1`) |
| Workflow engine | **Temporal** (durable execution) |
| Authorization | **OPA** (Open Policy Agent, embedded; Rego policies) |
| Antivirus | **ClamAV** (INSTREAM scan on upload) |
| Secrets / keys | **Vault** or **AWS KMS** (prod); HKDF-derived local KEK (dev/on-prem) |
| e-Sign / PAdES | **EU Commission DSS** (Java sidecar `signature-signer`); LTV support |
| LLM routing | **litellm** (OpenAI / Anthropic / Ollama / on-prem), per-tenant config |
| Packaging | **Docker Compose** (dev), **Helm** chart, **Ansible** bundle (on-prem/air-gapped) |
| Observability | **Prometheus** (`/metrics` :8081), **OpenTelemetry** → Tempo, **Grafana** dashboards |
| CI/CD | GitHub Actions (`.github/workflows`), images → `ghcr.io/aieera/docms` |

---

## 3. Architecture

Read `docs/architecture.md` for the authoritative diagram. The load-bearing decisions:

### 3.1 Two communication modes, deliberately split

- **Synchronous gRPC** — only for hot-path decisions that cannot be deferred:
  - `document → policy` permission check on every read/write
  - `storage → policy` on every upload initiate
  - `storage → ClamAV` for the virus scan
- **Asynchronous NATS JetStream** — everything else: search indexing, notification
  fan-out, audit log, connector webhooks, AI post-processing. Subjects follow
  `dms.{domain}.{action}.v1`.

### 3.2 Transactional outbox (never publish directly)

Every domain write inserts a row into the per-service `outbox` table **inside the same DB
transaction**. A polling publisher forwards outbox rows to NATS. This guarantees
at-least-once delivery even if the process crashes between commit and publish. **Handlers
never publish to NATS directly.**

### 3.3 Tenant isolation = Postgres RLS + `NOBYPASSRLS` role

- Every tenant table has `tenant_id UUID` as the first column of its primary key, with an
  RLS policy: `USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`.
- All queries run inside `database.WithTenantTx(tenantID, ...)`, which opens a transaction
  and issues `SET LOCAL app.current_tenant`.
- The app DB role is `NOBYPASSRLS`, so a query that forgets the tenant predicate
  **fails closed (0 rows)** rather than leaking. NATS consumers re-establish tenant context
  from the message envelope before any DB work.

### 3.4 Per-blob envelope encryption + crypto-shredding

Each blob has a **DEK** (data encryption key) wrapped by a **per-tenant KEK** (key
encryption key). KEKs are Vault/AWS-KMS-backed in prod, HKDF-SHA256-derived from a master
secret in dev/on-prem, and **per-region** (ADR 0026) for residency isolation. Deletion is
**crypto-shredding**: drop the KEK and every DEK is unusable, every ciphertext is garbage.

### 3.5 Correlation & context propagation

A `correlation-id` threads the whole chain: HTTP header → NATS message header → every log
line via consumer middleware. OTEL spans carry `tenant_id`, `user_id`, `workspace_id` as
span attributes.

### 3.6 Document lifecycle invariants

```
draft → in_review → active → superseded → retained → archived → disposed
```

- `legal_hold` freezes **any** lifecycle transition.
- `region_pin` is immutable after the first upload — PATCH cannot change it; cross-region
  moves require the compliance migration tool.

---

## 4. Service catalog

Each Go service follows the convention `cmd/server` (entrypoint) +
`internal/{handler,service,repository,model,...}` + `migrations/`. The **document service is
the reference implementation** — copy its layering when scaffolding new services. Migrations
are per-service (`<svc>_schema_migrations` bookkeeping; versions do **not** share a namespace
across services).

| Service | Lang | Responsibility | Key tech |
|---|---|---|---|
| **auth** | Go | Sessions, password, MFA (TOTP/passkeys/WebAuthn), SSO (SAML/OIDC), SCIM provisioning, LDAP/AD, API keys, `/auth/me` | Postgres, Redis, crypto/rand |
| **policy** | Go | Authorization decisions via embedded OPA; Redis-cached permission checks (<5 ms); permission matrix | OPA/Rego, Redis |
| **document** | Go | Document CRUD, versioning, folders, workspaces, restore/trash, lifecycle, metadata schemas, tags, intelligence-result endpoints; **emits `dms.version.uploaded.v1`** (ADR 0021) | Postgres RLS, gRPC→policy |
| **storage** | Go | Multipart/chunked upload, presigned URLs, ClamAV scan, per-blob envelope encryption, blob re-encrypt/rewrap, region pinning | S3/MinIO, ClamAV, KMS |
| **search** | Go | OpenSearch indexer, hybrid + faceted + permission-filtered search, autocomplete, saved searches & alerts, federated search admin | OpenSearch, NATS consumer |
| **workflow** | Go | Temporal-backed approval workflows, task inbox, workflow designer/templates, routing patterns | Temporal, Postgres |
| **notification** | Go | In-app + real-time + email notifications, per-user preferences, rate limiting, SMTP | NATS, Redis, SMTP |
| **audit** | Go | Hash-chained tamper-evident audit log, CSV export, integrity verification, audit-trail visualization | Postgres (hash chain) |
| **signature** | Go | e-sign orchestration: envelopes, signers, in-person/mobile signing, QES/TSP, third-party connectors | NATS, signer sidecar |
| **signature-signer** | Java | PAdES signing sidecar (EU Commission DSS), LTV, HSM/TSP integration | DSS, Java |
| **billing** | Go | Stripe metering + webhooks, usage records, seat/feature licensing hooks | Stripe, Postgres |
| **connector** | Go | Outbound customer webhooks (HMAC-signed), native connector OAuth, email ingestion, watched-folder intake, iPaaS, M365 Graph | OAuth, NATS, HMAC |
| **graphql-gateway** | Go | GraphQL API facade over the REST/gRPC services (ADR 0074) | gqlgen-style schema |
| **mcp-server** | Go | Model Context Protocol server (SSE) so LLM agents can call DMS tools (ADR 0091) | MCP/SSE |
| **collaboration** | Node | Real-time co-authoring (Yjs CRDT, :8083), threaded comments, annotation layers | WebSocket, Yjs |
| **intelligence** | Python | OCR, classification, NER ensemble, embeddings, duplicate detection, RAG Q&A, translation, OCR-quality, anomaly detection, active-learning model training | litellm, transformers, Qdrant |
| **preview** | Python | Thumbnail / page rendering for the viewer | S3, render libs |

> **Infrastructure tier (Docker Compose):** postgres, redis, nats, minio, opensearch, qdrant,
> temporal, temporal-ui, clamav, minio-init (one-shot). ~23 containers report healthy once
> start-up periods pass.

---

## 5. Shared libraries (`pkg/`)

The `pkg/` module is the shared backend standard library; every service imports it.

| Package | Purpose |
|---|---|
| `config` | Env/secret loading, SIGHUP reload |
| `logger` | Structured zerolog logging, PII redaction at the middleware layer |
| `middleware` | tenant resolver → auth → policy → rate-limit → idempotency → handler chain |
| `database` | `WithTenantTx`, RLS helpers, connection pooling, migrations bookkeeping |
| `tenant` | Tenant context extraction & propagation (HTTP + NATS) |
| `events` | Outbox publishing, NATS JetStream consumers, dedupe, DLQ |
| `crypto` | Envelope encryption, DEK/KEK wrapping, HKDF derivation |
| `storage` | S3 client abstraction |
| `signing` / `esign` | PAdES / e-sign client helpers |
| `auth` | Shared auth primitives, token validation |
| `gateway` | grpc-gateway / REST wiring helpers |
| `dlp` | Data-loss-prevention helpers (PII patterns) |
| `notifications` | Notification send helpers |
| `license` | License validation & feature gating (ADR 0095) |
| `errors` | Typed error model, consistent HTTP/gRPC mapping |
| `validation` | Request validation |
| `metrics` | Prometheus counters/histograms helpers |
| `tracing` | OpenTelemetry span helpers |
| `health` | Liveness/readiness on `/healthz`, `/metrics` :8081 |
| `archtest` | Architecture/layering enforcement tests |
| `testutil` / `testharness` | Integration & RLS test helpers (testcontainers) |

---

## 6. Data, security & tenancy model

- **Tenant isolation:** Postgres RLS + `NOBYPASSRLS` app role (fail-closed). See §3.3.
- **Encryption at rest:** per-blob DEK wrapped by per-tenant, per-region KEK; crypto-shred
  deletion (ADR 0022, 0026). KEK rotation/rewrap supported (re-encrypt/rewrap-regional).
- **Encryption in transit:** TLS at ingress; signed workload-identity headers between
  services (zero-trust intent).
- **Session security:** httpOnly + Secure + `SameSite=Strict` cookies, CSRF double-submit
  token (no tokens in localStorage). Runbook: `06-session-cookies-csrf.md`.
- **Secrets:** loaded from KMS/Vault at boot, no secrets in source.
- **Audit:** hash-chained, tamper-evident, integrity-verifiable, exportable; visualized in
  the admin audit-trail viz (ADR 0103).
- **Idempotency:** mutating POSTs accept an idempotency key (tenant + client UUID, 24h).
- **Lifecycle & holds:** legal hold freezes transitions; region pin immutable post-upload.
- **Headers:** CSP, HSTS preload, `X-Frame-Options: DENY`.

---

## 7. Feature catalog — Core DMS

| Feature | Notes |
|---|---|
| **Documents & versions** | Full CRUD, immutable version history, restore previous versions |
| **Folders & hierarchy** | Nested folders, move, permission-inherited placement |
| **Workspaces** | Workspace-scoped documents, settings, membership |
| **Trash & restore** | Soft delete, trash view, restore |
| **Metadata schemas** | Per-tenant custom metadata schema definitions (admin-managed) |
| **Tags** | Manual + AI-suggested tags, tenant tag admin |
| **Upload** | Multipart/chunked, presigned URLs, resumable, ClamAV scan, encrypt-on-complete |
| **Upload policy** | Per-tenant upload policy (size/type limits) |
| **Document viewer** | Rendered pages/thumbnails (preview worker), text viewer, PDF highlight from AI citations |
| **Share links** | Public/scoped share links with admin management & revocation |
| **Shared-with-me** | Inbound shares surfaced to the user |
| **Zero-trust share** | Token-gated, step-up-verified external share (`/zt/$token`, ADR 0098) |
| **Lifecycle states** | `draft → in_review → active → superseded → retained → archived → disposed` |
| **Bulk import/export** | gRPC bulk API (ADR 0075) + admin bulk page |

---

## 8. Feature catalog — Intelligence / AI

The Python `intelligence` worker runs an event-driven pipeline. Every task is
`acks_late=True`, ≤3 retries with exponential backoff + jitter, deduped via
`intel_processed_events`, and DLQ'd on terminal failure.

| Capability | ADR | Trigger | Output |
|---|---|---|---|
| **OCR** | — | `dms.version.uploaded.v1` | `ocr_results`, emits `…ocr_completed.v1` |
| **Classification** | — | `…ocr_completed.v1` | `document_classifications` |
| **NER ensemble** | 0078 | `…ocr_completed.v1` | regex + SpaCy + opt-in LLM; `document_entities` with provenance; PII/Financial/Legal/Medical taxonomy |
| **Entity extraction** | — | `…ocr_completed.v1` | `extraction_results` |
| **Embeddings** | — | `…ocr_completed.v1` | Qdrant vectors |
| **Duplicate detection** | — | OCR + embed | sha256/minhash/simhash + embedding similarity → `duplicate_candidates` |
| **Auto-tagging** | 0052 | classify + NER | `tag_suggestions`; never auto-applies below admin threshold |
| **Smart routing** | 0053 | classify | folder suggestions (rule/history/similarity); never auto-moves in v1 |
| **Compliance scan (PII/PHI)** | 0054 | NER | `compliance_findings`; recommends, never auto-holds |
| **Document Q&A (RAG)** | 0055, 0080 | on-demand REST/SSE | `qa_conversations/messages`, cited answers with page/char highlight |
| **Translation** | 0056 | on-demand | `document_translations` keyed on (version, target lang) |
| **Language detection** | 0056 | `…ocr_completed.v1` | `document_languages` |
| **OCR quality scoring** | 0057 | `…ocr_completed.v1` | 5-subscore composite + grade; single-shot auto-retry |
| **Anomaly detection** | 0058 | on-demand | metadata z-score + content centroid distance + behavioral |
| **Classification corrections** | 0059 | user action | append-only ledger; bulk reclassify |
| **Active learning** | 0060 | corrections threshold | per-tenant DistilBERT fine-tune → evaluate → admin-gated promote/retire |
| **Summarization** | — | on-demand | emits `…summarize.completed.v1` |
| **Redaction** | 0079 | `…document.redacted.v1` | `document_redactions`, redaction review workflow |
| **Contract intelligence graph** | 0099 | — | clause/entity relationship graph |
| **Cross-format compare** | 0101 | — | semantic diff across formats |
| **Clause library** | 0104 | — | reusable clause catalog (`/clauses`) |
| **LLM routing** | 0081 | — | litellm multi-provider, per-tenant config + usage metering |

**Document-service intelligence REST endpoints** expose all of the above to the UI:
duplicates confirm/reject, tag-suggestion review, route-suggestion accept/dismiss,
compliance review & dashboard, OCR-quality review queue, anomaly reports, model
management (`/admin/models`, promote/retire/retrain), training-example stats, active-learning
config, and per-document `entities` + `entities/correct` + correction ledger. (Full table in
`README.md`.)

---

## 9. Feature catalog — Search

| Feature | ADR | Notes |
|---|---|---|
| Full-text indexing | — | OpenSearch, NATS-driven partial updates from OCR |
| Hybrid search | — | keyword + semantic merge |
| Faceted search | 0082 | filter by class, tags, date, type, etc. |
| Permission-filtered search | 0083 | results respect per-user document permissions |
| Autocomplete | 0084 | query suggestions |
| Saved searches & alerts | 0085 | persisted queries + notification on new matches |
| Federated search (admin) | 0069 | search across external/connected sources |
| Semantic / RAG | 0080 | Qdrant vector retrieval feeding Document Q&A |

---

## 10. Feature catalog — Collaboration

| Feature | ADR | Notes |
|---|---|---|
| Real-time co-authoring | 0065, 0096 | Yjs CRDT via Node `collaboration` service (:8083); OnlyOffice/Collabora integration path |
| Threaded comments | 0066 | comment threads on documents |
| Annotation layers | 0067 | highlights/markup overlay on the viewer |
| Lightweight tasks | 0068 | quick task assignment (`/tasks`) |
| Notifications | 0086 | in-app + real-time + email, per-user preferences |

---

## 11. Feature catalog — Workflows & approvals

| Feature | ADR | Notes |
|---|---|---|
| Temporal-backed workflows | 0023 | single namespace `vaultdms` + `tenant_id` search attribute |
| Approval routing patterns | 0064 | sequential, parallel, quorum routing |
| Workflow designer & templates | — | visual designer (`/workflows`, template edit) |
| Task inbox ("My Tasks") | — | surfaces assigned workflow steps |
| Workflow instances | — | per-instance timeline view |

---

## 12. Feature catalog — Signatures / e-sign

| Feature | ADR | Notes |
|---|---|---|
| PAdES signing | 0025 | EU Commission DSS Java sidecar (`signature-signer`) |
| PAdES LTV | 0072 | long-term validation (tier-2 runbook) |
| Signing orchestration | — | envelopes, signers, ordering, status |
| QES / TSP integration | 0070 | qualified e-signatures via trust service providers / HSM |
| Third-party e-sign connectors | 0071 | e.g. DocuSign (currently mock via `ESIGN_MOCK_OK`) |
| Mobile / in-person signing | 0073 | `/sign/in-person/$requestId`, signature pad |
| Signature validity badge | — | UI verification indicator |

---

## 13. Feature catalog — Compliance, retention & governance

| Feature | ADR | Notes |
|---|---|---|
| Retention policies | — | scheduled retention cron, per-tenant policies (admin) |
| Legal holds | — | freeze lifecycle; admin legal-holds page |
| GDPR DSR | 0024, 0114 | per-request Temporal workflow, hold short-circuit, 7-yr ledger, HMAC anonymization, admin approval workflow |
| DSR verify token | — | tokenized subject verification |
| Cross-service erase / purge | — | fan-out erase + connector purge on DSR |
| Redaction review | 0079 | review queue before applying redactions |
| Data residency | 0026, 0110 | per-region KEK + region pinning; UAE residency support |
| Compliance scanning | 0054 | PII/PHI detection + dashboard |
| Audit trail | — | hash-chained, exportable, visualized (0103) |
| Document-processing failure surface | 0115 | makes silent pipeline failures visible to admins |

---

## 14. Feature catalog — Identity & security

| Feature | ADR | Notes |
|---|---|---|
| Password + sessions | — | httpOnly/Secure/SameSite cookies + CSRF |
| MFA (complete surface) | 0063 | TOTP, recovery codes, enforcement policy |
| Passkeys / WebAuthn | 0061 | passwordless / second factor |
| SSO (SAML/OIDC) | — | SSO wizard, crypto/rand X.509 serials |
| SCIM provisioning | — | automated user lifecycle from IdP |
| LDAP / AD direct | 0062 | direct directory bind |
| MFA policy / enforcement | — | per-tenant policy page |
| Permission matrix | — | role/permission visualization & admin |
| Zero-trust share | 0098 | step-up verified external access |
| License enforcement | 0095 | per-feature/seat gates (phase 1+2 shipped) |

---

## 15. Feature catalog — Integrations & connectors

| Feature | ADR | Notes |
|---|---|---|
| REST API (`/api/v1/*`) | — | versioned, API-key + session auth, scopes, idempotency, pagination |
| API keys | — | tenant-scoped Bearer `vdms_…`, scope-gated, admin-managed |
| Customer webhooks (outbound) | 0076 | HMAC-signed delivery of domain events |
| Event streaming | 0077 | stream domain events to external consumers |
| Native connectors | 0089 | OAuth-based (Google Drive landed; Salesforce/M365 roadmap) |
| iPaaS | 0090 | Zapier / Make / n8n (backend + admin built; tile gated pending app publish) |
| MCP server | 0091 | LLM agents call DMS tools over SSE |
| GraphQL API | 0074 | gateway facade |
| gRPC bulk import/export | 0075 | high-throughput ingest/export |
| Email ingestion | 0087 | inbound email → documents |
| Watched-folder intake | 0088 | drop-folder ingestion |
| M365 Graph connector | 0111 | Microsoft 365 content |
| Outlook add-in | 0112 | `addins/outlook` |
| Word add-in | 0113 | `addins/word` |
| Browser extension | 0097 | `extension/` (capture/save to DMS) |
| ERP integration | — | dual-auth (session-or-API-key) push; HMAC webhook receiver; worked example in `docs/integrations/erp-integration.md` |

---

## 16. Clients: web, mobile, desktop add-ins, browser extension

- **Web (`web/`)** — React 18 + Vite + TanStack Router/Query + Zustand + Radix/shadcn +
  Tailwind. File-based routes under `web/src/routes`; API clients in `web/src/api/*`;
  feature components under `web/src/components/{documents,intelligence,ai,workflow,signatures,viewer,…}`.
- **Mobile (`mobile/`)** — React Native / Expo app (`app/`, `api/`, `store/`).
- **Office add-ins (`addins/`)** — Outlook and Word task-pane add-ins.
- **Browser extension (`extension/`)** — manifest v3 (background, content, popup, options,
  OAuth callback) for capture-to-DMS.

---

## 17. Admin console map

The web app's `/admin/*` area (TanStack file routes) — a quick map of what an administrator
can configure:

- **Tenant & identity:** `tenant`, `tenant/identity` (+`ldap`), `tenant/license`, `users`,
  `groups`, `sso`, `identity`, `mfa-policy`, `permissions`, `permission-lag`.
- **Governance:** `retention`, `legal-holds`, `privacy`, `data-governance`, `residency`,
  `compliance`, `pii`/`pii-scanning`, `audit-log`.
- **Intelligence:** `intelligence/` (auto-tag, tag-review, routing-rules, filing-analytics,
  compliance(+config), ocr-review/ocr-config, ner-config, anomalies/anomaly-reports,
  models, usage).
- **Content config:** `metadata-schema`, `tags`/`tagging`, `routing`, `ocr`, `ai`,
  `upload-policy`, `workflows`, `settings`/`tenant-settings`.
- **Integrations:** `integrations/` (index, ipaas, events, email, mcp), `integrations-hub`,
  `connectors`, `webhooks`, `api-keys`, `share-links`.
- **Platform:** `platform/` (db-info, load-tests, support-search), `billing`, `bulk`.

User-facing routes include `/` (dashboard), `/search`, `/saved-searches`, `/ask` (Q&A),
`/tasks`, `/workflows`, `/notifications`, `/clauses`, `/workspaces`, `/trash`,
`/shared-with-me`, and signing routes.

---

## 18. API surface (REST · gRPC · GraphQL · MCP)

- **Protobuf contracts** (`proto/vaultdms/v1/`) are the source of truth and generate Go
  stubs, grpc-gateway REST, and OpenAPI:
  `auth, policy, document, storage, search, workflow, notification, audit, signature,
  billing, collaboration, intelligence, bulk, events, common`.
- **REST:** versioned `/api/v1/*`. Auth = session cookie (web) **or** Bearer API key
  (`Authorization: Bearer vdms_…`, tenant-scoped + scope-gated). Idempotency keys on
  mutating POSTs; cursor pagination; HMAC-signed webhooks for events.
  Contracts: `docs/api/openapi.yaml` (Redoc), `docs/api/SEMVER_POLICY.md`,
  `docs/api/INTEGRATION_GUIDE.md`, `docs/integrations/erp-integration.md`.
- **gRPC:** internal hot-path calls + bulk import/export.
- **GraphQL:** `graphql-gateway` facade (ADR 0074).
- **MCP:** `mcp-server` exposes DMS tools to LLM agents over SSE (ADR 0091).
- **Gateway/edge:** Kong (`kong.yaml`) fronts REST upstreams; gateway-signature secret
  rendered at boot.

---

## 19. Event taxonomy & the intelligence pipeline

Subjects follow `dms.{domain}.{action}.v1`. Examples: `dms.version.uploaded.v1`,
`dms.version.ocr_completed.v1`, `dms.classify.completed.v1`, `dms.ner.completed.v1`,
`dms.embed.completed.v1`, `dms.autotag.completed.v1`, `dms.routing.completed.v1`,
`dms.compliance.completed.v1`, `dms.language.detected.v1`, `dms.translation.completed.v1`,
`dms.ocr.quality.completed.v1`, `dms.anomaly.completed.v1`, `dms.classify.corrected.v1`,
`dms.model.promoted.v1`, `dms.user.*`, `dms.signature.*`. DLQ subjects:
`dms.dlq.intel_events.<consumer>.<reason>`.

**Pipeline chain:** `version.uploaded` → OCR → `ocr_completed` fans out to **classify, NER,
extract, embed, duplicate, language-detect, OCR-quality** in parallel; `classify_completed`
→ smart-routing + (with NER) auto-tag; `ner_completed` → compliance scan; corrections feed
the **active-learning loop** (collector → retrain → evaluate → promote). Each NATS subject is
declared in stream bootstrap with a defined DLQ. Runbooks: `docs/runbooks/05-*`.

---

## 20. Deployment topologies

### SaaS (default `values.yaml`)
Shared multi-tenant Postgres/Redis/OpenSearch/NATS; external S3; KMS-backed KEK; shared
Temporal; nginx + cert-manager + Let's Encrypt ingress; HPA + PDB on every service;
Prometheus ServiceMonitors + OTLP traces to Tempo.

### On-prem (`values-onprem.yaml`)
Everything in-cluster; S3 → Dell ECS/Ceph/MinIO; on-prem SMTP; telemetry stays in-cluster;
**Ollama** for LLM (no external OpenAI); billing deployed but Stripe disabled.

### Air-gapped (`values-airgapped.yaml`)
No outbound internet; images pre-loaded to an internal registry; all LLM via Ollama; audit
export to local SIEM; **offline signed license file** mounted as a Secret; connector OAuth
callbacks disabled.

### Packaging
- **Docker Compose** — `make docker-up` (build) / `make docker-up-prebuilt` (pull
  `ghcr.io/aieera/docms`). `docker-compose.{prod,prebuilt,passkeys}.yml` variants.
- **Helm chart** (ADR 0092) — `docs/runbooks/helm-install.md`.
- **Ansible bundle** (ADR 0093) — `docs/runbooks/ansible-install.md`.
- **Alternative DB adapters** (ADR 0094) — beyond stock Postgres.

---

## 21. Local development guide

**Backend (repo root):**

| Task | Command |
|---|---|
| One-shot onboarding (gen-env → up → migrate → seed) | `make setup` |
| Build all binaries → `./bin` | `make build` |
| All Go tests (race) | `make test` / one service `make test-<svc>` |
| Single test | `go test -race -run TestName ./services/<svc>/internal/<pkg>/...` |
| Integration tests (testcontainers) | `go test -tags integration ./services/<svc>/...` |
| Lint / format / tidy | `make lint` · `make fmt` · `make tidy` |
| Regenerate proto + gateway + OpenAPI | `make proto-gen` |
| Proto lint / breaking check | `make proto-lint` · `make proto-breaking` |
| Security scan | `make security-check` (gosec + govulncheck) |
| Migrations | `make migrate-up SERVICE=document` (also `migrate-down`, `migrate-create NAME=…`) |
| Stack up/down | `make docker-up` · `make docker-up-prebuilt` · `./scripts/wait-for-healthy.sh` |
| Run Go services on host | `docker compose stop <svc…>` then `make run-all` / `make stop-all` |
| Reset (DANGER: down -v + re-setup) | `make reset` |

**Frontend (`cd web`):** `npm run dev` (or `make run-web`), `npm run build`, `npm test`
(vitest; single: `npm test -- -t "name"`), `npm run test:e2e` (Playwright), `npm run lint`.

> **Port note:** you cannot run host services and their containers simultaneously — ports
> collide. If host ports return `000` or cross-wire, batch-restart the API-fronting
> containers (known Docker/WSL recipe).

---

## 22. Observability, performance, chaos & DR

- **Metrics:** Prometheus on `/metrics` (:8081); per code path: `*_total`, `*_duration_seconds`,
  `*_errors_total`. Grafana dashboards in `ops/grafana/dashboards/`.
- **Tracing:** OpenTelemetry spans at every service boundary → Tempo.
- **Logging:** zerolog, structured, PII-redacted, carries `tenant_id/user_id/trace_id/correlation_id`.
- **Performance / load:** harness under `tests/load` + `docs/performance/` (ADR 0105;
  baseline template provided). A full campaign is scaffolded but a real run is deferred
  (cost + AWS-scoped creds).
- **Chaos:** scenarios in `docs/chaos/` — pod-kill, network partition, clock skew, disk full,
  NATS disconnect, KMS outage.
- **Disaster recovery:** `docs/runbooks/10-disaster-recovery.md` + DR-rehearsal template.

---

## 23. ADR register (decision log)

ADRs in `docs/adr/` are the authoritative record of cross-cutting decisions (Nygard format,
monotonic, immutable once merged). Highlights:

- **Foundations:** 0021 (version.uploaded emitter = document service), 0022/0026
  (per-tenant/per-region KEK), 0023 (Temporal namespacing), 0024 (GDPR DSR), 0025 (PAdES/DSS).
- **Intelligence:** 0052 auto-tag, 0053 smart-routing, 0054 compliance/PII, 0055 Q&A chat,
  0056 translation, 0057 OCR quality, 0058 anomaly, 0059 corrections, 0060 active learning,
  0078 NER, 0079 redaction, 0080 RAG, 0081 litellm, 0099 contract graph, 0101 cross-format
  compare, 0102 predictive filing, 0104 clause library.
- **Identity/security:** 0061 passkeys, 0062 LDAP/AD, 0063 MFA, 0095 license enforcement,
  0098 zero-trust share.
- **Collaboration:** 0064 approval routing, 0065 co-authoring, 0066 comments, 0067
  annotations, 0068 tasks, 0096 Yjs.
- **Search:** 0069 federated, 0082 faceted, 0083 permission-filtered, 0084 autocomplete,
  0085 saved-search alerts, 0086 notification prefs.
- **Signatures:** 0070 QES/TSP, 0071 third-party e-sign, 0072 PAdES-LTV, 0073 in-person.
- **Integrations/API:** 0074 GraphQL, 0075 gRPC bulk, 0076 webhooks, 0077 event streaming,
  0087 email ingestion, 0088 watched folders, 0089 native connectors, 0090 iPaaS, 0091 MCP,
  0097 browser extension, 0111 M365 Graph, 0112 Outlook add-in, 0113 Word add-in.
- **Platform/ops:** 0092 Helm, 0093 Ansible, 0094 DB adapters, 0103 audit-trail viz,
  0105 load-test harness, 0107 Tailwind RTL, 0110 UAE residency, 0114 DSR admin approval,
  0115 processing-failure surface.

> Note: `docs/adr/index.md`'s register table is current only through 0060; the files 0061–0115
> exist and are authoritative — treat the filenames as the canonical title list.

---

## 24. Roadmap — current gaps & upcoming features

These are the realistic next increments. They split into **finish-what-exists** (known
partial wiring) and **net-new** ideas.

### 24.1 Finish-what-exists (in-flight / partial)

| Item | State | Next step |
|---|---|---|
| **License enforcement** (ADR 0095) | Phase 1+2 in document service only; `pkg/license` + `cmd/license-gen` done | Wire the other 13 services, grace-period middleware, seat counting, per-feature gates |
| **Native connectors** (ADR 0089) | Google OAuth slice landed | Drive *import* action, then Salesforce + M365 wrappers (7 vendors pending) |
| **iPaaS** (ADR 0090) | Backend + admin UI built; tile/chip commented out | Publish Zapier/Make/n8n apps, then re-enable the tile |
| **DocuSign cutover** (ADR 0071) | Dev account exists; running on `ESIGN_MOCK_OK=true` | Complete real-provider steps; flip mock off |
| **M365 takeover hardening** | Email-based mapping closed in audit fix | Replace email mapping with a `(tid, oid)` link table |
| **tenant-id-from-context sweep** | ~30 handlers still derive tenant inconsistently | Standardize on context-derived tenant before main-branch merge |
| **graphql-gateway dial bugs** | startup-only `grpc.WithBlock`; `collaboration:9090` misconfigured (Yjs is :8083) | Lazy reconnect + correct address |
| **intelligence-worker silent OCR drops** | jobs occasionally dropped silently | Surface via processing-failure feature (ADR 0115) + DLQ alerting |
| **Helm verification** (ADR 0092) | chart shipped; live kind/K8s test deferred | Verify on standalone `kind` (not Docker Desktop K8s) |
| **Load-test campaign** (ADR 0105) | harness scaffolded | Run real campaign (flag ~$150–300 + 13–16h + EKS creds) |
| **Per-tenant KEK / proto-style tech debt** | tracked in `docs/tech-debt/` | Pay down per the debt notes |

### 24.2 Net-new feature ideas (candidates to add)

- **Desktop sync client** — selective-sync agent (the G3 "desktop sync" line item) for
  offline edit + reconcile.
- **Records management / disposition certificates** — formal disposition approvals and
  certificates on top of retention.
- **eDiscovery / legal export** — case-scoped collection, hold notices, load-file export.
- **Advanced PDF tooling** — in-browser redaction burn-in, page reorder/merge/split,
  form-field (AcroForm) capture.
- **Smart workspaces / rules engine** — saved-filter "smart folders" with automated actions
  (route, tag, notify) driven by classification + NER.
- **Analytics & reporting dashboards** — content growth, storage-by-class, compliance
  posture, signing turnaround, search-relevance KPIs.
- **More native connectors** — Box, Dropbox, SharePoint on-prem, NetSuite, SAP, Slack/Teams
  notifications.
- **Agentic workflows via MCP** — let the MCP server drive multi-step actions (classify →
  route → request approval) as first-class agent tools.
- **Configurable model marketplace** — per-tenant choice of embedding/LLM/classification
  models with cost guardrails (extends litellm routing + active learning).
- **Mobile capture** — scan-to-document with on-device edge detection feeding the OCR
  pipeline.
- **Fine-grained ABAC** — attribute-based policies (clearance, project, region) layered on
  the existing OPA model.
- **Watermarking & DRM** — dynamic watermarks and view-only/expiring access for sensitive
  shares.

---

## 25. Glossary

| Term | Meaning |
|---|---|
| **RLS** | Postgres Row-Level Security — the tenant-isolation mechanism |
| **KEK / DEK** | Key-Encryption-Key (per-tenant/region) wrapping a per-blob Data-Encryption-Key |
| **Crypto-shredding** | Deleting data by destroying its KEK so ciphertext is unrecoverable |
| **Outbox** | Per-service table written in the same tx as a domain change, polled and published to NATS |
| **DLQ** | Dead-letter queue subject for terminally failed events |
| **PAdES / LTV** | PDF Advanced Electronic Signatures / Long-Term Validation |
| **QES / TSP** | Qualified Electronic Signature / Trust Service Provider |
| **DSR** | Data Subject Request (GDPR access/erase) |
| **NER** | Named-Entity Recognition (PII/Financial/Legal/Medical taxonomy) |
| **RAG** | Retrieval-Augmented Generation (Document Q&A over Qdrant vectors) |
| **MCP** | Model Context Protocol — lets LLM agents call DMS tools over SSE |
| **ADR** | Architecture Decision Record (`docs/adr/`) |

---

*Generated from the live repository (services, `pkg/`, `proto/`, `docs/adr`, `docs/runbooks`,
`web/src/routes`, README & architecture docs). For anything ambiguous, the protobufs and ADRs
are authoritative.*
