# signature

E-signature requests + signer tracking + internal PDF signing + external
provider adapters (DocuSign, Adobe Sign).

## Responsibilities

- Create signature request (signers + order); generate per-signer
  signing URL with a random token.
- Track signer status: `pending → signed | declined`.
- On all signers complete: mark request `completed`, publish outbox
  event `dms.signature.completed.v1`.
- Notify signers via the notification service (outbox event
  `dms.notify.signature_requested.v1` — see
  [03c](../../docs/audit/remediation/03c-middleware-outbox.md)).
- Verify embedded signatures on a document (stub — real impl uses
  PDF signing lib).

## API surface

REST:

- `POST /api/v1/signatures/requests` — create
- `GET /api/v1/signatures/requests/{id}`
- `GET /api/v1/signatures/document/{documentId}` — list by doc
- `POST /api/v1/signatures/requests/{id}/sign/{signerId}`
- `POST /api/v1/signatures/requests/{id}/cancel`
- `GET /api/v1/signatures/verify/{documentId}`

Outbox publishes:

- `dms.notify.signature_requested.v1`
- `dms.signature.completed.v1`

## Dependencies

- **Postgres** tables: `signature_requests`, `outbox`.
- **S3**: stores signed PDFs (internal provider).
- **NATS** stream `WORKFLOWS` for published events.

## Configuration

Standard DB/Redis/NATS. No signature-specific env.

## Running locally

```bash
make up
( cd services/signature && SEDOC_HTTP_PORT=8087 go run ./cmd/server )
```

## Testing

```bash
make test-signature
```

## Deployment

`deploy/helm/sedoc/templates/signature/` — full 6-resource set.
HPA disabled by default (low-volume service).

## Metrics

- `http_requests_total{path=/api/v1/signatures*}`
- `event_bus_published_total{topic=dms.signature.*}`

## Troubleshooting

**Request stuck in pending, no notification sent** — outbox publisher
is the delivery mechanism. Check `SELECT * FROM outbox WHERE
event_type='dms.notify.signature_requested.v1' AND published=false`.

**SigningURL leaks** — URLs contain a 16-byte hex token. If leaked,
anyone with the URL can sign. To revoke, cancel the request via
`POST /…/cancel` which marks the row `cancelled`; the sign endpoint
rejects any subsequent POST.
