# search

Full-text search over documents, backed by OpenSearch + hybrid BM25 /
vector rerank.

## Responsibilities

- Index documents on every `dms.document.created.v1`,
  `dms.version.ocr_completed.v1`, `dms.permission.changed.v1`, and
  `dms.version.classified.v1` event.
- Serve REST search: keyword, phrase, facet filters, autocomplete,
  saved searches.
- Enforce ACL filtering at query time — every hit respects the
  caller's `readable_by` set.
- Partial updates (OCR content, permissions) without reindexing
  full documents.

## API surface

REST:

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/v1/search` | Primary search |
| GET | `/api/v1/search/autocomplete?q=` | Typeahead |
| POST | `/api/v1/saved-searches` | Persist a search |
| GET | `/api/v1/saved-searches` | List saved |
| DELETE | `/api/v1/saved-searches/{id}` | Delete saved |

NATS consumers (durable):

- `dms.document.created.v1` → index new document
- `dms.document.updated.v1` → reindex
- `dms.document.deleted.v1` → drop from index
- `dms.version.ocr_completed.v1` → partial update `content`
- `dms.permission.changed.v1` → partial update `readable_by`
- `dms.version.classified.v1` → partial update `document_class`
- `dms.version.entities_detected.v1` → partial update entities

## Dependencies

- **OpenSearch** index `dms_documents` (and `_saved_searches` for
  per-tenant saved queries).
- **Postgres**: `saved_searches` table.
- **NATS JetStream**: 8 durable subscriptions under the `DOCUMENTS`
  stream.

## Configuration

From `pkg/config` (prefix `VAULTDMS_`):

- `OPENSEARCH_URL`, `OPENSEARCH_USERNAME`, `OPENSEARCH_PASSWORD`
- `DATABASE_URL`, `REDIS_URL`, `NATS_URL`
- `HTTP_PORT` (default 8080), `GRPC_PORT` (9090), `HEALTH_PORT` (8081)

## Running locally

```bash
make up                  # infra + OpenSearch
make migrate             # document + search migrations
cd services/search && go run ./cmd/server
```

## Testing

```bash
make test-services       # all services
go test ./services/search/...
```

## Deployment

Helm: `deploy/helm/vaultdms/templates/search/` — full 6-resource set
(deployment, service, hpa, pdb, networkpolicy, servicemonitor).

## Metrics

- `search_latency_seconds{mode=keyword|semantic|hybrid}` — histogram
- `http_requests_total{path=/api/v1/search}`
- `event_bus_consumed_total{topic=dms.*}` per-subject index throughput

## Troubleshooting

**Documents disappear from search after upload**

The OCR pipeline is writing `dms.version.ocr_completed.v1` but the
search consumer is lagging. Check the NATS consumer for the
`search-dms.version.ocr_completed.v1` durable:

```
docker exec vaultdms-nats nats consumer info DOCUMENTS search-dms.version.ocr_completed.v1
```

**Search returns 0 hits for a user who should see the doc**

The indexed `readable_by` array is stale — the permission-changed
event was missed. Trigger a reindex:

```sql
-- Find the doc's tenant + id, then republish the permission state:
SELECT id, tenant_id FROM documents WHERE title = '...';
```

Republish from the policy service admin CLI or re-save the ACL.

**Slow autocomplete**

Autocomplete uses a completion suggester that's separate from the main
index. If the completion suggester isn't warm, the first query takes
seconds. Warm it with a scheduled GET on common prefixes or raise
`index.refresh_interval` on `_autocomplete`.
