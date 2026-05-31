# VaultDMS API — Integration Guide

A practical guide to integrating an external system with the VaultDMS REST API: how to
authenticate, push content, and receive events. The full machine-readable contract is the
[OpenAPI spec](./openapi.yaml); this guide explains how the pieces fit together.

- **Contract:** [openapi.yaml](./openapi.yaml) (rendered at the published API site)
- **Versioning policy:** [SEMVER_POLICY.md](./SEMVER_POLICY.md) · **Changelog:** [CHANGELOG.md](./CHANGELOG.md)
- **End-to-end example:** [ERP integration guide](../integrations/erp-integration.md)

---

## 1. Base URL, versioning, and common headers

All endpoints live under a single versioned prefix:

```
{DMS_BASE_URL}/api/v1/...
```

- Production: `https://api.vaultdms.io/api/v1`
- Local dev: `http://localhost:8080/api/v1`

The whole `/api/v1/*` surface shares **one** semver — there are no per-endpoint versions.
Every response carries:

| Header | Meaning |
|---|---|
| `API-Version` | the API semver that served the request (e.g. `1.0.0`) |
| `X-Correlation-ID` | request correlation id — echoed if you send one, generated otherwise. **Log it**; it ties your call to VaultDMS server logs for support. |

Breaking changes bump the major version and are recorded in [CHANGELOG.md](./CHANGELOG.md).
Pin against a major version and watch the changelog.

---

## 2. Authentication

VaultDMS accepts two credential types. External integrations use **API keys**.

### 2.1 API keys (machine-to-machine)

Send the key as a Bearer token:

```
Authorization: Bearer vdms_<secret>
```

Keys are **tenant-scoped** (a key only ever sees its own tenant's data — Postgres
row-level security fails closed otherwise) and carry one or more **scopes**. Each protected
route demands a specific scope:

| Scope | Grants |
|---|---|
| `upload` | the storage upload flow (initiate / complete / abort / download) |
| `documents:read` | read a document, list folders |
| `documents:write` | create documents, versions, folders |
| `integrations:read` | poll the integration trigger feed (reconcile) |

Issue keys from the admin API-key surface (`/api/v1/api-keys/*`) or the admin UI. Grant the
**minimum** scopes the integration needs. Treat the key as a secret: store it in a secret
manager or environment variable, never in source control. Rotate by issuing a new key and
retiring the old one.

> **Dual-auth routes.** A subset of routes accept *either* a session cookie (browser) *or* a
> Bearer API key (integration) on the same path — dispatch is by the `Authorization` header.
> Both stamp the same tenant/user identity, so authorization and tenant isolation behave
> identically regardless of which credential you use.

### 2.2 Session cookies (interactive)

The web UI and admin endpoints use session cookies (login via `/api/v1/auth/login`). This is
for interactive/browser use, not service-to-service integration. Webhook **management**
endpoints (§4.1) are part of this surface.

---

## 3. Conventions

### 3.1 Idempotency

Write endpoints that create resources (e.g. `POST /documents`, `POST /documents/{id}/versions`)
honor an **`Idempotency-Key`** request header. A retry with the same key replays the original
result instead of creating a duplicate. The key is tenant-scoped server-side.

Derive the key deterministically from your own entity (e.g. `invoice:{id}`,
`invoice:{id}:v{n}`) so a crash-and-retry never double-creates.

### 3.2 Pagination & polling

List/poll endpoints use a cursor + limit. The integration trigger feed, for example:

```
GET /api/v1/integrations/triggers/documents?since=<rfc3339>&limit=<n>
```

- `since` — return rows with `updated_at` strictly greater; defaults to "1 hour ago".
- `limit` — page size, capped server-side at **100**.
- Results are ordered `updated_at ASC`, so a "dedupe by id, advance cursor to last
  `updated_at`" loop converges without gaps.

### 3.3 Errors

Errors return a JSON body:

```json
{ "error": "human-readable reason" }
```

with a conventional status code (`400` invalid input, `401` missing/!valid credential,
`403` scope/permission denied, `404` not found, `5xx` server). Always branch on the status
code, not the message text.

---

## 4. Receiving events (webhooks)

VaultDMS pushes domain events to a tenant-registered HTTPS endpoint, HMAC-signed.

### 4.1 Register a subscription

| Method & path | Purpose |
|---|---|
| `POST /api/v1/webhooks` | Create: `{ "url": "...", "events": ["dms.version.uploaded.v1", …] }` → returns the subscription incl. its signing **secret** (`whsec_…`) |
| `GET /api/v1/webhooks` | List subscriptions |
| `DELETE /api/v1/webhooks/{id}` | Delete |
| `GET /api/v1/webhooks/{id}/deliveries` | Recent delivery attempts (status, response) |
| `POST /api/v1/webhooks/{id}/rotate-secret` | Rotate the signing secret |
| `POST /api/v1/webhooks/{id}/test` | Send a synthetic test event |
| `POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/redeliver` | Replay a delivery |

**Subscription URL requirements** (validated at create time):

- Must be **`https`**.
- Must resolve to a **public** IP — loopback / private / link-local are rejected (SSRF guard).
- Must pass a `HEAD` reachability check.

> You cannot register `http://localhost`. For local development use a public HTTPS tunnel.

### 4.2 Delivery format

```
POST <your url>
Content-Type: application/json
User-Agent: VaultDMS-Webhook/1.0
X-DMS-Event:     <event type>
X-DMS-Timestamp: <unix seconds>
X-DMS-Signature: sha256=<hex>

<raw JSON body — the event envelope>
```

Respond **2xx** to acknowledge. Non-2xx (or a timeout) is retried with exponential backoff;
after the attempts are exhausted the delivery is **dead-lettered** (inspect via the
deliveries endpoint, replay via redeliver). Acknowledge fast and process asynchronously.

### 4.3 Verify the signature

The signature is HMAC-SHA256 over `timestamp + "." + rawBody`, keyed by the subscription
secret, hex-encoded with a `sha256=` prefix:

```
X-DMS-Signature = "sha256=" + hex( HMAC_SHA256( secret, timestamp + "." + rawBody ) )
```

Verify against the **raw request body** (not a re-serialized object):

```js
import crypto from 'node:crypto';

function verify(req, rawBody, secret) {
  const ts  = req.header('X-DMS-Timestamp');
  const sig = req.header('X-DMS-Signature');                  // "sha256=…"
  const mac = crypto.createHmac('sha256', secret)
                    .update(ts).update('.').update(rawBody)
                    .digest('hex');
  const expected = `sha256=${mac}`;
  return sig &&
         crypto.timingSafeEqual(Buffer.from(sig), Buffer.from(expected)) &&
         Math.abs(Date.now() / 1000 - Number(ts)) < 300;       // 5-min replay window
}
```

### 4.4 Envelope & idempotency

The body is a CloudEvents-style envelope:

```json
{
  "type":     "dms.version.uploaded.v1",
  "tenantid": "<tenant uuid>",
  "id":       "<event id — idempotency key>",
  "time":     "2026-06-01T12:00:00Z",
  "data":     { "...event-specific fields..." }
}
```

- **At-least-once:** the same event may arrive more than once. **Dedupe on the event id.**
- **Tenant:** read `tenantid` at the envelope top level (fall back to `data.tenant_id` for
  older payloads). Map it to your tenant before any write.

### 4.5 Event catalog (common subjects)

| Subject | `data` highlights |
|---|---|
| `dms.document.created.v1` | `document_id`, `workspace_id`, `folder_id`, `title` |
| `dms.version.uploaded.v1` | `document_id`, `version_id`, `version_number`, `sha256`, `size_bytes` |
| `dms.document.state_changed.v1` | `document_id`, `from_state`, `to_state`, `action` |
| `dms.document.deleted.v1` | `document_id`, `deleted_by` |
| `dms.signature.completed.v1` | `document_id`, `version_id`, `request_id` |

Subscribe only to the subjects you need.

---

## 5. Pushing content

To ingest a document, use the storage upload flow then create the document/version:

```
POST /api/v1/storage/uploads/initiate            (scope: upload)
PUT  <presigned upload_url>                       (raw bytes → object storage)
POST /api/v1/storage/uploads/{upload_id}/complete (scope: upload)
POST /api/v1/documents                            (scope: documents:write, Idempotency-Key)
   └─ or POST /api/v1/documents/{id}/versions     (scope: documents:write, Idempotency-Key)
```

Resolve the target folder first with
`GET|POST /api/v1/workspaces/{workspace_id}/folders` (scopes `documents:read` /
`documents:write`). Full request/response field shapes are in [openapi.yaml](./openapi.yaml).

After a successful ingest, VaultDMS emits `dms.document.created.v1` /
`dms.version.uploaded.v1`, which arrive at your webhook (§4) — the durable confirmation that
the content landed.

---

## 6. Deep linking into the VaultDMS UI

To link a user from your app to the document in the VaultDMS web UI, use the path form:

```
{DMS_BASE_URL}/workspaces/{workspace_id}/documents/{document_id}
```

---

## 7. Configuring from the web UI

API keys and webhooks can be managed from the VaultDMS admin UI instead of the API:

| Screen | Route | Purpose |
|---|---|---|
| **API Keys** | `/admin/api-keys` | Create a key (name + expiry); the secret is shown **once** — copy it then. Revoke keys. |
| **Webhooks** | `/admin/webhooks` | Create a subscription (URL + events), reveal/rotate the signing secret, send a test event, browse deliveries, redeliver. |
| **Integrations hub** | `/admin/integrations` | Connectors, iPaaS, email intake, MCP. |

Two caveats when setting up from the UI:

1. The API-key create form currently issues `documents:read` + `documents:write` only. If your
   integration also needs `upload` or `integrations:read`, mint the key via
   `POST /api/v1/api-keys` with the full scope set.
2. The webhook event-preset list doesn't include `dms.document.state_changed.v1`. The delivery
   backend accepts any event string, so subscribe to it via `POST /api/v1/webhooks` directly.

---

## 8. Worked example

The [ERP integration guide](../integrations/erp-integration.md) walks the full bidirectional
flow end-to-end (API-key issuance, the 5-step ingest, webhook subscription + HMAC
verification, reconcile, deep links) for an external ERP pushing invoices/quotes/orders into
VaultDMS and reflecting their lifecycle + signature status back.
