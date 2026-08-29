# SeDoc ↔ ERP Integration Guide

**Audience:** engineers integrating an external ERP (e.g. Raabyt) with SeDoc.
**Scope:** this document describes the **SeDoc side only** — the API surface the ERP
calls and the webhook contracts it consumes. The ERP-side sync worker, sync log, and UI
(status panels, admin pages) live in the ERP repository and are out of scope here.

---

## 1. Architecture — two directions

```
                         ┌───────────────────────────── SeDoc ─────────────────────────────┐
   ERP (Node/Sequelize)  │                                                                     │
   ──────────────────    │   gateway (Kong) ──► document service ──► storage / policy / …      │
   sync worker  ─────────┼──►  REST + Bearer vdms_ API key   (OUTBOUND: ERP pushes/pulls)      │
                         │                                                                     │
   webhook receiver ◄────┼───  connector service  ◄── NATS JetStream domain events            │
                         │     (INBOUND: SeDoc pushes events to the ERP, HMAC-signed)       │
                         └─────────────────────────────────────────────────────────────────────┘
```

Two independent channels:

| Direction | Transport | Auth | Used for |
|---|---|---|---|
| **Outbound** ERP → DMS | REST over the gateway | Bearer `vdms_…` API key (per-route scope) | Push documents/versions, ensure folders, read state, resync |
| **Inbound** DMS → ERP | HTTP POST (webhook) | HMAC signature the ERP verifies | Notify ERP of uploads, lifecycle changes, deletes, signature completion |

The two channels are decoupled by design: the ERP push path never blocks on event
delivery, and event delivery is at-least-once via the transactional outbox + connector
delivery worker.

---

## 2. Authentication

### 2.1 API keys (outbound, ERP → DMS)

Service-to-service calls use a **Bearer API key** issued per tenant in SeDoc:

```
Authorization: Bearer vdms_<random>
```

Routes that accept a key use the `SessionOrAPIKey` middleware
([pkg/middleware/session_or_apikey.go](../../pkg/middleware/session_or_apikey.go)):
dispatch is purely by the `Authorization` header — `Bearer vdms_…` takes the API-key path
(enforces the route's required scope, stamps an `api_key` identity), anything else takes the
session-cookie path. Both stamp the same `tenant + user` identity, so RLS tenant isolation
and policy checks behave identically regardless of which path ran.

**Scopes** (each route demands exactly one):

| Scope | Grants |
|---|---|
| `upload` | the storage initiate → complete → abort → download flow |
| `documents:write` | create document, create version, create folder |
| `documents:delete` | delete (soft-delete to Trash) a document — lets the ERP clean up after itself |
| `documents:read` | read a document, list folders |
| `integrations:read` | poll the iPaaS/reconcile trigger feed |
| `webhooks:manage` | create/list/delete subscriptions, rotate secret, test-send, redeliver |

Issue **one key per tenant** carrying the union of scopes the ERP needs
(`upload`, `documents:read`, `documents:write`, `integrations:read`, `webhooks:manage`,
and `documents:delete` if the ERP should be able to remove documents it created — leave it
off for a read+create-only posture).
Keys are tenant-scoped: a key can only ever touch its own tenant's data (Postgres RLS
fails closed otherwise).

> The key is a bearer secret. Store it in the ERP's secret store / env, never in source or
> chat. Rotate by issuing a new key and retiring the old one.

### 2.2 Webhook management auth (session OR API key)

The webhook **management** endpoints (`/api/v1/webhooks…`, §4.1) accept **either** the
admin-UI session cookie **or** a Bearer `vdms_` API key carrying the `webhooks:manage`
scope (`SessionOrAPIKey` on the connector routes). This means the ERP can manage its own
subscription programmatically — e.g. a boot-time `ensureDmsSubscription` that checks for an
existing subscription and creates one if missing — using the same API key it pushes
documents with, as long as that key includes `webhooks:manage`.

### 2.3 Gateway

All outbound calls go through the gateway (Kong) at the tenant's SeDoc base URL. REST
upstreams are health-checked and the gateway injects its signature header to the backends;
the ERP does not need to reproduce that — it only sends the `Authorization: Bearer vdms_…`
header. (If you run against a service directly, bypassing Kong, you must still send the
Bearer key — the per-route middleware is on the service, not only the gateway.)

---

## 3. Outbound — ERP → DMS REST API

Base path: `{DMS_BASE_URL}/api/v1`.

### 3.1 Endpoint reference

| Method & path | Scope | Idempotent | Purpose |
|---|---|---|---|
| `POST /storage/uploads/initiate` | `upload` | — | Begin an upload, get an upload session + presigned target |
| `POST /storage/uploads/{upload_id}/complete` | `upload` | — | Finalize the blob after the bytes are uploaded |
| `POST /storage/uploads/{upload_id}/abort` | `upload` | — | Cancel an in-flight upload |
| `GET  /storage/downloads/{document_id}/{version_id}` | `upload` | yes | Get a download URL for a version |
| `POST /documents` | `documents:write` | **yes** (`Idempotency-Key`) | Create the document record |
| `GET  /documents/{document_id}` | `documents:read` | yes | Read current document state (lifecycle, metadata) |
| `POST /documents/{document_id}/versions` | `documents:write` | **yes** (`Idempotency-Key`) | Attach a new version to a document |
| `DELETE /documents/{document_id}` | `documents:delete` | yes (repeat → `404`) | Soft-delete a document to Trash; emits `dms.document.deleted.v1`. `423` under legal hold |
| `GET  /workspaces/{workspace_id}/folders` | `documents:read` | yes | List folders (to resolve/ensure target folder) |
| `POST /workspaces/{workspace_id}/folders` | `documents:write` | yes | Create a folder |
| `GET  /integrations/triggers/documents?since=&limit=` | `integrations:read` | yes | Poll documents updated since a cursor (reconcile) |

Routes:
[storage_proxy.go:83-86](../../services/document/internal/handler/storage_proxy.go#L83),
[main.go:1018-1022](../../services/document/cmd/server/main.go#L1018),
[integrations_triggers.go:40](../../services/document/internal/handler/integrations_triggers.go#L40).

### 3.2 The ingest flow (push a document)

The ERP sync worker uploads a document in this order. Each ERP entity (invoice, quote,
sales_order, payment_received) maps to one SeDoc document.

```
1. (first time per workspace) ensure folder
   GET  /api/v1/workspaces/{workspace_id}/folders          → find or
   POST /api/v1/workspaces/{workspace_id}/folders          → create

2. initiate upload
   POST /api/v1/storage/uploads/initiate
        body: { workspace_id, folder_id, filename, mime_type, size_bytes, sha256 }
        → { upload_id, upload_url }      (presigned PUT target)

3. upload the bytes
   PUT  <upload_url>   (raw file body — straight to object storage, not through the API)

4. complete the upload
   POST /api/v1/storage/uploads/{upload_id}/complete
        → { content_blob_id, … }

5a. create the document (first sync of this entity)
   POST /api/v1/documents
        Idempotency-Key: <stable per entity, e.g. "invoice:{id}">
        body: { workspace_id, folder_id, title, content_blob_id, document_class, tags, … }
        → { id (document_id), lifecycle_state, … }

5b. OR add a version (entity already has a document)
   POST /api/v1/documents/{document_id}/versions
        Idempotency-Key: <stable per version>
        body: { content_blob_id, … }
        → { version_id, version_number, … }
```

After step 5, SeDoc emits `dms.version.uploaded.v1` (and `dms.document.created.v1` on
first create). Those events flow back to the ERP via webhooks (§4) — that is how the ERP
learns the upload landed, rather than trusting the synchronous response alone.

### 3.3 Idempotency

`POST /documents` and `POST /documents/{id}/versions` run behind the `Idempotency` middleware
([main.go:1012](../../services/document/cmd/server/main.go#L1012)). Send a stable
`Idempotency-Key` header; a retry with the same key **replays the original result instead of
creating a duplicate**. The key is tenant-scoped server-side.

This is the server-side counterpart to the ERP's `UNIQUE(entity_type, entity_id)` on its sync
log: derive the key deterministically from the ERP entity (e.g. `invoice:{id}` for create,
`invoice:{id}:v{n}` for a version) so a crash-and-retry never double-ingests.

### 3.4 Reconcile (read current state)

To reconcile drift without waiting for events, poll:

```
GET /api/v1/integrations/triggers/documents?since=<rfc3339>&limit=<≤100>
Authorization: Bearer vdms_…            (scope integrations:read)
```

Returns documents with `updated_at > since`, ordered `updated_at ASC` (so a "dedupe by id"
cursor pattern works), capped at 100. Each row includes `id`, `lifecycle_state`,
`workspace_id`, `folder_id`, `document_class`, `tags`, timestamps
([integrations_triggers.go:46](../../services/document/internal/handler/integrations_triggers.go#L46)).
`since` defaults to "1 hour ago" if omitted.

For a single document, `GET /api/v1/documents/{document_id}` (scope `documents:read`) returns
its current `lifecycle_state`.

> **Note — version count.** A SeDoc document has no `version_count` field (only
> `current_version_id`). The ERP must maintain its own count by tallying
> `dms.version.uploaded.v1` events, or by listing versions. Don't expect to read a count from
> a single document GET. (See §7.)

### 3.5 Deep link — "Open in DMS"

The canonical document URL in the SeDoc web UI is a **path**, not query params:

```
{DMS_BASE_URL}/workspaces/{workspace_id}/documents/{document_id}
```

(route [web/.../workspaces/$workspaceId/documents/$documentId.tsx](../../web/src/routes/_authenticated/workspaces/$workspaceId/documents/$documentId.tsx)).

Build the ERP's "Open in DMS" link with this template. A `?folder=…&doc=…` query form does
**not** resolve to the document — it lands on the workspace index. (See §7.)

---

## 4. Inbound — DMS → ERP Webhooks

SeDoc pushes domain events to a tenant-registered HTTPS endpoint. The connector service
subscribes to NATS domain events and fans each one out to matching subscriptions
([service.go:186](../../services/connector/internal/service/service.go#L186)), then a delivery
worker POSTs them with retry + dead-lettering
([webhook/delivery.go](../../services/connector/internal/webhook/delivery.go)).

### 4.1 Subscription management

Session-authenticated (admin UI / admin session), connector service:

| Method & path | Purpose |
|---|---|
| `POST /api/v1/webhooks` | Create a subscription: `{ "url": "...", "events": ["dms.version.uploaded.v1", …] }` → returns the subscription incl. its signing **secret** (`whsec_…`) |
| `GET /api/v1/webhooks` | List subscriptions |
| `DELETE /api/v1/webhooks/{id}` | Delete a subscription |
| `GET /api/v1/webhooks/{id}/deliveries` | Last 50 delivery attempts (status, response body) |
| `POST /api/v1/webhooks/{id}/rotate-secret` | Rotate the signing secret |
| `POST /api/v1/webhooks/{id}/test` | Send a synthetic test event to the URL |
| `POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/redeliver` | Replay a single delivery |

Routes: [handler.go:52-58](../../services/connector/internal/handler/handler.go#L52).

**URL requirements** (enforced by `ValidateURL` at create time,
[delivery.go:145](../../services/connector/internal/webhook/delivery.go#L145)):

- Must be **`https`**.
- Must resolve to a **public** IP — loopback, private (RFC1918), and link-local addresses are
  rejected (SSRF guard).
- Must pass a `HEAD` reachability check.

> This means you cannot register `http://localhost:…` for local testing. Use a public HTTPS
> tunnel (e.g. ngrok) or a deployed receiver.

**On-prem / self-hosted exception.** Deployments where the receiver legitimately lives on a
trusted private network (e.g. an internal ERP on the same LAN) can set
`SEDOC_WEBHOOK_ALLOW_PRIVATE=true` on the connector service
(`cfg.WebhookAllowPrivateTargets`). That waives the `https` + public-IP checks for
subscription URLs — plain `http://192.168.…` targets register and deliver. The DNS and
`HEAD` reachability checks still run, and every delivery is still HMAC-signed (§4.3), so
receivers remain authenticated. Leave the flag unset on public multi-tenant deployments.

### 4.2 Delivery format

Each delivery is an HTTP POST to the subscription URL:

```
POST <subscription url>
Content-Type: application/json
User-Agent: SeDoc-Webhook/1.0
X-DMS-Event:     <event type, e.g. dms.version.uploaded.v1>
X-DMS-Timestamp: <unix seconds>
X-DMS-Signature: sha256=<hex>

<raw JSON body = the CloudEvents envelope>
```

([delivery.go:105-114](../../services/connector/internal/webhook/delivery.go#L105))

A delivery is considered successful on a **2xx** response. Anything else is retried with
exponential backoff (`model.RetryBackoff`); after the configured attempts are exhausted the
delivery is **dead-lettered** ([delivery.go:129-139](../../services/connector/internal/webhook/delivery.go#L129)).
The receiver should respond `2xx` quickly and process asynchronously.

### 4.3 Signature verification (ERP side)

The signature is HMAC-SHA256 over `timestamp + "." + rawBody` keyed by the subscription
secret, hex-encoded, prefixed `sha256=`
([delivery.go:173](../../services/connector/internal/webhook/delivery.go#L173)):

```
X-DMS-Signature = "sha256=" + hex( HMAC_SHA256( secret, timestamp + "." + rawBody ) )
```

The ERP receiver must verify against the **raw request body** (not a re-serialized object):

```js
import crypto from 'node:crypto';

function verifyDmsWebhook(req, rawBody, secret) {
  const ts  = req.header('X-DMS-Timestamp');
  const sig  = req.header('X-DMS-Signature');         // "sha256=…"
  const mac  = crypto.createHmac('sha256', secret)
                     .update(ts).update('.').update(rawBody)   // timestamp + "." + body
                     .digest('hex');
  const expected = `sha256=${mac}`;
  // constant-time compare; also reject if |now - ts| is too large (replay guard)
  return sig && crypto.timingSafeEqual(Buffer.from(sig), Buffer.from(expected));
}
```

Reject the request if the signature doesn't match. Optionally reject if
`|now − X-DMS-Timestamp|` exceeds a few minutes to bound replay.

### 4.4 Envelope shape

The body is the domain event's CloudEvents-style envelope (forwarded verbatim from NATS):

```json
{
  "type":     "dms.version.uploaded.v1",
  "tenantid": "<tenant uuid>",
  "id":       "<event_id — UUIDv7, idempotency key>",
  "time":     "2026-06-01T12:00:00Z",
  "data": {
    "event_id":       "...",
    "tenant_id":      "...",
    "document_id":    "...",
    "version_id":     "...",
    "version_number": 3
  }
}
```

- **Idempotency:** dedupe on the event id (`event_id` in `data`, mirrored to the envelope
  `id`). The same event may be delivered more than once (at-least-once).
- **Tenant:** carried at the envelope top level as `tenantid`; some older payloads also
  duplicate it into `data.tenant_id`. Read `tenantid` first, fall back to `data.tenant_id`
  ([service.go:233](../../services/connector/internal/service/service.go#L233)). The ERP must
  map `tenantid` to its own tenant before any DB write.

### 4.5 Event catalog (the ones the ERP cares about)

| Subject | Emitted by | Key `data` fields | ERP uses it to… |
|---|---|---|---|
| `dms.version.uploaded.v1` | document svc ([events.proto:25](../../proto/vaultdms/v1/events.proto#L25)) | `document_id`, `version_id`, `version_number`, `sha256`, `size_bytes` | confirm upload; increment version count |
| `dms.document.state_changed.v1` | document svc ([documents.go:1184](../../services/document/internal/service/documents.go#L1184)) | `document_id`, `from_state`, `to_state`, `action`, `reason`, `changed_by` | update lifecycle state |
| `dms.document.deleted.v1` | document svc ([documents.go:296](../../services/document/internal/service/documents.go#L296)) | `document_id`, `deleted_by` | mark deleted-in-DMS |
| `dms.signature.completed.v1` | signature svc ([service.go:236](../../services/signature/internal/service/service.go#L236), [esign.go:706](../../services/signature/internal/service/esign.go#L706)); workflow stub ([signature_stub.go:104](../../services/workflow/internal/workflows/signature_stub.go#L104)) | `document_id`, `version_id`, `request_id` (stub: `instance_id`, `document_id`) | mark the invoice/quote signed |

Subscribe to exactly these four for the ERP sync feature. Other domains
(`dms.document.created.v1`, `dms.document.updated.v1`, `dms.folder.*`, …) are available from
the same fanout if needed.

> **Signature mapping caveat.** `document_id` is the only field present on **every**
> signature-completed path — the Temporal workflow stub omits `version_id`/`request_id`. Map
> the signed status by `document_id → ERP entity`; do not depend on `request_id` being
> present.

The connector fanout explicitly includes `dms.signature.>`, `dms.folder.>`, and the
document state/delete subjects ([service.go:206-207](../../services/connector/internal/service/service.go#L206)),
so all four reach subscribers.

---

## 5. License enforcement (HTTP 402 / 423)

SeDoc enforces a deployment license (ADR 0095, a signed JWT in `SEDOC_LICENSE_JWT`).
Two gates sit directly on the integration surface, so the ERP **will** encounter these
status codes and must handle them distinctly from auth failures:

| Status | Code in body | When | Affected endpoints |
|---|---|---|---|
| **402 Payment Required** | `feature_not_licensed` | the license lacks a feature flag | `GET /integrations/triggers/*` (reconcile poll) requires `feature_flags.ipaas` |
| **423 Locked** | `license_locked` | license is in **grace** or **expired** | every mutating call (POST/PUT/PATCH/DELETE) on the document, signature, and workflow services — i.e. the entire §3.2 ingest flow |

Error body shape for both:

```json
{ "error": { "code": "license_locked", "message": "License expired — writes are locked. …" } }
```

What stays open, by design:

- **Reads** (GET document, list folders, download) keep working through grace/expiry so a
  tenant can export and wind down.
- **Webhook delivery is not license-gated.** The connector keeps fanning out events and the
  delivery worker keeps POSTing to the ERP regardless of license state, so the ERP stays
  consistent (deletes, state changes) even while writes are locked.
- **Webhook management** (§4.1) is likewise ungated.

**ERP handling guidance:**

- On **423**: pause the outbound push queue and raise an operator alert ("DMS license
  expired — sync paused"). Do **not** retry-storm or mark sync rows permanently failed —
  the push succeeds unchanged once the license is renewed. Keep consuming inbound webhooks.
- On **402**: alert as a configuration/licensing error and stop polling that endpoint —
  retries can never succeed until the license adds the `ipaas` feature. The rest of the
  integration (push + webhooks) continues to work without it.
- Treat both as distinct from **401** (bad/revoked API key) in the sync log and dashboards.

**License prerequisite for this integration:** the deployment's license must include
`feature_flags.ipaas` for the reconcile poll (§3.4). Verify with a quick
`GET /api/v1/integrations/triggers/documents?limit=1` — a 402 means the license needs
regenerating with `--features …,ipaas` (`cmd/license-gen`), not a code problem. The `esign`
flag gates only the third-party e-sign vendor routes; `dms.signature.completed.v1` events
themselves are not feature-gated.

---

## 6. End-to-end verification

A minimal smoke test exercising both directions:

1. **Issue an API key** for the test tenant with scopes `upload, documents:read,
   documents:write, integrations:read`.
2. **Register a webhook** (`POST /api/v1/webhooks`) pointing at a public HTTPS receiver, with
   `events: ["dms.version.uploaded.v1","dms.document.state_changed.v1","dms.document.deleted.v1","dms.signature.completed.v1"]`.
   Save the returned `whsec_…` secret.
3. **Ingest** a document via the §3.2 flow (folder → initiate → PUT → complete → create).
4. **Observe inbound:** the receiver gets `dms.version.uploaded.v1`; verify the HMAC with the
   secret; confirm the envelope `tenantid` and `data.document_id`.
5. **Transition lifecycle** on the document → receiver gets `dms.document.state_changed.v1`
   with `to_state`.
6. **Reconcile:** `GET /api/v1/integrations/triggers/documents?since=…` returns the document
   with the new `lifecycle_state`.
7. **Deep link:** open `{DMS_BASE_URL}/workspaces/{workspace_id}/documents/{document_id}` and
   confirm it lands on the document.
8. **Delivery log:** `GET /api/v1/webhooks/{id}/deliveries` shows the attempts and 2xx
   responses; `POST /api/v1/webhooks/{id}/test` and `…/redeliver` are available for debugging.

---

## 7. Configuring the integration in the SeDoc web UI

Everything above can be driven from the API, but a tenant admin sets most of it up from the
SeDoc web admin. For reference, the relevant screens:

| Screen | Route | What it does |
|---|---|---|
| **API Keys** | `/admin/api-keys` ([api-keys.tsx](../../web/src/routes/_authenticated/admin/api-keys.tsx)) | Create a key (name + expiry, default 90 days); the secret is shown **once** at creation — copy it then. Revoke keys; see prefix / last-used / expiry. |
| **Webhooks** | `/admin/webhooks` ([webhooks.tsx](../../web/src/routes/_authenticated/admin/webhooks.tsx)) | Create a subscription (URL + pick events), reveal the signing secret once, rotate the secret, send a test event, browse the delivery log, and redeliver a single attempt. |
| **Integrations hub** | `/admin/integrations` ([integrations/index.tsx](../../web/src/routes/_authenticated/admin/integrations/index.tsx)) | Landing page for connectors, iPaaS, email intake, and MCP. |
| **Connectors** | `/admin/connectors` ([connectors.tsx](../../web/src/routes/_authenticated/admin/connectors.tsx)) | Native connector setup (Google, M365, …) — not used by the ERP push, but lives in the same area. |
| **Document (deep-link target)** | `/workspaces/{workspace_id}/documents/{document_id}` ([$documentId.tsx](../../web/src/routes/_authenticated/workspaces/$workspaceId/documents/$documentId.tsx)) | Where an "Open in DMS" link lands (§3.5). |

> The ERP's own status panels / admin sync log (Phase 9) live in the **ERP** web app, not
> here. These SeDoc screens are the *provider* side — where the tenant mints the key and
> registers the webhook the ERP then uses.

### Two UI caveats for the full ERP flow

1. **Pick the scopes deliberately.** The `/admin/api-keys` create form defaults to
   `documents:read` + `documents:write`
   ([api-keys.tsx](../../web/src/routes/_authenticated/admin/api-keys.tsx)); toggle on
   **`upload`** (storage), **`integrations:read`** (reconcile poll), **`webhooks:manage`**
   (self-managed subscription) and, if wanted, **`documents:delete`** before issuing. Scopes
   are fixed at issue time — to change a key's scopes, issue a new key and revoke the old one.
2. **`state_changed` isn't in the webhook preset list.** The `/admin/webhooks` event presets
   include `document.created/updated/deleted`, `version.uploaded`, and `signature.completed`,
   but **not** `dms.document.state_changed.v1`
   ([webhooks.tsx:28](../../web/src/routes/_authenticated/admin/webhooks.tsx#L28)). The
   delivery backend accepts any event string, so subscribe to it via
   `POST /api/v1/webhooks` directly, or add it to the preset list.

---

## 8. Known gaps / contract notes for the ERP side

These are fixed in the **ERP repository**, not SeDoc — but they are contract decisions
SeDoc dictates:

1. **Deep-link template.** Use the path form
   `…/workspaces/{workspace_id}/documents/{document_id}`. The `?folder=&doc=` query form does
   not resolve in the SeDoc web UI (§3.5).
2. **Version count.** There is no single-field version count on a SeDoc document. The ERP
   must derive it from `dms.version.uploaded.v1` events (or list versions). Don't read it from
   a document GET (§3.4).

---

## 9. Reference

- Dual-auth middleware: [pkg/middleware/session_or_apikey.go](../../pkg/middleware/session_or_apikey.go)
- ERP ingest routes: [services/document/cmd/server/main.go:1018](../../services/document/cmd/server/main.go#L1018),
  [storage_proxy.go:83](../../services/document/internal/handler/storage_proxy.go#L83)
- Reconcile/poll: [integrations_triggers.go](../../services/document/internal/handler/integrations_triggers.go)
- Webhook subscription API: [services/connector/internal/handler/handler.go:52](../../services/connector/internal/handler/handler.go#L52)
- Webhook delivery + HMAC + URL validation: [services/connector/internal/webhook/delivery.go](../../services/connector/internal/webhook/delivery.go)
- Event fanout: [services/connector/internal/service/service.go:186](../../services/connector/internal/service/service.go#L186)
- Event schema (versions): [proto/vaultdms/v1/events.proto](../../proto/vaultdms/v1/events.proto)
- Related ADRs: 0076 (customer webhooks), 0090 (iPaaS integrations), 0021 (version.uploaded emission point)
