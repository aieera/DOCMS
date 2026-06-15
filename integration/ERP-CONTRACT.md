# ERP ↔ SeDoc Integration — ERP Contract & Cutover

This is the contract the **real ERP** must satisfy to replace the mock
(`cmd/mockerp`) and the steps to switch the integration over to it. When the ERP
implements everything here, cutover is **config-only** — no integration code
changes (see [§6](#6-config-switch-mock--real)).

The integration is **one-way (ERP → SeDoc)**. SeDoc is never the source of truth;
nothing is written back to the ERP. The ERP has two responsibilities:

1. **Push** the four canonical events to the integration worker's webhook
   (at-least-once). — [§2](#2-webhook-events-erp--worker)
2. **Expose** three pull endpoints the integration calls: document **render**,
   customer **authorization**, and the backfill **listing**. — [§3](#3-render-endpoint)–[§5](#5-listing-endpoints-backfill-only)

```
ERP ──(4 webhook events, at-least-once)──▶  Worker  POST /webhooks/erp
ERP ◀──(GET render / authz / listing)────  Worker + BFF   (ERP_BASE_URL)
```

---

## 1. Conventions

- All payloads are JSON; UTF-8; `Content-Type: application/json`.
- Timestamps (where present) are RFC 3339 / ISO 8601 UTC.
- `customer_ref`, `doc_number`, `file_ref` are **opaque ERP identifiers** —
  stable, unique within the ERP, safe to put in a URL path (they are not secrets;
  free-text/search terms are the only thing kept out of URLs, elsewhere).
- The integration reaches the ERP at **`ERP_BASE_URL`** and (optionally)
  authenticates with **`ERP_API_TOKEN`** (`Authorization: Bearer <token>`) — see
  [§6](#6-config-switch-mock--real).

---

## 2. Webhook events (ERP → worker)

**Endpoint (ERP is configured with this):** `POST {WORKER_WEBHOOK_URL}/webhooks/erp`
(default worker is `:8090`, so `http://worker:8090/webhooks/erp`).

**Response:** `202 Accepted` with `{"event_id": "...", "duplicate": false}`. The
worker validates, dedups, enqueues, and returns immediately — it **never** blocks
on SeDoc. Any non-2xx (incl. 5xx) means *not durably accepted* → the ERP **must
retry**.

### 2.1 Delivery guarantee — `erp_event_id` + at-least-once

- Every event MUST carry a **stable, unique `erp_event_id`** per *logical* event.
  The worker stores it under a UNIQUE constraint and dedups on it: the same id
  re-delivered is acknowledged and dropped (no duplicate work).
- The ERP MUST deliver **at-least-once**: emit from a **transactional outbox**
  (write the event in the same DB transaction as the business change) and a
  publisher that **retries with backoff** until it gets a 202. At-least-once +
  the stable id = exactly-once effect.
- **Stability matters:** re-sending the *same* logical change with the *same* id
  is a safe no-op; sending it with a *different* id creates duplicate work.
  Derive the id deterministically from the business change, e.g.
  `evt-<entity>-<id>-<transition>` (`evt-invoice-188-confirmed`), not a random
  UUID per send-attempt.
- Ordering is **not** required. If a `document.committed` arrives before its
  `customer.created`, the worker provisions the customer on first sighting.

### 2.2 Common envelope

| Field | Type | Required | Notes |
|---|---|---|---|
| `erp_event_id` | string | **yes** | stable per logical event (see §2.1) |
| `kind` | string | **yes** | one of the four kinds below |

### 2.3 The four events

#### `customer.created` — a customer record is created
| Field | Type | Required | Notes |
|---|---|---|---|
| `customer_ref` | string | **yes** | the ERP customer id |
| `name` | string | no | display name (used to name the main folder) |

Fires when a new customer is created. The worker provisions a bucketed main
folder + 6 subfolders (Quotes / POs / SOs / DOs / Invoices / Attachments).

```json
{"erp_event_id":"evt-cust-CUST-1-created","kind":"customer.created","customer_ref":"CUST-1","name":"Acme Corp"}
```

#### `customer.updated` — the customer is renamed
| Field | Type | Required | Notes |
|---|---|---|---|
| `customer_ref` | string | **yes** | |
| `name` | string | **yes** | the new display name |

Fires on a rename. The worker renames the **existing** main folder (link-stable —
no new folder, document links unaffected).

```json
{"erp_event_id":"evt-cust-CUST-1-rename-3","kind":"customer.updated","customer_ref":"CUST-1","name":"Acme Corporation"}
```

#### `document.committed` — a business document reaches a fileable state
| Field | Type | Required | Notes |
|---|---|---|---|
| `customer_ref` | string | **yes** | |
| `doc_type` | string | **yes** | `quote` \| `po` \| `so` \| `do` \| `invoice` |
| `doc_number` | string | **yes** | the ERP document number |
| `status` | string | no | ERP status, e.g. `sent`, `confirmed` (stored as the version's change summary) |
| `file_ref` | string | no\* | the id the worker renders to bytes via [§3](#3-render-endpoint) |
| `bytes` | string (base64) | no\* | inline bytes instead of `file_ref` |
| `filename` | string | no | defaults to `<doc_type>-<doc_number>.pdf` |
| `mime` | string | no | defaults to `application/pdf` |

\* Provide **exactly one** bytes source: `file_ref` (preferred — keeps webhooks
small, lets the worker stream) or inline `bytes`.

Fires when a quote/PO/SO/DO/invoice is **created or marked sent/confirmed** — i.e.
each transition that should produce a stored version. The worker upserts on a
stable key `external_id = "<doc_type>-<doc_number>"`:
- new key → new document (v1);
- same key, **changed** bytes → a new **version**;
- same key, **identical** bytes → **no-op** (idempotent).

So it's safe (and expected) to emit on every fileable transition; identical
re-emits don't create noise.

```json
{"erp_event_id":"evt-invoice-188-confirmed","kind":"document.committed",
 "customer_ref":"CUST-1","doc_type":"invoice","doc_number":"188",
 "status":"confirmed","file_ref":"erpdoc-998877","mime":"application/pdf"}
```

#### `attachment.uploaded` — a user attaches a file
| Field | Type | Required | Notes |
|---|---|---|---|
| `customer_ref` | string | **yes** | |
| `file_ref` | string | no\* | rendered via [§3](#3-render-endpoint) |
| `bytes` | string (base64) | no\* | inline instead of `file_ref` |
| `filename` | string | no | |
| `mime` | string | no | defaults to `application/pdf` |
| `doc_type` | string | no | optional classification hint |

\* Exactly one bytes source, as above.

Fires when a user uploads an attachment. The worker stages it via SeDoc `/ingest`
(SeDoc OCRs + auto-routes; low-confidence reads land in the review queue).

```json
{"erp_event_id":"evt-attach-55012","kind":"attachment.uploaded",
 "customer_ref":"CUST-1","file_ref":"erpfile-55012","filename":"signed-contract.pdf"}
```

### 2.4 Validation (worker-side, for reference)
The worker rejects (terminal, no retry) an event missing `erp_event_id`, with an
unknown `kind`, or missing the per-kind required fields above. A document/
attachment with neither `file_ref` nor `bytes`, and no render source, fails the
job (visible in the Sync Dashboard DLQ).

---

## 3. Render endpoint

**`GET {ERP_BASE_URL}/erp/documents/{file_ref}/pdf`** — called by the worker (and
the BFF for backfill) to fetch a document's bytes when an event carries a
`file_ref`.

- `{file_ref}` is the value from a `document.committed` / `attachment.uploaded`
  event (URL-path-escaped).
- **200** → the raw bytes in the body; `Content-Type` honored (defaults to
  `application/pdf`). Bodies stream; up to 256 MiB are read.
- Non-2xx → treated as a **retryable** error (the worker backs off, then
  dead-letters). 404 for an unknown `file_ref` is acceptable (→ retried then DLQ).
- MUST be **stable/idempotent**: the same `file_ref` returns the same bytes.
- Auth: if `ERP_API_TOKEN` is set, the request carries
  `Authorization: Bearer <token>` (see [§6](#6-config-switch-mock--real)).

---

## 4. Authorization endpoint

**`GET {ERP_BASE_URL}/erp/authz/customer/{customer_ref}?user={erp_user_id}`** —
called by the **BFF on every per-customer request** (and document-id requests,
which resolve the doc → folder → owning customer first). This is the hot path
that keeps one ERP user from seeing another customer's files.

- `{erp_user_id}` is the authenticated ERP user (the ERP's auth proxy stamps it
  as `X-ERP-User` on requests into the BFF).
- **200** → allowed. **403** → denied. **Any other status** → the BFF **fails
  closed** (the user gets an error, never access).
- MUST be fast and consistent (it runs on every read).
- Auth: `Authorization: Bearer <ERP_API_TOKEN>` when configured.

---

## 5. Listing endpoints (backfill only)

Used **once** by the backfill (`cmd/backfill` / the Sync Dashboard "Start
backfill") to onboard data that existed before cutover. Not on any hot path.

**`GET {ERP_BASE_URL}/erp/customers`**
```json
{"customers":[{"customer_ref":"CUST-1","name":"Acme Corp"}, ...]}
```

**`GET {ERP_BASE_URL}/erp/customers/{customer_ref}/documents`**
```json
{"documents":[
  {"kind":"document","doc_type":"invoice","doc_number":"188","status":"confirmed","file_ref":"erpdoc-998877","mime":"application/pdf"},
  {"kind":"attachment","filename":"contract.pdf","file_ref":"erpfile-55012","mime":"application/pdf"}
]}
```
- `kind`: `document` (→ committed/upsert) | `attachment` (→ `/ingest`).
- `file_ref` resolves to bytes via [§3](#3-render-endpoint).
- The current backfill reads the **full** list per call. For very large ERPs,
  paginated variants would be a follow-up (the integration would need cursor
  support — flagged as the one listing-side enhancement that's not yet built).

---

## 6. Config switch (mock → real)

Cutover is **config-only**. The same `erp.HTTPClient` and handlers talk to the
mock or the real ERP; only env changes.

| Var | Service(s) | Mock value | Real value |
|---|---|---|---|
| `ERP_BASE_URL` | worker, bff, backfill | `http://localhost:8095` (or `mockerp:8095`) | the real ERP base, e.g. `https://erp.staging.example.com` |
| `ERP_API_TOKEN` | worker, bff, backfill | *(unset — mock needs no auth)* | bearer token the ERP requires on render/authz/listing |

Plus, on the **ERP side**: configure the ERP's outbox/webhook publisher to POST
events to the worker's `{WORKER_WEBHOOK_URL}/webhooks/erp`.

That's the whole switch — no code change. (If the ERP authenticates these pulls
some other way than a bearer token, e.g. mTLS, terminate it at the network/proxy
layer so the app contract is unchanged.)

---

## 7. Cutover checklist (staging → production)

1. **ERP implements** §2–§5 and points its event publisher at the staging
   worker's `/webhooks/erp`.
2. **Provision a staging SeDoc tenant**: API key (scopes `documents:read`,
   `documents:write`, `upload`) + a workspace id.
3. **Set staging env** for worker + bff + backfill: `SEDOC_BASE_URL`,
   `SEDOC_API_KEY`, `SEDOC_WORKSPACE_ID`, `ERP_BASE_URL=<real staging ERP>`,
   `ERP_API_TOKEN=<token>`, `SEDOC_INTEGRATION_WORKER_URL` (bff).
4. **Deploy** worker + bff against staging; confirm `/healthz` and that the
   worker's `/webhooks/erp` is reachable from the ERP.
5. **Run the smoke test** ([§8](#8-cutover-smoke-test)) — it must pass end-to-end.
6. **Backfill** (optional, if onboarding pre-existing data): trigger from the Sync
   Dashboard or `cmd/backfill`; watch progress + per-item failures.
7. **Soak**: drive real ERP activity in staging; watch the Sync Dashboard
   (throughput, backlog, DLQ should stay ~0, limiter 429s = 0).
8. **Production**: repeat 2–7 against the production tenant + ERP. Keep the mock
   out of the production deploy.

Rollback: point `ERP_BASE_URL` back at the mock (or disable the ERP's publisher);
no data is written back to the ERP, so there's nothing to unwind there.

---

## 8. Cutover smoke test

[`scripts/cutover-smoke.sh`](scripts/cutover-smoke.sh) exercises the full path
against a live ERP + staging tenant: it POSTs **all four event types** to the
worker webhook (a `document.committed` carrying a real `file_ref` so the **live
ERP render endpoint** is exercised), waits for the worker to provision + file,
then through the BFF verifies the **customer tree**, a **document download**, and
an **authz denial** for an unauthorized user (hitting the **live ERP authz
endpoint**).

```bash
WORKER_WEBHOOK_URL=https://worker.staging.example.com \
BFF_URL=https://files.staging.example.com \
AUTH_USER=alice UNAUTH_USER=mallory \
DOC_FILE_REF=erpdoc-998877 ATTACH_FILE_REF=erpfile-55012 \
  ./scripts/cutover-smoke.sh
```

It prints PASS/FAIL per step and exits non-zero on any failure. See the script
header for all env vars. Requires `curl` + `jq`.
