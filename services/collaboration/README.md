# collaboration

Real-time WebSocket hub for document collaboration — presence, typing
indicators, cursor positions, share-link live state.

## Responsibilities

- Accept authenticated WebSocket upgrades (session cookie validated
  against the auth service on handshake).
- Maintain per-document rooms in memory; broadcast events to
  connected peers in the same room.
- Emit presence heartbeats to Redis so other replicas can see who is
  online.
- Sticky-session routed by ingress cookie affinity — a given user's
  connections always land on the same pod while the cookie is valid.

## API surface

WebSocket endpoint: `ws://…/ws/collab/{document_id}` (upgraded
from HTTP via the `/ws` prefix in ingress).

Messages (JSON):

- Client → server: `cursor`, `typing`, `selection`, `save_snapshot`
- Server → client: `peer_join`, `peer_leave`, `peer_cursor`,
  `document_updated`

No REST. No NATS publish (collaboration state is ephemeral). Consumes
`dms.document.updated.v1` from NATS to broadcast the update to
connected clients.

## Dependencies

- **Redis**: pub/sub fanout across replicas
  (`collab:{tenant}:{doc_id}`), presence keys
  (`presence:{tenant}:{user}`).
- **auth service** HTTP for session validation on upgrade.
- **NATS** JetStream subscribe to `dms.document.updated.v1` for
  push-on-save.

## Configuration

- `WS_PORT` (default 8083)
- `REDIS_URL` (scheme-less `host:port`)
- `AUTH_SERVICE_URL` (e.g. `http://auth:8080`)

## Running locally

```bash
make up
cd services/collaboration && npm ci && npm run dev
# Or via compose:
docker compose --profile app up -d collaboration
```

## Testing

Hand-tested via the browser's DevTools WebSocket inspector. Automated
Node tests are not yet in scope.

## Deployment

`deploy/helm/vaultdms/templates/collaboration/` — deployment + hpa
(min 2, max 5 — sticky sessions), pdb, networkpolicy, servicemonitor.
Scale-down uses a 5-minute stabilization window to avoid dropping
live sessions mid-edit.

Ingress-level annotation `nginx.ingress.kubernetes.io/affinity: cookie`
pins a given browser to a single pod.

## Metrics

- `websocket_connections` (gauge) — scraped from Node's prometheus
  client
- `websocket_messages_total{direction=in|out,type=…}`

## Troubleshooting

**Peers don't see each other after reconnect** — the reconnect landed
on a different pod. Check ingress cookie affinity is enabled:
`kubectl get ingress vaultdms -o yaml | grep affinity`.

**High memory per pod** — each WS connection holds a room membership
struct. Expected ~10 KiB/user. If higher, look for leaked listeners
on disconnected sockets.

## Architecture

```mermaid
graph LR
  BR[Browser<br/>cookie+CSRF subprotocol] -->|WS upgrade| C(collab)
  C -->|verify cookie| AS[auth /auth/me]
  C -->|check document:read| POL[policy /permissions/check]
  C <-->|pub/sub fanout| RD[(Redis<br/>dms:{t}:doc:{d})]
  NE[NATS annotation events] -->|bridge| C
```

Client→server messages (zod-validated): `room.join`, `room.leave`, `comment.{create,update,delete}`, `typing.{start,stop}`. Server→client broadcasts mirror those with past-tense forms (`comment.created` etc) plus `presence.{joined,left}`. Schema violation → close code 4400; policy deny → 4003; auth fail → 4001.

## Env var reference

| Var | Purpose |
|---|---|
| `WS_PORT` (8083) | WebSocket listen port |
| `REDIS_URL` | cross-pod pub/sub + presence |
| `AUTH_SERVICE_URL` | `GET /auth/me` for cookie validation at upgrade |
| `POLICY_SERVICE_URL` | `document:read` check on `room.join` |
| `VAULTDMS_NATS_URL` | annotation-event bridge |

## On-call

No dedicated runbook. The WS service is stateless except for room membership; safe to restart. Cookie+CSRF verification short-circuits before any state is touched. Related: [runbook 05 — event pipeline](../../docs/runbooks/05-event-pipeline.md) for the NATS→Redis bridge.
