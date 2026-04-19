# Tech Stack & Feature Inventory — Blueprint vs. Reality

**Generated:** 2026-04-19
**Sources:**
- Blueprint: [`DMS Architecture/dms-blueprint.md`](../../DMS%20Architecture/dms-blueprint.md) (sections 3.2, 4.x, 6.x, 8.x, 17.x, 18, 21)
- Live state: `docker ps`, `./dms-admin nats list`, `/healthz` probes, service source trees at [services/](../../services/), [web/](../../web/), [mobile/](../../mobile/).
- Cross-refs: [docs/STATE_OF_THE_PROJECT.md](../STATE_OF_THE_PROJECT.md), [docs/audit/02-missing.md](../audit/02-missing.md), this session's earlier [PROJECT_ANALYTICAL_REPORT_2026-04-18.md](PROJECT_ANALYTICAL_REPORT_2026-04-18.md) and [api-integration-matrix.md](api-integration-matrix.md).

Status legend: ✅ Shipped and working · 🟡 Partially built / wired but gated · ⬜ Planned, not started · ❌ Blocked/broken

---

## 1. Tech stack — blueprint vs. on-disk

### 1.1 Language runtimes

| Layer | Blueprint (§3.2, §17.x) | On disk | Match? |
|---|---|---|---|
| Core backend services (11) | Go 1.22 | Go (version pinned by [go.work](../../go.work); all 11 services compile and run — see [docs/STATE_OF_THE_PROJECT.md:60](../STATE_OF_THE_PROJECT.md#L60)) | ✅ |
| Intelligence service | Python 3.12 + FastAPI | Python service at [services/intelligence/](../../services/intelligence/) — **not running locally this session** | 🟡 Code present; Celery worker defined in compose `app` profile (not default-started) |
| Preview service | Python 3.12 + Celery | Python at [services/preview/](../../services/preview/) — same story | 🟡 |
| Collaboration service | Node.js 22 (WebSocket) | Node.js at [services/collaboration/](../../services/collaboration/) — defined in compose `app` profile | 🟡 |
| Signature signer sidecar | (not in blueprint as distinct svc — blueprint has single Go signature service) | Java 17 + Gradle at [services/signature-signer/](../../services/signature-signer/) for PAdES — scaffolding only | 🟡 Above blueprint; sidecar added for DSS PAdES-B-LT |
| Frontend | React 18 + Vite + TanStack Router + TanStack Query + Zustand | React 18.2, Vite 5.0, TanStack Router 1.15, Zustand 4.5 ([web/package.json](../../web/package.json)) — TanStack **Query** present v5.17 | ✅ |
| Mobile | React Native | [mobile/](../../mobile/) directory exists — **unverified build state** | 🟡 |
| Desktop sync client (Tauri) | Rust + Tauri | ⬜ Not in repo | ⬜ |
| Browser extension | Chrome/Firefox/Edge | ⬜ Not in repo | ⬜ |

### 1.2 Data tier

| Component | Blueprint (§4.x) | On disk / running | Match? |
|---|---|---|---|
| Primary DB | PostgreSQL 16 | PostgreSQL 16-alpine, port 15432, 57 tables | ✅ |
| Search | OpenSearch 2.x | OpenSearch 2.12.0, port 9200 | ✅ |
| Vector store | Qdrant | Qdrant 1.7.4, port 6333 — **no collections created** (intelligence pipeline stubbed) | 🟡 |
| Object storage | S3 / MinIO | MinIO dev-only, port 9000; 5 buckets provisioned; prod S3 via config | ✅ |
| Cache / session | Redis 7 | Redis 7-alpine, port 6379, appendonly + LRU | ✅ |
| Event bus | NATS JetStream | NATS 2.10, port 4222, **20 streams + DLQs provisioned** | ✅ |
| Task queue | Temporal | Temporal 1.22 + temporal-ui, ports 7233/8233 | ✅ |

### 1.3 Platform services

| Component | Blueprint | On disk / running | Match? |
|---|---|---|---|
| API gateway (§3.1: "Kong/Envoy") | Kong/Envoy for rate-limiting, AuthN, routing, TLS term | ❌ **No gateway deployed.** In dev, Vite proxy routes. In prod, the [deploy/helm/vaultdms/](../../deploy/helm/vaultdms/) chart defines a generic `ingress.yaml`. Blueprint service decomposition assumes a gateway; at least 5 services (audit/notification/workflow/signature/search) trust raw `X-Tenant-ID` headers because they expect to be behind one. | ❌ Architectural gap — see Risk #1 in §4 |
| Antivirus scanning | (§5.x implied in storage flow) | ClamAV INSTREAM on port 3310, wired into storage service | ✅ |
| Key management (§8.5) | AWS KMS / HashiCorp Vault abstraction | `pkg/crypto` abstraction exists with Local/Vault/AWS KMS providers. **Dev uses `VAULTDMS_LOCAL_KEK`.** Blueprint calls for per-tenant KEK; today a **single shared KEK** is used — [docs/STATE_OF_THE_PROJECT.md:42](../STATE_OF_THE_PROJECT.md#L42). | 🟡 Abstraction ✅, per-tenant rotation ❌ |
| DLP (§8.7) | PII/PHI detection on ingest | Code at [pkg/dlp/](../../pkg/dlp/) (stub regex patterns) | 🟡 |
| Identity providers | SAML, OIDC, SCIM 2.0 | Auth service ships all three endpoints; SAML uses `math/rand` for X.509 serial — known defect | 🟡 |
| LLM routing (§6.9) | Vendor-neutral abstraction: OpenAI/Anthropic/Bedrock/local | `LiteLLM` referenced in intelligence service; provider routing plumbed but intelligence service itself not running | 🟡 |
| PDF.js viewer (§17.3) | PDF.js + annotation layer | `pdfjs-dist` 4.0 + `react-pdf` 10.4 in [web/package.json](../../web/package.json) | ✅ |
| OnlyOffice/Collabora for DOCX (§17.3) | Embedded viewer | ⬜ Not integrated | ⬜ |
| Monaco editor (§17.3 code files) | Read-only syntax highlighting | ⬜ Not imported | ⬜ |
| Cornerstone.js (§17.3 DICOM) | Medical imaging | ⬜ | ⬜ |
| hls.js (§17.3 video) | Adaptive streaming | ⬜ | ⬜ |
| Yjs CRDT (§17.4) | Real-time co-auth | ⬜ Not imported in web/ | ⬜ |
| OpenSeadragon (§17.3 large images) | Image viewer | ⬜ | ⬜ |
| i18n — 20+ locales + RTL (§17.8) | `react-intl` | `react-intl` 6.6 in deps ([web/package.json:47](../../web/package.json#L47)); **no `locales/` directory with translation files verified** | 🟡 |

### 1.4 Cross-cutting libraries on disk

| pkg/ | Purpose | Notes |
|---|---|---|
| `pkg/auth` | Session validation, JWT, bearer extraction | Used by all Go services |
| `pkg/config` | Viper + `VAULTDMS_*` env prefix | 11 services depend on it |
| `pkg/crypto` | KEK/DEK wrapping, AES-GCM | Local/Vault/KMS providers |
| `pkg/database` | pgx pooling, RLS tenant ctx, outbox publisher | Used by every service with DB |
| `pkg/events` | NATS JetStream helpers, `DefaultStreams` topology | Central source of truth for stream subjects |
| `pkg/health` | `/healthz`, `/readyz`, `/metrics` | Billing forked its own this session with a deeper probe |
| `pkg/logger` | zerolog + correlation IDs + PII scrubber | |
| `pkg/metrics` | Prometheus wrappers | |
| `pkg/middleware` | Session/CSRF/Tenant interceptors + recovery | |
| `pkg/storage` | S3 client factory | |
| `pkg/tenant` | Tenant-from-ctx helpers | |
| `pkg/tracing` | OpenTelemetry setup | Exporter unconfigured in dev |
| `pkg/validation` | Regex validators | |
| `pkg/testutil`, `pkg/testharness` | Test fixtures, compose orchestration | |

---

## 2. Feature completeness — blueprint §§5–18 vs. built

Blueprint §1.2 gives **three north-star differentiators**. Measuring each:

### 2.1 Differentiator 1: Deployment-agnostic single codebase

| Deployment target | Blueprint § | Status | Evidence |
|---|---|---|---|
| SaaS (AWS/GCP/Azure) | 14.x | 🟡 Plumbed | Helm chart at [deploy/helm/vaultdms/](../../deploy/helm/vaultdms/) (~91 templates). **Unverified end-to-end on any cloud.** |
| On-prem Kubernetes | 13.x | 🟡 Plumbed | `deploy/helm/vaultdms/values-onprem.yaml`. Unverified. |
| Air-gapped (SCIF) | 13.6 | 🟡 Plumbed | `values-airgapped.yaml` + [scripts/airgap/](../../scripts/airgap/). Unverified. |
| Docker Compose (dev) | — | ✅ | `docker compose up -d` brings up all 9 infra containers healthy this session |

### 2.2 Differentiator 2: Per-document data residency enforcement

| Capability | Blueprint §9.1 | Status | Evidence |
|---|---|---|---|
| `region_pin` column on documents | Required | ✅ | Column present on `documents` table ([services/document/migrations/000007_residency_migrations.up.sql](../../services/document/migrations/) and enforced by middleware) |
| Residency validation on write paths (DB/blob/search/cache/log/backup) | Required | 🟡 | Middleware exists; blob/search/cache enforcement unverified E2E |
| Inter-region migration workflow | §9.1 | ✅ | `residency_migrations` + `residency_migration_items` tables + `/api/v1/residency/migrations` REST endpoints (document service) |
| Audit of residency violations | §9 | ⬜ | No dedicated endpoint/dashboard |

### 2.3 Differentiator 3: Vendor-neutral LLM routing + embedded intelligence

| Capability | Blueprint §6.x | Status | Evidence |
|---|---|---|---|
| OCR pipeline (Surya / PyMuPDF) | 6.1 | 🟡 | Python code in `services/intelligence/`; service not running; no `dms.version.uploaded.v1` events being published by storage (the trigger) |
| Classification (distilbert) | 6.3 | 🟡 | Scaffolded |
| NER (spaCy) | 6.6 | 🟡 | Scaffolded |
| Embeddings → Qdrant | 6.8 | ❌ | Qdrant upsert stubbed; collections never created |
| RAG Q&A (LiteLLM) | 6.8/6.9 | ❌ | Endpoint exists in code; pipeline not wired |
| Vendor-neutral provider routing | 6.9 | 🟡 | Provider abstraction scaffolded; OpenAI/Anthropic/Bedrock/local keys in config |
| Redaction pipeline | 6.7 | 🟡 | `/api/v1/documents/{id}/redact` endpoint exists, handler logic unverified |
| Duplicate detection (SimHash) | 6.4 | 🟡 | `document_fingerprints`, `duplicate_candidates` tables exist |
| Structured extraction | 6.2 | 🟡 | `extraction_results` table exists |

### 2.4 Platform features (blueprint §§5, 7, 8, 10–12, 15)

| Domain | Capability | § | Status | Notes / Evidence |
|---|---|---|---|---|
| **Storage** | Multipart upload | 5.1 | 🟡 | gRPC endpoints exist; `dms.version.uploaded.v1` publish **missing** → halts downstream ([docs/STATE_OF_THE_PROJECT.md:29](../STATE_OF_THE_PROJECT.md#L29)) |
| Storage | Content-addressed dedup (SHA-256) | 5.3 | ✅ | `content_blobs.sha256_hash` |
| Storage | Envelope encryption per blob | 5.x | ✅ | AES-GCM, `encryption_key_id` column |
| Storage | Per-tenant KEK (BYOK) | 5.8 | ❌ | Single shared KEK |
| Storage | Thumbnails / Previews | 5.4 | 🟡 | Python preview service present; frontend viewer uses raw PDF |
| Storage | WORM / immutable storage | 5.7 | ⬜ | No WORM implementation |
| **Search** | Hybrid lexical + semantic | 7.1 | 🟡 | BM25 ✅; semantic ❌ (needs Qdrant upsert) |
| Search | Permission-filtered queries | 7.3 | ✅ | `readable_by` field in index |
| Search | Autocomplete | 7.5 | ✅ | Endpoint present |
| Search | Saved searches / alerts | 7.6 | ✅ | `saved_searches` table, REST CRUD, UI (Wave 4) |
| Search | Federated cross-tenant search | 7.7 | ⬜ | |
| **Security** | SAML 2.0 | 8.1 | 🟡 | Code ships; `math/rand` defect blocks prod |
| Security | OIDC | 8.1 | 🟡 | Code ships; IdP-verified ❌ |
| Security | SCIM 2.0 | 8.3 | 🟡 | 16 `/scim/v2/*` routes implemented; IdP integration unverified |
| Security | TOTP MFA | 8.1 | ✅ | Implemented + UI (Wave 4) |
| Security | Session cookies (HttpOnly) | 8.4 | ✅ | `dms_session` + `dms_csrf` cookies set on login |
| Security | ABAC via OPA | 8.2 | ✅ | Policy service runs embedded OPA + Redis cache |
| Security | Tamper-evident audit log | 8.8 | ✅ | SHA-256 hash chain per tenant, monthly partitions |
| Security | Audit integrity verification endpoint | 8.8 | ❌ | Code exists; route not exposed ([docs/STATE_OF_THE_PROJECT.md:21](../STATE_OF_THE_PROJECT.md#L21)) |
| Security | DLP on ingest | 8.7 | 🟡 | `pkg/dlp` stub |
| **Workflow** | Engine (Temporal) | 10.1 | 🟡 | Temporal up; `workflow_definitions` table **empty** |
| Workflow | Approval routing | 10.2 | ⬜ | No definitions → no instances |
| Workflow | Task assignment | 10.x | ⬜ | Same |
| Workflow | Co-authoring | 10.3 | ⬜ | Requires OnlyOffice/Collabora integration |
| **Collaboration** | Comments + @mentions | 10.4 | 🟡 | `comments` table exists; handler incomplete per blueprint |
| Collaboration | Real-time presence | 10.8 | 🟡 | Node WebSocket svc boots; frontend not wired |
| Collaboration | CRDT sync (Yjs) | 17.4 | ⬜ | Yjs not in web deps |
| **Signatures** | First-party (PAdES-B-LT) | 11.1 | ❌ | No PAdES library integrated (Java `signature-signer` sidecar is scaffolding) |
| Signatures | DocuSign adapter | 11.2 | 🟡 | Scaffolded |
| Signatures | Adobe Sign adapter | 11.2 | 🟡 | Scaffolded |
| Signatures | Long-term validation (LTV) | 11.3 | ❌ | Depends on PAdES |
| **Integrations** | Outbound webhooks | 12.2 | ✅ | SSRF-guarded + HMAC signing + retry |
| Integrations | Event streaming to customers | 12.3 | 🟡 | MCP SSE endpoint on connector service |
| Integrations | Salesforce OAuth | 12.4 | 🟡 | Scaffolded |
| Integrations | Microsoft 365 OAuth | 12.4 | 🟡 | Scaffolded |
| Integrations | Google Workspace OAuth | 12.4 | 🟡 | Scaffolded |
| Integrations | Public REST + OpenAPI | 12.1 | 🟡 | [docs/api/openapi.yaml](../api/openapi.yaml) exists; currency unverified |
| Integrations | gRPC internal | 12.1 | ✅ | 14 `.proto` files, 129 RPCs across 13 services |
| **Billing** | Stripe subscriptions | 14.6 | 🟡 | Webhook handler present; E2E untested |
| Billing | Usage metering (hourly) | 14.6 | ✅ | Metering cron + schema reconciled **this session** |
| Billing | Invoicing / PDF generation | 14.6 | ⬜ | Not implemented |
| Billing | Per-tenant feature flags | 14.6 | ✅ | `organizations.settings` JSONB |
| **Compliance** | GDPR data-subject export/erasure | 9.2 | 🟡 | `/api/v1/privacy/dsr/*` routes in document service |
| Compliance | Legal hold | 9.3 | 🟡 | Tables + `ApplyHold` RPC; UI admin route |
| Compliance | Retention policies | 9.4 | ✅ | `retention_policies` table + admin UI |
| Compliance | eDiscovery chain-of-custody | 9.5 | ⬜ | |
| Compliance | SOC 2 evidence automation | 9.6 | ⬜ | |
| **Observability** | Structured logs (zerolog) | 15.1 | ✅ | JSON + tenant + correlation-id in every log |
| Observability | Top-20 SLIs | 15.2 | 🟡 | `/metrics` exposed; dashboards at [deploy/monitoring/](../../deploy/monitoring/) unverified |
| Observability | OpenTelemetry tracing | 15.3 | 🟡 | `pkg/tracing` wired; exporter unconfigured in dev |
| Observability | 4-tier alerting | 15.4 | 🟡 | ServiceMonitor YAML in Helm chart; runtime unverified |
| **Frontend** | Web SPA | 17.1 | ✅ | 34 routes, runs at :3000 |
| Frontend | Design tokens + Radix | 17.2 | ✅ | Radix UI 1.x + Tailwind 3.4 |
| Frontend | WCAG 2.2 AA | 17.2 | 🟡 | `@axe-core/react` dev-dep ([web/package.json:56](../../web/package.json#L56)); compliance not asserted |
| Frontend | PDF viewer with annotations | 17.3 | 🟡 | `pdfjs-dist` + `react-pdf` imported; annotation layer unverified |
| Frontend | DOCX/PPTX/XLSX viewer | 17.3 | ⬜ | No OnlyOffice integration |
| Frontend | Large-image viewer (OpenSeadragon) | 17.3 | ⬜ | |
| Frontend | Video (hls.js) | 17.3 | ⬜ | |
| Frontend | DICOM (Cornerstone) | 17.3 | ⬜ | |
| Frontend | Monaco code viewer | 17.3 | ⬜ | |
| Frontend | Mobile (React Native) | 17.5 | 🟡 | `mobile/` dir — state unverified |
| Frontend | Desktop sync (Tauri) | 17.6 | ⬜ | Not in repo |
| Frontend | Browser extension | 17.7 | ⬜ | Not in repo |
| Frontend | i18n + RTL | 17.8 | 🟡 | `react-intl` dep; no `locales/` verified |

### 2.5 "Advanced differentiating features" (§18)

Blueprint §18 lists 10+ advanced features. Today: 0 clearly shipped, 2–3 scaffolded. Examples flagged in blueprint but not in code today:
- Semantic similarity search across tenants (⬜)
- Workflow templates marketplace (⬜)
- AI-assisted bulk tagging (⬜)
- Natural-language admin console (⬜)
- Auto-redaction ML pipeline (🟡 — route exists, model unwired)

---

## 3. Blueprint anti-features — compliance check

Blueprint §1.3 explicitly says NOT to build these. Confirming we haven't:

| Anti-feature | In repo? |
|---|---|
| Consumer tier / personal storage | ❌ Not present ✅ |
| Office suite (native editing) | ❌ Not present ✅ |
| Email client (Outlook/Gmail replacement) | ❌ Not present ✅ |
| Notion-style DB / spreadsheet mode | ❌ Not present ✅ |
| IE / legacy browser support | ❌ Not present ✅ |

**Good:** the project has held the line on scope.

---

## 4. Delta summary

### 4.1 Strong (≥90% aligned with blueprint)
- Core backend language choice (Go) ✅
- Database + event bus + object + vector stores ✅
- Authentication primitives (password + TOTP + session + CSRF) ✅
- OPA-backed authorization ✅
- Outbox pattern for transactional events ✅
- Residency model (per-document `region_pin`) ✅
- Audit hash-chain ✅
- Webhook delivery with HMAC+SSRF guard ✅
- Usage metering ✅
- gRPC + protobuf contract set ✅

### 4.2 Partial (40–80% aligned)
- OCR/NER/embeddings pipeline — code scaffolded, **pipeline broken** (storage publish missing)
- Hybrid search — lexical yes, semantic no
- Signatures — REST skeleton yes, actual signing no
- Workflow engine — Temporal up, zero definitions
- Deployment — compose yes, Helm chart present but untested on a cluster
- Mobile — directory exists, build state unknown
- i18n — lib in deps, translations not committed

### 4.3 Missing (<20% aligned)
- **API gateway layer (Kong/Envoy)** — the blueprint assumes this sits in front of services; today nothing does. This is the biggest *architectural* gap and caused the "Vite proxy routes everything to auth" incident fixed earlier today.
- Desktop sync client (Tauri) — not in repo
- Browser extension — not in repo
- DOCX/DICOM/video viewers — no client-side libs in web deps
- Yjs CRDT for co-auth — not imported
- Per-tenant KEK rotation — single shared KEK
- PAdES-B-LT signing — no library integrated
- SOC 2 evidence automation — no code path
- Federated cross-tenant search — no code path
- AI-assisted bulk operations — no code path

### 4.4 Above blueprint (scope additions)
- `signature-signer` Java sidecar (separate Go/Java split — more decoupled than blueprint's monolithic signature service, probably a correct call for PAdES)
- `dms-admin` CLI with `nats` subcommand + preflight-coverage tool (added today) — blueprint doesn't call for this but it's a quality-of-life win

---

## 5. Verdict

**Architectural fidelity to the blueprint: moderate.** The service decomposition, data architecture, event bus, security primitives, and residency model match the blueprint's prescriptions. Where reality diverges, it's mostly in:

1. **No API gateway in front of services** — the most important gap, both operationally (causes routing bugs) and security-wise (5 services trust raw tenant headers because they assume a gateway authenticated the caller).
2. **Intelligence pipeline is plumbed but not flowing** — code exists in Python + Qdrant + LiteLLM, but the storage service doesn't emit the kick-off event, so nothing downstream ever runs. **This single bug blocks half the blueprint's differentiator #3.**
3. **Signature + Workflow + eDiscovery / SOC 2 evidence** — product surfaces exist in the UI, backend is empty (no PAdES lib, no workflow defs, no evidence-automation code).
4. **Client surfaces beyond web** — mobile unverified, desktop + extension absent.

**Today's session additions** (billing schema reconcile, audit/connector subscribe fixes, NATS subject preflight, Vite proxy expansion, auth rehydration) move several ✅→ from 🟡 but don't change the macro picture.

**Honest one-liner:** the platform **behind** the UI is structurally sound but missing its front door (gateway) and its brain (intelligence pipeline); the **client side** is ~⅓ of blueprint scope (web yes, mobile/desktop/extension no).
