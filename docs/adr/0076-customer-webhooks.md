# ADR 0076 — Customer-facing webhooks

Date: 2026-05-13
Status: Accepted (codifies the existing implementation in
`services/connector` and extends it with test-send + verification
samples per blueprint §12.2)

## Context

§12.2 of the product blueprint requires customer-facing webhooks
so external systems can react to VaultDMS domain events without
polling. The connector service already implements the bulk of the
plumbing (subscriptions table, JetStream fan-out, signed delivery,
retry, DLQ, redeliver). The ADR exists to codify the contract so a
future maintainer doesn't reinvent the wire format and so the
sample code we publish to customers (Verify panel) is anchored to
a single source of truth.

## Decision

### Tables

`webhook_subscriptions(tenant_id, id, url, secret, events JSONB,
active, failure_count, last_success_at, last_failure_at, created_by,
created_at, updated_at)` — `events` carries the array of subscribed
event types (`dms.document.created.v1`, etc.); rows are
tenant-scoped and RLS-isolated.

`webhook_deliveries(tenant_id, id, subscription_id, event_type,
event_id, payload JSONB, status, http_status, attempts,
last_attempt_at, next_retry_at, error_message, created_at)`
— append-only audit + retry state. `status` is the enum
`pending | failed | delivered | dead_letter`.

### Wire format

Every delivery is a POST with `Content-Type: application/json` and
the headers:

| Header | Value |
|---|---|
| `X-DMS-Signature` | `sha256=<hex>` HMAC-SHA256 of `timestamp + "." + body` |
| `X-DMS-Timestamp` | Unix seconds at send time |
| `X-DMS-Event` | Event type, e.g. `dms.document.created.v1` |
| `User-Agent` | `VaultDMS-Webhook/1.0` |

Customers must reject any request whose timestamp drifts more than
**5 minutes** from now — this is the replay window. The receiver
also re-computes the signature with their stored secret and uses a
constant-time compare. The Verify panel in
`/admin/webhooks` ships ready-made snippets for Node/Python/Go/Ruby
+ a curl-and-openssl recipe; those snippets are the customer
contract — if the signing input ever changes, the panel changes
with it (see `web/src/routes/_authenticated/admin/webhooks.tsx` →
`SAMPLES`).

### Retry + DLQ

Backoff schedule, capped at six attempts:

| Attempt | Wait before retry |
|---|---|
| 1 → 2 | 5 s |
| 2 → 3 | 30 s |
| 3 → 4 | 2 min |
| 4 → 5 | 15 min |
| 5 → 6 | 1 h |
| 6 → DLQ | 6 h then dead-letter |

A delivery is **dead-lettered** once attempts equals
`len(model.RetryBackoff)` (6) — at which point `status` flips to
`dead_letter` and no further retries fire. Admins can re-drive any
delivery (including DLQ rows) via `POST
/api/v1/webhooks/{id}/deliveries/{deliveryId}/redeliver`, which
clones the row with `attempts = 0` so the worker picks it up on
its next tick. The original row is preserved for audit.

### Test-send

`POST /api/v1/webhooks/{id}/test` (added Wave 12.2c) queues a
synthetic `dms.webhook.test.v1` delivery against an existing
subscription, going through the same HMAC + retry path as
production traffic. This lets operators verify their receiver
without having to trigger a real domain event in production.

### URL validation

`webhook.ValidateURL` enforces:

- `https://` only
- Hostname must resolve to a non-loopback, non-private,
  non-link-local IP (no SSRF to internal services)
- HEAD probe within 5s to confirm reachability before accepting
  the subscription

The frontend mirrors these checks for fast-fail UX but the backend
is authoritative.

## Consequences

- The connector service owns both subscription CRUD and the
  delivery worker — no separate webhooks service. JetStream
  fan-out (`StartEventFanout`) subscribes per-subject because
  `dms.>` spans multiple JetStream streams.
- Adding a new event family (`dms.foo.>`) requires extending the
  `subjects []string` list in `service.go::StartEventFanout` AND
  the `EVENT_PRESETS` array on the admin page; otherwise the
  webhook UI won't offer it as a checkbox.
- DLQ is durable — no auto-cleanup. If retention becomes an issue,
  add a background sweep keyed on `created_at < now() - interval
  '90 days' AND status = 'dead_letter'`.
- Replay protection is the receiver's responsibility (we emit
  `X-DMS-Timestamp` but don't authoritatively rotate; the Verify
  snippets demonstrate the 5-minute window).

## Not chosen

- **mTLS receivers** — out of scope for §12.2. HMAC was enough
  for blueprint sign-off; mTLS would require per-tenant cert
  enrollment and is queued for §15 enterprise SSO surface.
- **Per-event-type retry policy** — uniform schedule is simpler
  to reason about and matches Stripe / GitHub. Revisit if a
  specific event family proves bursty enough to need its own
  knob.
- **Server-Sent Events** as an alternative — pushed to the same
  §15 milestone; webhooks remain the contracted blueprint shape.
