# Runbook — embed + Qdrant (Wave 5 Prompt 5.5)

**Last rehearsed:** not yet (cross-tenant live drill in Wave 13.3).
**On-call:** intelligence-platform team.

## What this covers

After `dms.version.ocr_completed.v1` the intelligence consumer enqueues
`generate_embeddings`. The hardened task chunks the OCR text
(sentence-packed into 512-token windows with 64-token overlap), embeds
in batches of 32, and upserts the vectors into the single Qdrant
collection `dms_vectors` with a tenant-scoped payload.

Deterministic point id: `uuid5(NAMESPACE_URL, "{tenant}/{doc}/{ver}/{i}")`.
**Because the tenant is in the id, two tenants uploading identical
documents cannot overwrite each other's vectors — even before the
search-side filter runs.**

### Completion event

`dms.embed.completed.v1` payload:
```
tenant_id, document_id, version_id, chunk_count, model_version, collection
```

## Cross-tenant isolation contract

The search service MUST include this filter in every Qdrant query:

```json
{"must": [{"key": "tenant_id", "match": {"value": "<caller-tenant>"}}]}
```

The embed task is the write side of this contract — every payload it
writes has a non-empty `tenant_id` (enforced at `build_payload` which
raises `ValueError` on empty). The read side is Prompt 5.5's
companion concern: any code path that queries Qdrant without the
tenant filter is a P0 security bug.

Unit-level enforcement (shipped): [tests/test_embed.py](../../services/intelligence/tests/test_embed.py) asserts payload shape + point-id tenancy.
Integration-level enforcement (Wave 13.1): 2 tenants upload identical
documents; each tenant's `/search` returns only their own hits.

## Metrics

| Metric | Type | Alert on |
|---|---|---|
| `embed_documents_total{status}` | Counter | failed rate > 5% for 5m |
| `embed_chunks_total` | Counter | throughput check; ~doc count × avg chunks/doc |
| `embed_duration_seconds` | Histogram | p95 > 120s |
| `embed_dlq_total{reason}` | Counter | any non-zero rate |

## Common failure modes

### Embed task always times out

The embedder is sync and the soft-limit is 600s. A 2000-page PDF
produces ~4000 chunks; at 50ms/chunk that's 200s — within budget. If
you're seeing timeouts, check:
1. Model weights weren't loaded at boot (cold-start cost per task).
2. GPU missing; the embedder falls back to CPU and slows ~10×.

### Chunks upserted but search returns nothing

Three possibilities:
1. **Search query missing tenant filter** — cross-check at search
   service; this would be an isolation bug.
2. **Embedding dim mismatch** — `VectorParams(size=embedding_dim)` in
   `_ensure_collection` doesn't match what the embedder actually
   emits. Drop the collection + re-ingest.
3. **Collection doesn't exist** — `_ensure_collection` was skipped
   (idempotency check raced). Manual:
   ```
   curl -s http://qdrant:6333/collections/dms_vectors | jq .status
   ```

### Terminal DLQ entries

Check `embed_dlq_total{reason}`:
- `timeout` — task hit 11-min hard cap. Document too large or embedder
  stuck. Investigate, replay manually via `dms-admin nats replay --stream INTEL_EVENTS_DLQ`.
- `model_error` — embedder crashed. Check worker logs for the full stack.
- `unknown` — unclassified; enrich `classify_error_reason` keyword list.

## Rollback

1. Revert `app/tasks/embed.py` and `app/chunker.py` to pre-5.5.
2. Qdrant collection `dms_vectors` stays populated — benign if embed
   stops; search would still read old vectors.
3. To purge entirely: `curl -X DELETE http://qdrant:6333/collections/dms_vectors`.

## Known deferred

- Per-tenant isolated collections for tenants that opt in
  (`isolated-vector-store`) — not implemented. Single collection +
  payload filter is the only mode today. Logged in out-of-scope.
- `page_number` in payload — OCR doesn't pass page maps forward, so
  this field is absent. Search hit rendering falls back to
  `start_char` / `end_char` offsets.
- `classification_top_1` in payload — would require an extra DB
  lookup per upsert; deferred as classification arrives on its own
  channel (`dms.classify.completed.v1`) and the search service can
  join at query time.
- True async embedding concurrency (the `EMBED_CONCURRENCY=4` constant
  is advisory until the embedder gains async support).
