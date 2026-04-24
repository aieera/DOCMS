# notification

In-app + real-time + email notifications, triggered by NATS events
across the platform.

## Responsibilities

- Consume `dms.notify.>` subjects; insert `notifications` rows;
  publish Redis pub/sub for real-time WebSocket delivery.
- Per-user preferences (per-channel enable, quiet hours).
- Email delivery (stub — integrates with SES / SendGrid).

## API surface

REST:

- `GET /api/v1/notifications` — paginated, with `read` filter
- `PATCH /api/v1/notifications/{id}/read`
- `POST /api/v1/notifications/read-all`
- `GET /api/v1/notifications/unread-count`
- `GET/PUT /api/v1/notifications/preferences`

NATS consumer: `dms.notify.>` (`dms.notify.signature_requested.v1`,
`dms.notify.workflow_assigned.v1`, etc.).

Redis pub/sub channel: `notif:{tenant}:{user}` — real-time fan-out
that the collaboration / WS service subscribes to for push.

## Dependencies

- **Postgres** tables: `notifications`, `notification_preferences`.
- **Redis** for real-time pub/sub.
- **NATS** stream `NOTIFICATIONS`.

## Configuration

Standard DB/Redis/NATS URLs, `VAULTDMS_HTTP_PORT`.

## Running locally

```bash
make up
( cd services/notification && VAULTDMS_HTTP_PORT=8086 go run ./cmd/server )
```

## Testing

```bash
make test-notification
```

## Deployment

`deploy/helm/vaultdms/templates/notification/` — full 6-resource set.

## Metrics

- `http_requests_total{path=/api/v1/notifications*}`
- `event_bus_consumed_total{topic=dms.notify.*}`

## Troubleshooting

**In-app notifications are delivered but emails aren't** — the email
channel is currently a log stub (`would send email`). Integrate with
SES / SendGrid in `services/notification/internal/service/service.go`
`Deliver` function.

**Redis pub/sub drops messages** — pub/sub is best-effort by design.
The `notifications` row is the durable record; any WS listener that
comes online after a missed publish reads from the row via the REST
list API.

## Architecture

```mermaid
graph LR
  NE[NATS dms.notify.&gt;] --> N(notification)
  N --> PG[(notifications<br/>preferences)]
  N -->|pub| RD[(Redis notif:{t}:{u})]
  RD -->|sub| WS[collab / web WS]
  N -->|stubbed| MAIL[email provider<br/>SES / SendGrid]
  CL[client REST] -->|list/read| N
```

## Env var reference

| Var | Purpose |
|---|---|
| `VAULTDMS_DATABASE_URL` | notifications + preferences |
| `VAULTDMS_REDIS_URL` | real-time pub/sub |
| `VAULTDMS_NATS_URL` | `dms.notify.>` subscribe |
| `VAULTDMS_HTTP_PORT` (8080) / `_HEALTH_PORT` (8081) | service ports |

## On-call

No dedicated runbook. Notifications are resilient by design — REST is the authoritative read path; Redis pub/sub is the push-delivery optimisation. Restart is safe.
