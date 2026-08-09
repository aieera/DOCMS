# Runbook: search reindex (projection repair)

## What this repairs

### Missing dates / author / lifecycle on search hits (run this after upgrading)

Until the date fix, `dms.document.created.v1` and `dms.document.reindexed.v1`
carried no `created_at`, `updated_at` or `created_by_name`, and the search
service's `IndexDocument.CreatedAt` was a non-optional `time.Time`. Every
indexed document therefore stored the literal `0001-01-01T00:00:00Z`:

- search hits returned `"created_at":"0001-01-01T00:00:00Z"` (the Reports
  engine read the real dates straight from Postgres, which is why only
  search looked wrong);
- **Relevance / Newest / Oldest was inert** — the sort key was identical
  across the whole corpus;
- the `author` and `lifecycle_state` facets rendered blank because
  `created_by_name` / `lifecycle_state` were never projected.

Both events now carry those fields, so **newly created and newly edited
documents self-heal**. Documents indexed before the upgrade keep the bad
values until reindexed — run the whole-tenant pass below once per tenant
after deploying. The search service defensively renders a zero date as
`null` rather than year 1, so the UI degrades to "no date" until the
reindex lands.

### ACL / content wipe from the sparse-update bug

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
| created_at, updated_at | `documents` row (RFC3339; omitted when NULL rather than written as year 1) |
| created_by_name | `users.display_name` via `LEFT JOIN users ON (tenant_id, created_by)` |
| content, content_snippet | `ocr_results` pages of the current version (ordered, capped at 1 MB) |
| readable_by, readable_by_users, readable_by_groups | folder/workspace ACL (`computeFolderReaders` — same query that stamps created events) |
| version_count | `document_versions` count |

Each document is emitted as a `dms.document.reindexed.v1` event through
the **transactional outbox** (never a direct NATS publish); the search
indexer full-replaces the index doc from it. Batched at 200 docs per
transaction; safe to re-run (idempotent upserts).

## Running it

The endpoint lives on the internal mux (not routed by the gateway), so
the call must carry BOTH the shared gateway secret and the tenant
header — `X-Auth-Tenant-ID`, not the legacy `X-Tenant-ID`. Omitting
either returns 401.

Whole tenant:

```bash
curl -sf -X POST "http://document:8080/internal/v1/search/reindex" \
  -H "X-Gateway-Signature: $SEDOC_GATEWAY_SECRET" \
  -H "X-Auth-Tenant-ID: <tenant-uuid>" -H "X-User-ID: <admin-user-uuid>"
# → {"reindexed": <count>}   (counts only live documents: deleted_at IS NULL)
```

Single document (spot repair):

```bash
curl -sf -X POST "http://document:8080/internal/v1/search/reindex" \
  -H "X-Gateway-Signature: $SEDOC_GATEWAY_SECRET" \
  -H "X-Auth-Tenant-ID: <tenant-uuid>" -H "X-User-ID: <admin-user-uuid>" \
  -H "Content-Type: application/json" \
  -d '{"document_id": "<doc-uuid>"}'
```

All tenants (deploy repair — run once after shipping the indexer fix):

```bash
for t in $(psql "$DATABASE_URL" -Atc \
    "SELECT id FROM organizations WHERE deleted_at IS NULL"); do
  curl -sf -X POST "http://document:8080/internal/v1/search/reindex" \
    -H "X-Gateway-Signature: $SEDOC_GATEWAY_SECRET" \
    -H "X-Auth-Tenant-ID: $t" -H "X-User-ID: 00000000-0000-0000-0000-000000000000"
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
4. Date repair: `POST /api/v1/search` returns a real `created_at` on every
   hit (no `0001-01-01`), `sort_by=created_at` reorders the list, and the
   author / lifecycle facets have non-empty bucket values.

## Notes

- Load: one whole-tenant pass reads every live doc + its OCR text and
  writes one outbox row each. For very large tenants run off-peak; the
  batching keeps transactions short.
- Docs whose OCR never ran reindex with empty content (as they were);
  OCR re-emits content when it completes.
- The event subject is covered by the `DOC_EVENTS` stream binding
  (`dms.document.>`), so no NATS topology change is needed.
