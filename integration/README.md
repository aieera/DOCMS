# ERP ↔ SeDoc Document Integration

A **separate, self-contained** product that mirrors ERP documents into the SeDoc
DMS (one-way, ERP → DMS) and gives ERP users a per-customer file explorer. SeDoc
is never the source of truth; nothing is written back to the ERP.

Built against [`INTEGRATION.md`](../INTEGRATION.md) and
[`proto/gen/openapi/sedoc.swagger.json`](../proto/gen/openapi/sedoc.swagger.json)
as the API contract.

> **Connecting a real ERP?** Section B below is currently the mock
> (`cmd/mockerp`). [`ERP-CONTRACT.md`](ERP-CONTRACT.md) is the exact contract the
> real ERP must implement (the 4 events, render/authz/listing endpoints,
> delivery guarantees) and the config-only cutover steps + smoke test.

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

### Backfill (onboarding existing data)
Live events only sync net-new changes; to onboard customers/documents that
existed *before* the integration was switched on, run a **backfill**. It
enumerates existing ERP data from a source, synthesizes the canonical events, and
feeds them through the **same idempotent handlers** (`ensureCustomer` →
`document.committed`/`attachment.uploaded`) with **stable** `erp_event_id`s — so a
re-run replays the same SeDoc Idempotency-Keys and `(external_id, checksum)`
upserts, creating **nothing new**. All writes flow through the shared Prompt-1
rate limiter, so a backfill paces itself under 600/min and shares that budget with
the live worker.

Two ways to drive it:
- **CLI** (`cmd/backfill`) — one-shot, ops-run:
  ```
  go run ./cmd/backfill                              # full backfill from the ERP listing API
  go run ./cmd/backfill -customers CUST-1,CUST-2     # scope to specific customers
  go run ./cmd/backfill -source ndjson -file inv.ndjson   # from an NDJSON inventory
  ```
- **BFF** — `POST /files/sync/backfill` enqueues a `pending` run (status returned
  immediately as a `run_id`); the **worker poller** claims and executes it
  in-process under the shared limiter. Progress + per-item failures are queryable
  via `GET /files/sync/backfill[/{run_id}]`.

Runs are tracked in `backfill_runs` (status, total/processed/failed,
started/finished) with one `backfill_failures` row per failed item. Sources
implement `erp.Lister`: the ERP HTTP listing (`GET /erp/customers`,
`GET /erp/customers/{ref}/documents`) or an NDJSON file (one
`{"type":"customer|document|attachment",…}` record per line; document bytes are
still resolved via the ERP render endpoint).

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
| `SEDOC_INTEGRATION_RATE_PER_MIN` | Proactive SeDoc write throttle, requests/min (default 600 = SeDoc's `:upsert`/`/ingest` ceiling) |
| `SEDOC_INTEGRATION_RATE_BURST` | Token-bucket burst above the steady rate (default 20) |
| `SEDOC_INTEGRATION_BACKFILL_CONCURRENCY` | Customers processed in parallel during a backfill (default 4); SeDoc write rate is still capped by the shared limiter |
| `SEDOC_INTEGRATION_WORKER_URL` | BFF only: integration worker base URL for the `/files/sync/metrics` passthrough (default `http://localhost:8090`) |
| `ERP_BASE_URL` | worker/bff/backfill: ERP boundary (render/authz/listing). Default `http://localhost:8095` (mock); point at the live ERP to cut over |
| `ERP_API_TOKEN` | worker/bff/backfill: optional bearer presented to the ERP's endpoints (empty = none, for the mock or a network-isolated ERP) |

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
| `POST /files/customers/{ref}/upload` | customer | streaming multipart → 3-step upload → `/ingest` (browser sends `sha256`+`size` first → bytes stream straight to the presigned PUT, flat memory, no size cap) |
| `POST /files/search` | customer | body `{customer_ref, q}` (query in the body, not the URL → not logged); scoped: hits outside the subtree are dropped |
| `GET /files/documents/{id}` | doc→folder→customer | detail + version history |
| `GET /files/documents/{id}/download` | doc→folder→customer | streamed (flat memory; storage key never exposed) |
| `POST /files/documents/{id}/versions/{vid}/restore` | doc→folder→customer | |
| `GET /files/review-queue` · `/{id}` · `POST /{id}/resolve` | admin | passthrough to SeDoc review queue |
| `GET /files/sync/log` · `POST /files/sync/retry/{id}` | admin | worker sync_log + DLQ retry |
| `GET /files/sync/metrics` | admin | passthrough to the worker's runtime metrics (throughput, backlog, DLQ, limiter state) |
| `POST /files/sync/backfill` | admin | enqueue a backfill run (optional `{customer_refs:[…]}` scope) → `{run_id}`; the worker poller executes it |
| `GET /files/sync/backfill` · `/{run_id}` | admin | run list / one run with progress + per-item failures |

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
  progress bar and the routing outcome. Scoped to one customer via a `customerRef`
  prop — no routing/auth assumptions — so it's **embeddable** (see below).
- **[E] Review Queue** (`/review`, admin): keyset-paginated ("Load more") with a
  pending/resolved/rejected status filter. Each row shows confidence + reason + a
  lazy OCR preview and (when pending) New-version / New-document / Reject resolve
  actions; resolved/rejected rows show the audit trail (who actioned it, when,
  resulting document). Concurrent resolution is handled cleanly — a 409 surfaces
  as an info toast and refreshes the list rather than a red error.
- **[F] Sync Dashboard** (`/sync`, admin): live operational metrics — events/min
  throughput (sparkline), queue backlog, DLQ size, observed SeDoc 429s, and the
  shared rate-limiter's token/throttle-wait state — plus a **backfill** control
  panel (start a run, watch progress + per-item failures) and the `sync_log` /
  DLQ table with status filter + Retry. Metrics come from the worker via the
  `GET /files/sync/metrics` BFF passthrough.

Identity is sent via `X-ERP-User` / `X-ERP-Admin`; a dev control in the header
stands in for the ERP's SSO (the ERP auth proxy injects these in production).
Errors surface a copyable `correlation_id`. Loading uses skeletons; status uses
color **and** text.

```
cd integration/web && npm install
VITE_BFF_URL=http://localhost:8091 npm run dev   # dev server on :5180, proxies /files → BFF
npm run build                                     # tsc --noEmit && vite build → dist/ (SPA + embed.html)
npm run build:widget                              # → dist-widget/ (mountFileExplorer JS + CSS)
```

### Embedding in the ERP
The explorer mounts inside the ERP's customer record without becoming the ERP.
All config (BFF base URL, how the ERP user's token is obtained, which customer)
is injected — there's no hardcoded routing or auth. Three ways to ship it:

1. **JS widget (preferred)** — `npm run build:widget` emits a self-contained
   `dist-widget/sedoc-file-explorer.es.js` (+ `.umd.js`, `style.css`). The ERP
   drops it into its customer page:
   ```js
   import { mountFileExplorer } from "sedoc-file-explorer";
   import "sedoc-file-explorer/style.css";
   const handle = mountFileExplorer(document.getElementById("files"), {
     customerRef: "CUST-1",
     apiBase: "https://erp.example.com",      // Files BFF base ("" = same origin)
     getAuthToken: () => erp.getUserToken(),   // → Authorization: Bearer <token>
   });
   // later, when leaving the customer record:
   handle.unmount();
   ```
2. **iframe fallback** — `dist/embed.html` reads `customerRef` + `apiBase` from the
   URL; the host supplies the token over `postMessage` (never in the URL):
   ```html
   <iframe id="f" src=".../embed.html?customerRef=CUST-1&apiBase=https://erp.example.com"></iframe>
   <script>
     addEventListener("message", (e) => {
       if (e.data?.type === "sedoc-files-ready")
         f.contentWindow.postMessage({ type: "sedoc-files-token", token: erp.getUserToken() }, "*");
     });
   </script>
   ```
3. **Standalone SPA** — `npm run dev` / `dist/index.html`, with a dev-identity
   control standing in for the ERP's SSO. Unchanged, for local development.

`getAuthToken` returns the ERP user's token, sent as `Authorization: Bearer …`;
the ERP's auth proxy maps it to the `X-ERP-User`/`X-ERP-Admin` the BFF trusts.
When omitted (standalone dev only) the explorer falls back to dev identity headers
from localStorage.

## Remaining (build prompt §11.9 — hardening)
a11y audit, load-test the tree at ~100k customers, and a security review of the
key boundary (the BFF authz tests cover the core: no cross-customer reads, key
never reaches the browser).
