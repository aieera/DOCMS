# ADR 0071 — Third-party e-Signature Connectors (DocuSign, Adobe Sign)

Date: 2026-05-09
Status: Accepted

## Context

[ADR 0070](0070-qes-tsp-integration.md) ships QES via Swisscom /
Intesi / InfoCert for cases where eIDAS-qualified signatures are
required. Many customers — especially North American — already
have a paid relationship with **DocuSign** or **Adobe Sign** and
want to keep their existing audit trail / signing workflow. §11.2
asks for both to be first-class providers on the signature service.

Two providers, similar shape:

| Vendor      | API base                           | Auth                                  | Webhook product       |
|-------------|------------------------------------|---------------------------------------|------------------------|
| DocuSign    | `https://demo.docusign.net/restapi` | OAuth2 authcode → JWT user-impersonation | DocuSign Connect       |
| Adobe Sign  | `https://api.echosign.com/api/rest` | OAuth2 authcode + refresh-token        | Adobe Sign Webhooks v1 |

Both produce a "signed PDF" + "Certificate of Completion" on
completion. Both expose a webhook callback when the envelope status
changes. Both store their own LTV-eligible audit trail.

§11.2 deliverables:

1. Connector **SDK reuse** — one Go package, two adapters, one
   service-layer call site.
2. **Send doc for signing** with signer order + role; **status
   webhooks** that update us; **receive signed back** as a new
   document version through the existing version-uploaded path
   (ADR 0021).
3. **OAuth per tenant** — each tenant connects their own account
   once; we never share credentials across tenants.
4. **Retention of TSP audit trail** — the CoC PDF the vendor
   produces is stored as an attachment + the JSON event log is
   archived under the signature_request.

## Decision

### `pkg/esign` — connector SDK

```go
type ESignClient interface {
    Send(ctx, SendReq)         (*SendResp, error)        // create envelope + send
    GetStatus(ctx, StatusReq)  (*StatusResp, error)      // poll fallback (webhooks lossy)
    GetSignedDocument(ctx, GetSignedReq) (*GetSignedResp, error) // pull final PDF + CoC
    ParseWebhook(headers, raw) (*WebhookEvent, error)    // verify HMAC + decode
}
```

Two adapters: `docusign.go`, `adobesign.go`, plus `mock.go` for
CI + Playwright. The signature-service factory picks one by
`provider` on the signature request (`docusign` / `adobe_sign`).

### Per-tenant OAuth

```sql
CREATE TABLE esign_oauth_tokens (
  tenant_id      UUID NOT NULL,
  provider       TEXT NOT NULL CHECK (provider IN ('docusign','adobe_sign')),
  access_token   TEXT NOT NULL,
  refresh_token  TEXT,
  expires_at     TIMESTAMPTZ NOT NULL,
  account_id     TEXT,                 -- DocuSign accountId / Adobe baseUri
  base_uri       TEXT,                 -- regional endpoint discovered at link time
  scope          TEXT,
  connected_by   UUID,
  connected_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, provider)
);
```

Tokens are encrypted at rest using `pkg/crypto.SealString` with the
per-tenant KEK (ADR 0022). Even a full Postgres dump won't yield
usable bearer tokens without the KEK.

Connect flow:
1. Admin clicks **Connect DocuSign** in `/admin/integrations`.
2. Browser → `GET /api/v1/esign/oauth/start?provider=docusign` →
   handler builds the vendor authorize URL with HMAC'd state and
   returns redirect.
3. Vendor redirects back to
   `/api/v1/esign/oauth/callback?provider=…&code=…&state=…`.
4. Handler exchanges the code, persists encrypted tokens, redirects
   the admin to `/admin/integrations?connected=docusign`.

Refresh: a goroutine refreshes any token within 5 min of expiry,
gated on a Redis lock so multiple service replicas don't race.

### Send + status + ingest

```
POST /api/v1/signatures/requests        provider: 'docusign' | 'adobe_sign'
   ↓ service.CreateRequest
   ↓ if provider == 'internal' → existing path
   ↓ else
   ↓ esignSvc.Send(req, document_bytes) → envelope_id + signing_url per signer
   ↓ store envelope_id on signature_requests.provider_envelope_id

POST /api/v1/esign/webhook/docusign     ← DocuSign Connect HTTPS POST
POST /api/v1/esign/webhook/adobe_sign   ← Adobe webhook
   ↓ handler.IngestWebhook
   ↓ verify HMAC (provider-specific)
   ↓ idempotently insert into esign_envelope_events
   ↓ on "completed": esignSvc.GetSignedDocument
   ↓ → pkg/storage upload + document service "create new version" RPC
   ↓ → CoC PDF stored as attachment under the signature_request
```

A 5-minute poll loop reconciles drift — webhooks are at-least-once
but each vendor has had outages. The poll calls `GetStatus` for any
`in_progress` request whose row hasn't been touched in 30 min and
forces a state update if the webhook never landed.

### Audit trail retention

Two artefacts come back from each vendor:

| Artefact            | DocuSign                              | Adobe Sign                           | Stored as                      |
|---------------------|----------------------------------------|---------------------------------------|---------------------------------|
| Signed PDF          | `combinedDocuments=true`              | `agreements/{id}/combinedDocument`    | New document version (ADR 0021) |
| Certificate of Completion | `certificate=true`              | `agreements/{id}/auditTrail`          | Attached blob, kind="esign_coc" |
| Event log JSON      | DocuSign Connect message body         | Adobe webhook event                   | `esign_envelope_events` row     |

The vendor's audit trail IS the source of truth for who signed
when from which IP — we don't try to reconstruct it ourselves. The
verify endpoint surfaces both: our internal record + a deep-link
to the vendor's certificate.

### Schema (migration 000036)

```sql
CREATE TABLE esign_envelope_events (
  tenant_id     UUID NOT NULL,
  id            UUID NOT NULL DEFAULT gen_random_uuid(),
  request_id    UUID NOT NULL,
  provider      TEXT NOT NULL,
  envelope_id   TEXT NOT NULL,
  event_type    TEXT NOT NULL,           -- "envelope_sent","recipient_signed","envelope_completed",…
  external_id   TEXT,                    -- vendor's event id; UNIQUE with provider+envelope for idempotency
  raw_payload   JSONB,
  received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (provider, envelope_id, external_id)
);
```

Plus the `esign_oauth_tokens` table above. Both RLS-isolated.

## Consequences

- **Webhook security**: DocuSign signs with HMAC-SHA256 over the
  payload using the Connect "HMAC Signature 1" secret; Adobe Sign
  signs with HMAC-SHA256 over `clientId + clientSecret + payload`.
  Both verifiers live in `pkg/esign/<provider>.go::ParseWebhook`.
  Wrong / missing HMAC → 401, no DB write.
- **Idempotency**: webhooks redeliver. We dedup on
  `(provider, envelope_id, external_id)` in `esign_envelope_events`.
  The completed-handler is idempotent at the version layer too —
  the document service rejects a second version-create with the
  same `provider_envelope_id` extra-attribute.
- **OAuth token rotation**: a 5-min watcher refreshes within the
  expiry window. On hard-fail (refresh token rejected) the
  integration is auto-disconnected; admin sees a banner on
  `/admin/integrations` until they reconnect.
- **Vendor outage**: webhooks queue at the vendor's side; our 5-min
  poll loop closes the gap on completion. Send-side outages bubble
  up as 503 to the user — we do NOT silently fall back to internal
  signing.
- **Cost**: DocuSign + Adobe both bill per envelope. The send
  endpoint is rate-limited per tenant (default 100/min) so a
  buggy script can't drain a customer's plan. Limit configurable
  via the existing `tenant_settings.api_quotas` JSON.

## Out of scope

- Bulk send / template management. Both vendors expose template
  APIs; we'll wire those in a follow-up once a customer asks.
- Sending across multiple accounts in one tenant. The OAuth row
  is `(tenant, provider)` — one connection per tenant per vendor.
- Adobe Acrobat Sign (the renamed-and-rebranded enterprise tier).
  Same API as Adobe Sign for the surface we touch; no special
  handling.
