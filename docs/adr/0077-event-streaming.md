# ADR 0077 — Customer event streaming

Date: 2026-05-13
Status: Accepted (deploy switch to NATS operator mode is staged
behind a feature flag; runtime code is shipped on the platform
account today so the polling endpoint, JWT issuance, and the
admin UI are all production-correct without requiring the
server-side rotation in this wave)

## Context

§12.3 of the product blueprint requires a streaming surface so
customers can subscribe to their own tenant's domain events
without needing the webhook receiver from §12.2. Two access
shapes are mandated:

1. **Native NATS** — customers connect a NATS client (Go, Python,
   JS, anything that speaks NATS) to a per-tenant subject
   namespace, scoped by token.
2. **HTTP polling fallback** — `GET /api/v1/events?since=...` with
   cursor pagination, for customers behind firewalls that can't
   hold an outbound NATS connection.

Retention is 7 days (configurable). Each event delivered through
either channel is the same JSON envelope the internal services
already emit on `dms.{domain}.{action}.v1` — so subscribers see
exactly what our internal consumers see, modulo subject rewrite.

## Decision

### Subject namespace

Every event the platform emits today carries a `tenant_id` in its
envelope and is published on `dms.{domain}.{action}.v1`. A new
**mirror consumer** in the connector service subscribes to the
existing JetStream subjects, reads each message, looks up
`envelope.data.tenant_id` (or `envelope.tenant_id` if flat), and
re-publishes onto `tenant.{tenant_id}.events.{domain}.{action}.v1`.

The mirror is its own NATS connection inside the connector
process. Failure modes: a malformed envelope is logged + Ack'd
(no infinite redelivery); a publish error returns Nak so the
upstream consumer retries with backoff. The mirror is per-event
idempotent because the Nats-Msg-Id header carries the source
delivery's unique id.

### Per-tenant JetStream stream

`TENANT_EVENTS_{tenantUUID}` (hex, no hyphens) covers the subject
filter `tenant.{tenantUUID}.events.>`. Stream config:

| Setting | Value |
|---|---|
| Retention | Limits |
| MaxAge | 7d (overridable per tenant via `event_stream_retention_days`) |
| MaxBytes | 5 GiB |
| Storage | File |
| Replicas | 1 in dev, 3 in prod |
| Discard | Old |
| Subjects | `tenant.{tenantUUID}.events.>` |

Streams are created lazily on the first token-issuance request for
a tenant and torn down by an admin "delete all my data" sweep
(Wave 12.6).

### Token issuance — JWT decentralized auth

NATS [decentralized auth](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/auth_intro/jwt)
is the production target. The shape:

- **Operator** key (`vaultdms-op`) — stored in the platform vault as
  the seed `O_VAULTDMS_OPERATOR_SEED`. Signs accounts.
- **Platform account** (`PLATFORM`) — where every `dms.*` subject
  is published. Existing services connect with the platform
  account creds.
- **System account** (`SYS`) — for monitoring + JetStream API.
- **Per-tenant accounts** (`TENANT_<uuid>`) — created at the same
  time as the per-tenant JetStream stream. Each has:
  - One signing key (operator-signed)
  - One subject-permission scope: `Pub Deny *`, `Sub Allow tenant.{tenantUUID}.events.>`
  - JetStream limits matching the stream's MaxBytes
- **Per-user JWTs** — issued on demand when an admin clicks
  "Generate token." The user JWT inherits the account's subject
  scope, has a configurable expiry (default 30d), and is returned
  as a creds file (concatenated NKEY seed + JWT) the customer
  saves to disk.

The polling endpoint shares the same token surface (Bearer header)
so a customer who issued one token uses it for both NATS and
HTTP without re-provisioning.

### Server-side switch (deploy-time)

Switching the dev compose `nats:` service from no-auth to operator
mode is staged behind `VAULTDMS_NATS_OPERATOR_MODE=true`. When set,
`docker/nats/server.conf` is mounted, containing:

```
operator: /etc/nats/operator.jwt
resolver: {
    type: full
    dir: /etc/nats/jwts
}
resolver_preload: {
    AAA...: <SYS-account-jwt>
    BBB...: <PLATFORM-account-jwt>
}
```

Every Go service then connects with the platform account creds
mounted at `/etc/vaultdms/nats/platform.creds`. The switch is
intentionally a separate deploy because it touches every service's
NATS connection — keep it out of the streaming-feature wave so the
two changes can be rolled back independently.

In dev / no-auth mode (the default today), the issuance endpoint
still produces a creds file the customer can use against a future
operator-mode server; the polling endpoint and the live-tail SSE
work end-to-end against the current single-account stack.

### Polling endpoint

```
GET /api/v1/events?since=2026-05-13T00:00:00Z&limit=100&cursor=...
Authorization: Bearer tev_...
```

Response:

```json
{
  "events": [
    {
      "id":         "...",
      "type":       "dms.document.created.v1",
      "subject":    "tenant.<id>.events.document.created.v1",
      "occurred_at":"...",
      "data":       { ... original envelope ... }
    }
  ],
  "next_cursor": "...",
  "has_more":    true
}
```

Reads from the per-tenant stream via a durable consumer named
`poll-<token_id>`. Cursor is the JetStream sequence; resume after
restart is implicit because the consumer persists its position.
The endpoint enforces a hard cap of 1000 events / call and a
default of 100; the customer paginates via `next_cursor`.

### Live tail (admin UI only)

`GET /api/v1/admin/event-stream/tail` returns Server-Sent Events
for 60 s, then closes. Drives the live-tail panel in
`/admin/integrations/events`. Uses an *ephemeral* JetStream
consumer (auto-cleaned), so a noisy debugging session can't
shadow the customer's durable polling cursor.

### SDK samples

The admin page ships ready-to-paste connection code for the three
languages our customer base requests most:

- **Go** (`github.com/nats-io/nats.go`)
- **Python** (`nats-py`)
- **JS** (`@nats-io/nats-core`)

Each example demonstrates loading the creds file, subscribing to
`tenant.{id}.events.>`, and printing the envelope. A fourth
`curl` sample shows the polling fallback. All samples are sourced
from `web/src/routes/_authenticated/admin/integrations/events.tsx`
→ `SAMPLES` so future format tweaks change one place.

### Event-type reference

The admin page also lists the subject prefixes the platform emits
today (`dms.document.*`, `dms.version.*`, etc.) with one-line
descriptions. The list is hand-curated rather than scraped, because
the canonical list lives across many `pkg/events/*.go` constants and
emitting them all (including internal-only ones like
`dms.outbox.*`) would mislead customers.

## Consequences

- The mirror consumer is a single point of slowdown: if it falls
  behind, every customer's tail is delayed. Monitor lag via the
  existing JetStream consumer-info metrics.
- Per-tenant stream creation is lazy → tenants who never call
  "generate token" pay zero cost.
- The token table (`tenant_event_tokens`) stores hashed bearer
  tokens for the polling endpoint and the NKEY seed + JWT for
  NATS. Both rotate on `regenerate` (old creds revoked).
- Revoking a JWT after issuance requires either short expiry +
  rotation (current model — default 30d) or pushing a revocation
  to NATS's account JWT. We do the former; the latter is queued
  for the operator-mode deploy.
- Customers behind a firewall can run only on polling; we don't
  expose a websocket NATS bridge, and don't plan to until at
  least one customer asks.

## Not chosen

- **Outbox table replacement** — we deliberately mirror to NATS
  rather than to a per-tenant Postgres table. The poll endpoint
  is JetStream-backed because tracking per-tenant Postgres state
  for a hundred-thousand-event-per-day shape is wasteful when
  JetStream is already file-durable.
- **Direct WebSocket NATS bridge** — adds another auth shape and
  another service. The SSE live-tail covers debugging needs; the
  NATS protocol over its native TCP / WS is the customer-facing
  shape.
- **Per-event ACLs** — out of scope. A customer with a token
  for tenant T can read every event of theirs. Field-level
  filtering belongs in the customer's consumer.
