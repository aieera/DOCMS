# ADR 0104 — Clause library + reuse

Status: Accepted (Phases 1, 2, 4 + approval shipped; Phase 3 shipped as
copy-based picker — in-editor OnlyOffice plugin still deferred)
Date: 2026-05-19
Related: §18 Feature 11 of the blueprint; ADR 0052 (auto-tag),
ADR 0078 (LLM NER), ADR 0096 (Yjs collab — OnlyOffice bridge
deferred), ADR 0099 (contract intelligence graph).

## Context

Legal teams maintain a "library" of approved boilerplate clauses
(indemnification, governing law, force majeure, …). Today these live
in scattered Word documents on shared drives. §18 F11 asks for:

1. **First-class storage** — searchable, versioned, jurisdiction-tagged.
2. **Detection** — when a new contract uploads, find clauses that
   match the library and surface them; suggest adding novel clauses.
3. **Reuse** — in the OnlyOffice editor, "Insert clause" side-panel.
4. **Variation tracking** — when the same clause appears in many
   contracts, show usage frequency + textual variations.

Phase 1 ships the CRUD foundation (#1). The other three pieces depend
on infrastructure that doesn't fit a single session.

## What is shipped now (Phase 1)

- This ADR.
- Migration `000050_clauses.up.sql` — table with the shape the
  playbook asks for plus a tsvector for free-text search.
- `services/document/internal/handler/clauses_handler.go`:
  - `GET    /api/v1/clauses` — list + free-text search via `?q=`
  - `GET    /api/v1/clauses/{id}` — fetch one
  - `POST   /api/v1/clauses` — create (admin/owner only)
  - `PATCH  /api/v1/clauses/{id}` — update + auto-bump version
  - `DELETE /api/v1/clauses/{id}` — soft delete
- `web/src/routes/_authenticated/clauses/index.tsx` — table, search,
  create modal, jurisdiction/tag filter chips, body preview pane.
- Tile added to the main sidebar under Workspace group.

## What is **not** shipped now

- **Automated clause detection.** When a contract uploads, the system
  could scan it against the library and either (a) flag matches +
  highlight them in the viewer, or (b) suggest novel passages to add.
  Both need embedding-similarity OR a fine-tuned model. Phase 2 wires
  an `intelligence/app/tasks/clause_match.py` task that triggers on
  `dms.version.ocr_completed.v1`, splits the doc into sliding
  windows, embeds each, and queries Qdrant against a per-tenant
  clause-embedding collection. Out of scope today.
- **OnlyOffice "Insert clause" panel.** OnlyOffice's editor frame
  doesn't natively support custom plugins from inside an iframe —
  you have to use the documentserver plugin API, ship plugin JS to
  every document server, sign the manifest, and route insertions
  back via the Yjs bridge (ADR 0096 explicitly deferred this
  integration for the same protocol-mismatch reason). Phase 3 work.
- **Variation tracking across contracts.** Requires clause detection
  (above) to have run; then a separate pass clusters near-duplicate
  matches into a canonical clause + variation set. Phase 4.
- **Approval workflow.** `approved_by` column exists, but Phase 1
  doesn't wire it to anything — every clause is implicitly approved.
  Phase 2 adds a draft → in_review → approved workflow with
  notifications to designated reviewers.
- **Playwright.**

## DB schema

```sql
CREATE TABLE clauses (
    tenant_id      UUID        NOT NULL REFERENCES organizations(id),
    id             UUID        NOT NULL DEFAULT gen_random_uuid(),
    name           TEXT        NOT NULL,                       -- "Indemnification — mutual"
    body_text      TEXT        NOT NULL,                       -- the clause copy
    jurisdiction   TEXT        NOT NULL DEFAULT '',            -- "US-CA", "EU", "" = any
    tags           TEXT[]      NOT NULL DEFAULT '{}',
    version        INTEGER     NOT NULL DEFAULT 1,
    approved_by    UUID,                                       -- nullable until reviewed
    approved_at    TIMESTAMPTZ,
    created_by     UUID        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ,
    -- Materialized FTS column kept in sync via trigger so search
    -- queries are a single index scan rather than recomputing
    -- to_tsvector per row.
    search_tsv     TSVECTOR
                   GENERATED ALWAYS AS (
                       to_tsvector('english',
                           coalesce(name, '') || ' ' ||
                           coalesce(body_text, '') || ' ' ||
                           array_to_string(tags, ' ')
                       )
                   ) STORED,
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX idx_clauses_search ON clauses USING GIN (search_tsv);
CREATE INDEX idx_clauses_tenant ON clauses (tenant_id, deleted_at, updated_at DESC);

ALTER TABLE clauses ENABLE ROW LEVEL SECURITY;
ALTER TABLE clauses FORCE ROW LEVEL SECURITY;
CREATE POLICY clauses_tenant_isolation ON clauses
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));
```

Generated-column tsvector means CRUD writes don't need to maintain
the index manually; Postgres recomputes on row write. Per Phase 1
typical library sizes (low thousands of clauses per tenant), insert
overhead is negligible.

## API shape

`GET /api/v1/clauses?q=indemnify&jurisdiction=US-CA&tag=mutual&limit=50`

```json
{
  "clauses": [
    {
      "id": "uuid",
      "name": "Mutual Indemnification",
      "body_text": "Each party agrees to indemnify…",
      "jurisdiction": "US-CA",
      "tags": ["indemnification", "mutual"],
      "version": 3,
      "approved_by": "user-uuid",
      "approved_at": "2026-05-01T12:00:00Z",
      "created_by": "user-uuid",
      "created_at": "...",
      "updated_at": "...",
      "search_rank": 0.43
    }
  ],
  "total": 12
}
```

PATCH bumps `version` automatically. Soft delete sets `deleted_at`;
GET hides soft-deleted rows by default (admin recovery flow is
Phase 2).

## Open questions deferred

- **Per-jurisdiction inheritance.** Real legal libraries have parent
  templates that get overridden by jurisdiction-specific variants
  (US base → US-CA override). Phase 1 treats every jurisdiction-
  scoped clause as a flat row; Phase 5 adds inheritance + an effective-
  clause resolver.
- **Markdown vs rich text body.** Today `body_text` is plain text.
  Lawyers want bold/italic at minimum. Phase 2 swaps to a tiptap-
  compatible JSON or stores as DOCX-fragment.
- **Cross-tenant template marketplace.** Some tenants (especially
  smaller firms) would benefit from a curated starter library. Out of
  scope; the multi-tenant access model would need a new policy layer.

## Update (2026-07-14)

Phases 2 and 4, plus the approval workflow, are shipped and have been
verified live end-to-end against the dev stack (rebuilt `document`,
`intelligence`, `intelligence-worker`, `intelligence-worker-misc`).
Phase 3 ("Insert clause" reuse) shipped as a copy-based picker on the
clauses page rather than the in-editor OnlyOffice plugin originally
scoped — see below.

- **Detection task, chained after embeddings.**
  `services/intelligence/app/tasks/clause_match.py::detect_clauses` is
  chained AFTER `generate_embeddings` in
  `nats_consumer.py::_on_ocr_completed` — `.si()` immutable signatures
  so the chain doesn't race the Qdrant chunk upsert:
  ```python
  chain(
      generate_embeddings.si(**hardened_kwargs).set(queue="intelligence-embed"),
      detect_clauses.si(tenant_id=tid, document_id=did, version_id=vid)
          .set(queue="intelligence"),
  ).apply_async()
  ```
  For each non-deleted clause in the tenant's library, the task ensures
  a cached embedding (self-healing on edit via a `body_sha256` check),
  runs one Qdrant search per clause filtered to the document+version's
  own chunks, and records a `clause_matches` row when cosine similarity
  ≥ `settings.clause_match_threshold`. Idempotent per
  (tenant, document, version): delete-then-insert.

  **Fix applied during this verification pass:** the task was
  correctly written and correctly chained, but
  `services/intelligence/app/worker.py`'s Celery `include=[...]` list
  never listed `app.tasks.clause_match`, so neither worker process
  imported the module and Celery raised `Received unregistered task of
  type 'app.tasks.clause_match.detect_clauses'` (KeyError) for every
  document — the entire detection pipeline was silently dead end-to-end
  despite the task and chain both being implemented correctly. Added
  `"app.tasks.clause_match"` to that list; rebuilt
  `intelligence`/`intelligence-worker`/`intelligence-worker-misc` and
  confirmed via `celery -A app.worker inspect registered` that both
  worker processes now register the task.

- **Storage.** `clause_embeddings` (intelligence migrations
  000004/000005) caches one embedding vector per clause, keyed by
  `body_sha256` so an edited clause re-embeds lazily on next detection
  run rather than via a background job. `clause_matches` (document
  migrations 000098/000099) stores one row per (tenant, document,
  version, clause) with `similarity`, `chunk_index`, and
  `matched_text`, plus an FK to `document_versions` added in 000099
  after an early review caught the missing constraint.

- **REST endpoints** (`services/document/internal/handler/clause_matches_handler.go`):
  - `GET    /api/v1/documents/{id}/clause-matches` — detection results
    for a document, scoped to `current_version_id` so a superseded
    version's stale matches never surface in the panel.
  - `GET    /api/v1/clauses/{id}/variations` — Phase 4 usage tracking;
    groups matches across documents by a normalized-text hash
    (`md5(btrim(regexp_replace(lower(matched_text), '\s+', ' ', 'g')))`,
    kept byte-equivalent with `clause_match.py::_normalize_text`) into
    variation clusters with `occurrences`, `min_similarity`/
    `max_similarity`, and a sample snippet.
  - `POST   /api/v1/clauses/{id}/approve` — admin/owner only; stamps
    `approved_by`/`approved_at` on the clause.
  - `DELETE /api/v1/clauses/{id}/approve` — admin/owner only; clears
    both fields.

- **Web UI.** `MatchedClausesPanel` (document detail sidebar)
  self-hides when there are no matches; shows similarity %, an
  Approved badge, jurisdiction, the matched text, and a Copy button.
  The clauses list page (`clauses/index.tsx`) gained: a header
  Approve/Revoke toggle gated on `role === 'admin' || role === 'owner'`,
  a Copy-body button, and a "Usage" section backed by the variations
  endpoint ("Used in N documents" + per-variation occurrence/similarity
  rows) — this is the Phase 3 substitute: reuse via copy-to-clipboard
  from the library rather than an in-editor "Insert clause" side-panel.
  The OnlyOffice plugin-API integration remains deferred for the same
  reason ADR 0096 deferred it (no supported way to ship custom plugin
  JS into the iframed documentserver without a signed manifest + a
  Yjs-bridge round trip).

- **Threshold.** `SEDOC_CLAUSE_MATCH_THRESHOLD` (Python
  `settings.clause_match_threshold`, default `0.80`) is the single
  cosine-similarity cutoff used both when recording a `clause_matches`
  row and, implicitly, everywhere matches are read back (there is no
  separate read-side filter — a row's existence means it cleared the
  threshold at detection time).

- **Version-scoping guarantees.** Two independent places enforce that
  matches never leak across versions of the same document: (1) the
  Qdrant search filter in `_matches_for_clauses` requires an exact
  `(tenant_id, document_id, version_id)` match on the chunk payload —
  points from prior versions persist in Qdrant under different
  UUIDv5 ids and are excluded by the filter, not just outranked; (2)
  the document-service panel query joins on `documents.current_version_id`
  rather than "latest version_id for this document," so superseding a
  version immediately (not eventually-consistently) hides that
  version's matches from the panel even if its `clause_matches` rows
  are still on disk.

- **Live e2e verification (2026-07-14).** Uploaded a small PDF (via
  the same `createDocument → storage/uploads/initiate → PUT → complete
  → createVersion` sequence the web app's `useUpload` hook uses)
  containing the seeded "Confidentiality — Standard" clause's
  `body_text` verbatim. Confirmed a `clause_matches` row at similarity
  0.892 (≥ 0.80), the `/clause-matches` and `/variations` endpoints
  both returning 200 with the expected shape, and a full
  approve → revoke → approve round trip via the REST endpoints with
  the panel reflecting each state change. One calibration note worth
  keeping in mind for future test documents: because the intelligence
  chunker (`app/chunker.py`) packs up to 512 tokens per chunk and a
  short one-page test document embeds as a single chunk, padding the
  clause with more than one or two sentences of unrelated filler text
  measurably dilutes the mean-pooled sentence-embedding similarity
  below the 0.80 threshold — a real multi-page contract naturally
  avoids this because each clause-sized window becomes its own chunk.

Known limitation found in the same live pass (not clause-specific):
the intelligence NATS consumer's OCR mime-gate does not admit
`text/plain` uploads, so plain-text documents never reach OCR — and
therefore never reach embeddings or clause detection. PDFs and images
flow normally. Tracked as a general OCR-pipeline follow-up, outside
this ADR's scope.
