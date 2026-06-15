# ERP ↔ SeDoc Document Integration

A **separate, self-contained** product that mirrors ERP documents into the SeDoc
DMS (one-way, ERP → DMS) and gives ERP users a per-customer file explorer. SeDoc
is never the source of truth; nothing is written back to the ERP.

Built against [`INTEGRATION.md`](../INTEGRATION.md) and
[`proto/gen/openapi/sedoc.swagger.json`](../proto/gen/openapi/sedoc.swagger.json)
as the API contract.

```
ERP (events + render API + customer authz)         [B] mock under cmd/mockerp
   │  canonical events (webhook, at-least-once)
   ▼
[A] Integration Worker ───────────────► SeDoc DMS   (upsert / ingest / bulk)
   │  owns sync state (Postgres)         ▲
   │                                     │ server-side, holds the API key
ERP user ─► [D/E/F] UI ─► [C] Files BFF ─┘
```

## Components

| # | Component | Status | Path |
|---|---|---|---|
| A | Integration Worker (events → SeDoc) | **built** | `cmd/worker`, `internal/{sedoc,sync,store,erp,bucket}` |
| B | Mock ERP (emitter + render + authz) | **built** | `cmd/mockerp` |
| C | Files BFF (key-holding API for the UI) | **built** | `cmd/bff`, `internal/bff` |
| D | Customer File Explorer UI | **built** | `web/src/pages/FileExplorer.tsx` |
| E | Review Queue UI | **built** | `web/src/pages/ReviewQueue.tsx` |
| F | Sync Dashboard UI | **built** | `web/src/pages/SyncDashboard.tsx` |

All six components are built. A (mock) ERP event → the worker provisions folders
+ uploads bytes + `:upsert` / `/ingest` on SeDoc (idempotent, retry/DLQ); the
**Files BFF** serves an authorization-enforced, key-safe API over SeDoc; and the
**React UIs** give ERP users a per-customer file explorer plus admin triage +
sync monitoring.

## [A] Worker design

- **Ingress**: `POST /webhooks/erp` receives the 4 canonical events. Each MUST
  carry a stable `erp_event_id`. The handler validates, writes a `sync_log` row
  keyed by `erp_event_id` (unique → at-least-once dedup), enqueues a job, and
  returns 202 fast. SeDoc is never called inline with the request.
- **Queue**: DB-backed (`sync_log.status = pending`), polled by a worker loop
  with bounded concurrency, exponential backoff, and a dead-letter terminal
  state. No external broker needed.
- **State** (`internal/store`, Postgres):
  - `customer_map` — `customer_ref` → bucket/main/subfolder ids (+ name).
  - `sync_log` — one row per `erp_event_id`; **its `id` is the SeDoc
    `Idempotency-Key`** (stable, never random).
  - `ingestion_tracking` — `ingestion_item_id` → status/customer (for [E]).
- **SeDoc client** (`internal/sedoc`): Bearer-key auth, idempotency-key on every
  write, the 3-step upload (initiate → PUT → complete, skipping PUT on dedup),
  `:upsert`, `/ingest`, and **error classification** mapping SeDoc's documented
  codes to retryable vs terminal.
- **Sharding** (`internal/bucket`): `customer_ref` → a deterministic bucket so no
  parent folder holds ~100k direct children (mirrors SeDoc's own WS6 scheme).

### Event → action
| Event | Action |
|---|---|
| `customer.created` | ensure bucket folder → create main folder (`name`) → create 6 subfolders (quote/po/so/do/invoice/attachments) → persist `customer_map` |
| `customer.updated` | PATCH main folder name (link-stable) |
| `document.committed` | resolve subfolder by `doc_type` → fetch bytes → upload → `:upsert` (`external_id = <type>-<doc_number>`) |
| `attachment.uploaded` | resolve attachments folder → upload → `/ingest` (system OCRs + routes) |

### Error classification (per INTEGRATION.md §2)
- **Retryable** (backoff → DLQ): `409 IDEMPOTENCY_IN_PROGRESS`, `503
  IDEMPOTENCY_STORE_UNAVAILABLE`, `429` (honor `Retry-After`), network/5xx.
- **Terminal** (DLQ + alert): `400 VALIDATION`/`IDEMPOTENCY_KEY_*`, `403`,
  `422 IDEMPOTENCY_KEY_REUSED`.

## Demo (one command)

With a SeDoc stack running (parent `make docker-up`) plus an API key + workspace
id in `.env` (see `.env.example`):

```
cd integration && make docker-up     # postgres + worker + bff + mockerp + web
# → web UI on http://localhost:5180, BFF :8091, worker webhook :8090, mock ERP :8095
```

Then drive events through the mock ERP (its `emit` CLI posts to the worker):

```
go run ./cmd/mockerp emit customer.created  --customer CUST-1 --name "Acme"
go run ./cmd/mockerp emit document.committed --customer CUST-1 --doc-type invoice --doc-number 188
```

Open `http://localhost:5180`, set a dev user (e.g. `alice`) in the header, open
customer `CUST-1`, and the Invoices folder shows v1.

## Running locally (without Docker)

```
export SEDOC_INTEGRATION_DB_URL=postgres://sedoc:devpassword@localhost:15432/sedoc_integration?sslmode=disable
export SEDOC_BASE_URL=http://localhost:8080/api/v1      # SeDoc document gateway
export SEDOC_SEARCH_URL=http://localhost:8086/api/v1    # SeDoc search service (BFF)
export SEDOC_API_KEY=vdms_...                           # scopes: documents:read/write, upload
export SEDOC_WORKSPACE_ID=<workspace uuid>
make run-mockerp   # ERP render + authz on :8095
make run-worker    # webhook ingress + job loop on :8090
make run-bff       # Files BFF on :8091
make web-dev       # Vite dev server on :5180 (proxies /files → BFF)
```

## Tests

```
make test               # unit (bucketing, sedoc client, BFF auth gate)
make test-integration   # testcontainers: pipeline, BFF authz boundary, full e2e (e2e/)
make web-build          # tsc --noEmit && vite build
```

## Config (env)
| Var | Meaning |
|---|---|
| `SEDOC_INTEGRATION_DB_URL` | Postgres for the worker's own state |
| `SEDOC_BASE_URL` | SeDoc document gateway base (`…/api/v1`) |
| `SEDOC_API_KEY` | Bearer API key (kept server-side only) |
| `SEDOC_WORKSPACE_ID` | Target workspace for provisioned folders |
| `SEDOC_INTEGRATION_BUCKETS` | Shard count for customer bucketing (default 256) |
| `SEDOC_INTEGRATION_HTTP_PORT` | Webhook ingress port (default 8090) |
| `SEDOC_INTEGRATION_CONCURRENCY` | Worker-loop concurrency (default 8) |

## [C] Files BFF design
Browser → BFF → SeDoc. The BFF holds the API key (never sent to the client) and
gates **every** route: authenticate the ERP user (`X-ERP-User`, stamped upstream
by the ERP auth proxy) → check per-customer authorization via the ERP
(`erp.Authorizer`) → proxy to SeDoc scoped to that customer's folder subtree.
Admin routes additionally require `X-ERP-Admin: true`. SeDoc's `correlation_id`
is surfaced on errors for support.

| Route | Auth | Notes |
|---|---|---|
| `GET /files/customers/{ref}/tree` | customer | main folder + 6 subfolders with doc counts |
| `GET /files/customers/{ref}/folders/{folder_id}/documents?cursor=` | customer + folder∈subtree | keyset-paginated |
| `POST /files/customers/{ref}/upload` | customer | multipart → 3-step upload → `/ingest` |
| `GET /files/search?customer_ref=&q=` | customer | scoped: hits outside the subtree are dropped |
| `GET /files/documents/{id}` | doc→folder→customer | detail + version history |
| `GET /files/documents/{id}/download` | doc→folder→customer | streamed (flat memory; storage key never exposed) |
| `POST /files/documents/{id}/versions/{vid}/restore` | doc→folder→customer | |
| `GET /files/review-queue` · `/{id}` · `POST /{id}/resolve` | admin | passthrough to SeDoc review queue |
| `GET /files/sync/log` · `POST /files/sync/retry/{id}` | admin | worker sync_log + DLQ retry |

Document-id routes resolve the doc's folder → owning customer
(`store.CustomerByFolder`) and re-run the authz gate, so a user can never read a
document outside a customer they're entitled to. Run: `go run ./integration/cmd/bff`
(env: `SEDOC_BFF_HTTP_PORT`=8091, `SEDOC_SEARCH_URL`, plus the worker's SeDoc/DB
vars).

## [D/E/F] Web UIs (`web/`)
React 18 + Vite + TS + TanStack Query + Tailwind — a lean, self-contained SPA
(not the full SeDoc `web/` design system) that talks **only** to the BFF.

- **[D] File Explorer** (`/customers/:ref`): accessible folder tree (`role="tree"`,
  arrow-key nav) → document list with status + sync badges → detail panel with
  download + version history (restore) → drag-drop upload into Attachments with a
  progress bar and the routing outcome.
- **[E] Review Queue** (`/review`, admin): pending items with OCR excerpt +
  confidence + reason, and New-version / New-document / Reject resolve actions.
- **[F] Sync Dashboard** (`/sync`, admin): live `sync_log` table with status
  filter, the DLQ, last error + `correlation_id`, and a Retry action.

Identity is sent via `X-ERP-User` / `X-ERP-Admin`; a dev control in the header
stands in for the ERP's SSO (the ERP auth proxy injects these in production).
Errors surface a copyable `correlation_id`. Loading uses skeletons; status uses
color **and** text.

```
cd integration/web && npm install
VITE_BFF_URL=http://localhost:8091 npm run dev   # dev server on :5180, proxies /files → BFF
npm run build                                     # tsc --noEmit && vite build → dist/
```

## Remaining (build prompt §11.9 — hardening)
a11y audit, load-test the tree at ~100k customers, and a security review of the
key boundary (the BFF authz tests cover the core: no cross-customer reads, key
never reaches the browser).
