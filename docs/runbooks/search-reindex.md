# Runbook: search reindex (projection repair)

## What this repairs

The OpenSearch index is a projection fed by NATS events. Before the
indexer partial-update fix, `dms.document.updated.v1` (a **sparse** diff:
`{document_id, changed_fields, updated_by}`) was routed through the same
**full-replace** path as created events — so any metadata edit (rename,
tag change, description edit, bulk metadata update) rebuilt the index doc
from an empty projection:

- `readable_by` / `readable_by_users` / `readable_by_groups` wiped → the
  doc matched **no user's ACL filter** and vanished from everyone's
  search results;
- `content` / `content_snippet` (OCR text) wiped → even after an ACL
  repair the doc was no longer findable by content.

The indexer fix stops new wipes. **This runbook repairs docs that were
already wiped** — and is the standing tool for any future projection
drift.

## How it works

`POST /internal/v1/search/reindex` on the **document service** rebuilds
the full search projection from source of truth, per live document:

| Index field | Source of truth |
|---|---|
| title, description, tags, document_class, lifecycle_state, region_pin, mime_type, size_bytes | `documents` row |
| content, content_snippet | `ocr_results` pages of the current version (ordered, capped at 1 MB) |
| readable_by, readable_by_users, readable_by_groups | folder/workspace ACL (`computeFolderReaders` — same query that stamps created events) |
| version_count | `document_versions` count |

Each document is emitted as a `dms.document.reindexed.v1` event through
the **transactional outbox** (never a direct NATS publish); the search
indexer full-replaces the index doc from it. Batched at 200 docs per
transaction; safe to re-run (idempotent upserts).

## Running it

The endpoint lives on the internal mux (not routed by the gateway).
Same calling convention as the records cutoff-sweep CronJob: in-cluster
call with the internal identity headers carrying the tenant.

Whole tenant:

```bash
curl -sf -X POST "http://document:8080/internal/v1/search/reindex" \
  -H "X-Tenant-ID: <tenant-uuid>" -H "X-User-ID: <admin-user-uuid>"
# → {"reindexed": <count>}
```

Single document (spot repair):

```bash
curl -sf -X POST "http://document:8080/internal/v1/search/reindex" \
  -H "X-Tenant-ID: <tenant-uuid>" -H "X-User-ID: <admin-user-uuid>" \
  -H "Content-Type: application/json" \
  -d '{"document_id": "<doc-uuid>"}'
```

All tenants (deploy repair — run once after shipping the indexer fix):

```bash
for t in $(psql "$DATABASE_URL" -Atc \
    "SELECT id FROM organizations WHERE deleted_at IS NULL"); do
  curl -sf -X POST "http://document:8080/internal/v1/search/reindex" \
    -H "X-Tenant-ID: $t" -H "X-User-ID: 00000000-0000-0000-0000-000000000000"
  echo " tenant $t done"
done
```

Local stack: `http://localhost:8081` (document service host port) works
the same way.

## Verifying

1. Response `reindexed` count ≈ live doc count for the tenant
   (`SELECT count(*) FROM documents WHERE tenant_id='…' AND deleted_at IS NULL`).
2. Outbox drains: `SELECT count(*) FROM outbox WHERE event_type='dms.document.reindexed.v1' AND NOT published` → 0 within seconds.
3. A previously-vanished doc is findable again by an ACL-permitted user
   (search by a content phrase, not just the title).

## Notes

- Load: one whole-tenant pass reads every live doc + its OCR text and
  writes one outbox row each. For very large tenants run off-peak; the
  batching keeps transactions short.
- Docs whose OCR never ran reindex with empty content (as they were);
  OCR re-emits content when it completes.
- The event subject is covered by the `DOC_EVENTS` stream binding
  (`dms.document.>`), so no NATS topology change is needed.
