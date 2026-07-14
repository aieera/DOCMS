# Clause Library Phases 2–4 + Approval — Design

Date: 2026-07-14
Status: Approved (user, this date)
Builds on: ADR 0104 Phase 1 (clauses CRUD, shipped). Explicitly excludes
the in-iframe OnlyOffice "insert clause" plugin (documentserver plugin
API + signed manifests — out of scope; the clause picker with one-click
copy is the substitute).

## Decisions taken

- **Detection engine: reuse chunk vectors.** Documents' chunks are
  already embedded into Qdrant by the existing `embed` task. Detection
  embeds each *clause* once (cached), then runs one Qdrant search per
  clause filtered to the new document's chunks. No re-embedding of
  documents, no second collection.
- **Threshold:** cosine ≥ `SEDOC_CLAUSE_MATCH_THRESHOLD` (default 0.80).
- **Ordering:** `detect_clauses` chains AFTER `embed` in the
  ocr_completed fan-out (needs chunks present); classify/NER/duplicate
  remain parallel.
- **Variations = normalized-text grouping** (lowercase, collapse
  whitespace, hash). No ML clustering (YAGNI).
- **Detection matches all non-deleted clauses**; approval state is a
  badge, not a filter.

## Components

### intelligence service
- `app/tasks/clause_match.py` — `detect_clauses(tenant_id, document_id,
  version_id)`:
  1. Load tenant clauses (id, body_text, sha256) from shared DB.
  2. Ensure `clause_embeddings` cache row per clause (re-embed inline
     when `body_sha256` differs — self-healing, covers pre-existing
     clauses with no backfill).
  3. Per clause: Qdrant search filtered to (tenant_id, document_id),
     top-1; score ≥ threshold → match.
  4. Idempotent write: DELETE existing `clause_matches` for the
     (tenant, document, version), INSERT fresh rows.
- Migration (intelligence): `clause_embeddings(tenant_id, clause_id,
  body_sha256, vector real[], updated_at)` + RLS, tenant_id first PK
  column. `real[]` (not pgvector) — it's a cache read back into Python,
  never searched in SQL.
- Consumer: chain `embed → detect_clauses` in `_on_ocr_completed`.

### document service
- Migration (next free number — 000098 at time of writing):
  `clause_matches(tenant_id, document_id, version_id,
  clause_id, similarity, chunk_index, matched_text, detected_at)` + RLS.
- `GET /api/v1/documents/{id}/clause-matches` — matches joined to
  clauses (name, jurisdiction, approved_at) for the panel.
- `GET /api/v1/clauses/{id}/variations` — variant groups: normalized
  text hash → {count, sample matched_text, similarity min/max, sample
  document_ids} ordered by count desc.
- `POST /api/v1/clauses/{id}/approve` / `DELETE .../approve` —
  admin/owner; sets/clears approved_by + approved_at.

### web
- `MatchedClausesPanel` on doc detail (pattern: FilingSuggestionPanel):
  name, similarity %, approval badge, snippet, Copy button.
- Clauses page: Approve/Revoke action + badge; per-row Copy button
  (clipboard) — the Phase-3 substitute; Usage view (variations) in the
  preview pane.

## Routing
Both new API paths live under `/api/v1/documents` and `/api/v1/clauses`
— already routed in vite/kong/routes.yaml. No gateway changes.

## Testing
- Python: detect-task unit tests (mock Qdrant + embed): threshold
  boundary, stale-cache re-embed, idempotent rewrite; normalizer tests.
- Go: approve endpoint role-gating; variations grouping; compile-time
  goldens where applicable.
- Web: vitest — panel renders matches/empty, copy calls clipboard.
- Live e2e: seed clause, upload doc containing it verbatim + a
  paraphrase, verify panel + variations populate.

## Non-goals
- OnlyOffice in-editor plugin (infrastructure-blocked).
- ML variation clustering.
- Approved-only detection filter (approval is display metadata for now).
