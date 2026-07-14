# Clause Library Phases 2–4 + Approval — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Clause auto-detection on upload (Phase 2), variation tracking (Phase 4), approve/revoke workflow, and a copy-based clause picker (Phase 3 substitute), per `docs/superpowers/specs/2026-07-14-clause-phases-design.md`.

**Architecture:** A new `detect_clauses` Celery task chains after `generate_embeddings` in the intelligence OCR fan-out; it matches cached clause embeddings against the document's already-embedded chunks in Qdrant (cosine ≥ threshold) and writes `clause_matches` rows to the shared Postgres. The document service serves matches / variations / approval over REST; the web app adds a doc-detail panel and clause-page actions.

**Tech Stack:** Python (Celery, qdrant-client, asyncpg), Go 1.25 (net/http mux, pgx), React/TanStack Query, golang-migrate.

## Global Constraints

- Tenant isolation: every new table has `tenant_id` first in PK + RLS policy; all Go queries run inside `database.WithTenantTx`; Python DB access sets `app.current_tenant` via `set_config` (copy existing idioms).
- Threshold env: `SEDOC_CLAUSE_MATCH_THRESHOLD`, pydantic field `clause_match_threshold: float = 0.80`.
- Idempotency: detection DELETEs then INSERTs matches for `(tenant_id, document_id, version_id)`.
- No new gateway/vite routes — new endpoints live under `/api/v1/documents` and `/api/v1/clauses` (already routed).
- TDD: every code task writes the failing test first and runs it.
- Commit after each task (feature branch only, no push).

---

### Task 1: Migrations — `clause_matches` (document) + `clause_embeddings` (intelligence)

**Files:**
- Create: `services/document/migrations/000098_clause_matches.up.sql`
- Create: `services/document/migrations/000098_clause_matches.down.sql`
- Create: `services/intelligence/migrations/000004_clause_embeddings.up.sql`
- Create: `services/intelligence/migrations/000004_clause_embeddings.down.sql`

**Interfaces:**
- Produces: tables `clause_matches(tenant_id uuid, document_id uuid, version_id uuid, clause_id uuid, similarity real, chunk_index int, matched_text text, detected_at timestamptz)` and `clause_embeddings(tenant_id uuid, clause_id uuid, body_sha256 text, vector real[], updated_at timestamptz)`.

- [ ] **Step 1: Write `000098_clause_matches.up.sql`**

```sql
-- ADR 0104 Phase 2 — clause detection results. Written by the
-- intelligence detect_clauses task (shared DB, same pattern as
-- extracted_fields), read by the document service panel + variations
-- endpoints. Idempotent per (tenant, document, version): the task
-- deletes then re-inserts.
CREATE TABLE IF NOT EXISTS clause_matches (
    tenant_id    uuid        NOT NULL REFERENCES organizations(id),
    id           uuid        NOT NULL DEFAULT gen_random_uuid(),
    document_id  uuid        NOT NULL,
    version_id   uuid        NOT NULL,
    clause_id    uuid        NOT NULL,
    similarity   real        NOT NULL,
    chunk_index  integer     NOT NULL DEFAULT 0,
    matched_text text        NOT NULL DEFAULT '',
    detected_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, clause_id) REFERENCES clauses(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_clause_matches_doc
    ON clause_matches (tenant_id, document_id, version_id);
CREATE INDEX IF NOT EXISTS idx_clause_matches_clause
    ON clause_matches (tenant_id, clause_id, detected_at DESC);

ALTER TABLE clause_matches ENABLE ROW LEVEL SECURITY;
ALTER TABLE clause_matches FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS clause_matches_tenant_isolation ON clause_matches;
CREATE POLICY clause_matches_tenant_isolation ON clause_matches
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

- [ ] **Step 2: Write `000098_clause_matches.down.sql`**

```sql
DROP TABLE IF EXISTS clause_matches;
```

- [ ] **Step 3: Write `000004_clause_embeddings.up.sql`**

```sql
-- ADR 0104 Phase 2 — per-clause embedding cache. Self-healing: the
-- detect_clauses task re-embeds any clause whose body_sha256 differs
-- from the cached row (covers pre-existing clauses, no backfill).
-- real[] not pgvector: the vector is read back into Python for a
-- Qdrant query, never searched in SQL.
CREATE TABLE IF NOT EXISTS clause_embeddings (
    tenant_id   uuid        NOT NULL,
    clause_id   uuid        NOT NULL,
    body_sha256 text        NOT NULL,
    vector      real[]      NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, clause_id)
);

ALTER TABLE clause_embeddings ENABLE ROW LEVEL SECURITY;
ALTER TABLE clause_embeddings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS clause_embeddings_tenant_isolation ON clause_embeddings;
CREATE POLICY clause_embeddings_tenant_isolation ON clause_embeddings
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

- [ ] **Step 4: Write `000004_clause_embeddings.down.sql`**

```sql
DROP TABLE IF EXISTS clause_embeddings;
```

- [ ] **Step 5: Apply + verify against the dev DB**

Run (repo root):
```bash
migrate -database "postgres://sedoc:devpassword@localhost:15432/sedoc?sslmode=disable&x-migrations-table=schema_migrations" -path services/document/migrations up
migrate -database "postgres://sedoc:devpassword@localhost:15432/sedoc?sslmode=disable&x-migrations-table=intelligence_schema_migrations" -path services/intelligence/migrations up
docker exec sedoc-postgres sh -c 'psql -U $POSTGRES_USER -d $POSTGRES_DB -tAc "SELECT to_regclass('"'"'clause_matches'"'"'), to_regclass('"'"'clause_embeddings'"'"');"'
```
Expected: `clause_matches|clause_embeddings`

- [ ] **Step 6: Commit**

```bash
git add services/document/migrations/000098_* services/intelligence/migrations/000004_*
git commit -m "feat(clauses): clause_matches + clause_embeddings tables (ADR 0104 Phase 2)"
```

---

### Task 2: Intelligence — `detect_clauses` task (TDD)

**Files:**
- Create: `services/intelligence/app/tasks/clause_match.py`
- Create: `services/intelligence/tests/test_clause_match.py`
- Modify: `services/intelligence/app/config.py` (add `clause_match_threshold: float = 0.80` next to the other thresholds)

**Interfaces:**
- Consumes: `app.models.embedder.embed_single(text: str) -> list[float]`; `app.tasks.embed._qdrant()`; `settings.qdrant_collection`; Task 1 tables.
- Produces: Celery task `app.tasks.clause_match.detect_clauses(tenant_id, document_id, version_id)`; pure helpers `_body_sha(text) -> str`, `_normalize_text(text) -> str` (used by Task 4's SQL contract: Go normalizes identically — lowercase + collapsed whitespace).

- [ ] **Step 1: Write failing tests `tests/test_clause_match.py`**

```python
"""detect_clauses — pure helpers + orchestration (mocked I/O)."""
from unittest.mock import patch, MagicMock

from app.tasks.clause_match import (
    _body_sha,
    _normalize_text,
    _matches_for_clauses,
)


def test_body_sha_stable_and_content_sensitive():
    a = _body_sha("Governing law shall be England.")
    assert a == _body_sha("Governing law shall be England.")
    assert a != _body_sha("Governing law shall be Wales.")


def test_normalize_collapses_case_and_whitespace():
    assert _normalize_text("  Force\n\nMAJEURE   event ") == "force majeure event"


def _mk_hit(score, chunk_index=3, text="matched snippet"):
    h = MagicMock()
    h.score = score
    h.payload = {"chunk_index": chunk_index, "text_snippet": text}
    return h


def test_matches_respect_threshold_boundary():
    client = MagicMock()
    # clause A scores above, clause B exactly at threshold, C below.
    client.search.side_effect = [[_mk_hit(0.91)], [_mk_hit(0.80)], [_mk_hit(0.79)]]
    clauses = [
        {"id": "a", "vector": [0.1]},
        {"id": "b", "vector": [0.2]},
        {"id": "c", "vector": [0.3]},
    ]
    out = _matches_for_clauses(
        client, tenant_id="t1", document_id="d1", clauses=clauses, threshold=0.80,
    )
    assert [m["clause_id"] for m in out] == ["a", "b"]
    assert out[0]["similarity"] == 0.91
    assert out[0]["chunk_index"] == 3
    assert out[0]["matched_text"] == "matched snippet"


def test_no_hits_yield_no_matches():
    client = MagicMock()
    client.search.return_value = []
    out = _matches_for_clauses(
        client, tenant_id="t1", document_id="d1",
        clauses=[{"id": "a", "vector": [0.1]}], threshold=0.80,
    )
    assert out == []
```

- [ ] **Step 2: Run to verify failure**

Run: `docker cp services/intelligence/tests sedoc-intelligence:/app/ && docker exec sedoc-intelligence sh -c 'cd /app && python -m pytest tests/test_clause_match.py -q'`
Expected: collection ERROR — `No module named 'app.tasks.clause_match'`

- [ ] **Step 3: Implement `app/tasks/clause_match.py`**

```python
"""ADR 0104 Phase 2 — clause detection against uploaded documents.

Chained AFTER generate_embeddings in the ocr_completed fan-out (the
document's chunks must already be in Qdrant). For each non-deleted
clause in the tenant's library: ensure a cached embedding
(clause_embeddings, keyed by body_sha256 — self-healing on edit), run
one Qdrant search filtered to this document's chunks, and record a
clause_matches row when cosine score >= settings.clause_match_threshold.
Idempotent per (tenant, document, version): delete-then-insert.
"""
from __future__ import annotations

import asyncio
import hashlib
import logging
import re

from app.config import settings
from app.models.embedder import embed_single
from app.tasks.embed import _qdrant
from app.worker import celery_app

from qdrant_client.models import FieldCondition, Filter, MatchValue

log = logging.getLogger(__name__)


def _body_sha(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def _normalize_text(text: str) -> str:
    """Lowercase + collapse whitespace. The document service's
    variations endpoint groups by md5(this) — keep the two in sync."""
    return re.sub(r"\s+", " ", text.strip().lower())


def _matches_for_clauses(client, *, tenant_id: str, document_id: str,
                         clauses: list[dict], threshold: float) -> list[dict]:
    """One Qdrant search per clause, filtered to this document's chunks.
    Returns match dicts for hits at/above threshold."""
    out: list[dict] = []
    flt = Filter(must=[
        FieldCondition(key="tenant_id", match=MatchValue(value=tenant_id)),
        FieldCondition(key="document_id", match=MatchValue(value=document_id)),
    ])
    for c in clauses:
        hits = client.search(
            collection_name=settings.qdrant_collection,
            query_vector=c["vector"],
            query_filter=flt,
            limit=1,
        )
        if not hits:
            continue
        top = hits[0]
        if top.score >= threshold:
            payload = top.payload or {}
            out.append({
                "clause_id": c["id"],
                "similarity": float(top.score),
                "chunk_index": int(payload.get("chunk_index", 0)),
                "matched_text": (payload.get("text_snippet")
                                 or payload.get("text") or ""),
            })
    return out


async def _load_clauses_with_vectors(tenant_id: str) -> list[dict]:
    """Load non-deleted clauses + cached embeddings; re-embed stale or
    missing entries inline and upsert the cache."""
    from app.db.pool import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id)
            rows = await conn.fetch(
                """
                SELECT c.id::text AS id, c.body_text,
                       e.body_sha256, e.vector
                  FROM clauses c
             LEFT JOIN clause_embeddings e
                    ON e.tenant_id = c.tenant_id AND e.clause_id = c.id
                 WHERE c.tenant_id = $1 AND c.deleted_at IS NULL
                """,
                tenant_id)
    out: list[dict] = []
    stale: list[tuple[str, str, list[float]]] = []
    for r in rows:
        sha = _body_sha(r["body_text"])
        if r["body_sha256"] == sha and r["vector"]:
            out.append({"id": r["id"], "vector": list(r["vector"])})
            continue
        vec = embed_single(r["body_text"])
        out.append({"id": r["id"], "vector": vec})
        stale.append((r["id"], sha, vec))
    if stale:
        async with pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_tenant', $1, true)", tenant_id)
                for cid, sha, vec in stale:
                    await conn.execute(
                        """
                        INSERT INTO clause_embeddings
                            (tenant_id, clause_id, body_sha256, vector, updated_at)
                        VALUES ($1, $2, $3, $4, now())
                        ON CONFLICT (tenant_id, clause_id) DO UPDATE
                           SET body_sha256 = EXCLUDED.body_sha256,
                               vector      = EXCLUDED.vector,
                               updated_at  = now()
                        """,
                        tenant_id, cid, sha, vec)
    return out


async def _replace_matches(tenant_id: str, document_id: str, version_id: str,
                           matches: list[dict]) -> None:
    from app.db.pool import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id)
            await conn.execute(
                """
                DELETE FROM clause_matches
                 WHERE tenant_id = $1 AND document_id = $2 AND version_id = $3
                """,
                tenant_id, document_id, version_id)
            for m in matches:
                await conn.execute(
                    """
                    INSERT INTO clause_matches
                        (tenant_id, document_id, version_id, clause_id,
                         similarity, chunk_index, matched_text)
                    VALUES ($1, $2, $3, $4, $5, $6, $7)
                    """,
                    tenant_id, document_id, version_id, m["clause_id"],
                    m["similarity"], m["chunk_index"], m["matched_text"])


@celery_app.task(
    name="app.tasks.clause_match.detect_clauses",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
)
def detect_clauses(self, tenant_id: str, document_id: str, version_id: str,
                   **_ignored) -> dict:
    """**_ignored swallows chain-passed args (celery chains pass the
    parent task's return as the first positional unless .si() is used;
    the consumer uses .si(), but keep the guard for manual dispatch)."""
    clauses = asyncio.run(_load_clauses_with_vectors(tenant_id))
    if not clauses:
        return {"matches": 0, "clauses": 0}
    client = _qdrant()
    matches = _matches_for_clauses(
        client, tenant_id=tenant_id, document_id=document_id,
        clauses=clauses, threshold=settings.clause_match_threshold,
    )
    asyncio.run(_replace_matches(tenant_id, document_id, version_id, matches))
    log.info("clause detection: %d/%d matched for doc %s",
             len(matches), len(clauses), document_id)
    return {"matches": len(matches), "clauses": len(clauses)}
```

- [ ] **Step 4: Add the setting in `app/config.py`** (next to `extraction_regex_confidence`)

```python
    # ADR 0104 Phase 2 — minimum cosine score for a clause-library match
    # against a document chunk.
    clause_match_threshold: float = 0.80
```

- [ ] **Step 5: Run tests to verify pass**

Run: `docker cp services/intelligence/app sedoc-intelligence:/app/ && docker cp services/intelligence/tests sedoc-intelligence:/app/ && docker exec sedoc-intelligence sh -c 'cd /app && python -m pytest tests/test_clause_match.py -q'`
Expected: `4 passed`

- [ ] **Step 6: Full intelligence suite regression**

Run: `docker exec sedoc-intelligence sh -c 'cd /app && python -m pytest tests -q'`
Expected: only pre-existing `test_ner_llm_live` failure (Anthropic credits).

- [ ] **Step 7: Commit**

```bash
git add services/intelligence/app/tasks/clause_match.py services/intelligence/tests/test_clause_match.py services/intelligence/app/config.py
git commit -m "feat(intelligence): clause detection task (ADR 0104 Phase 2)"
```

---

### Task 3: Chain detection after embed in the NATS consumer

**Files:**
- Modify: `services/intelligence/app/nats_consumer.py` (the `_on_ocr_completed` dispatch block, ~line 419)

**Interfaces:**
- Consumes: `detect_clauses` from Task 2.
- Produces: `chain(generate_embeddings.si(...), detect_clauses.si(...))` dispatch.

- [ ] **Step 1: Write the failing test** — append to `services/intelligence/tests/test_clause_match.py`:

```python
def test_consumer_chains_detect_after_embed():
    """detect_clauses must run AFTER embeddings exist in Qdrant, so the
    consumer dispatches chain(embed → detect), not a parallel task."""
    import inspect
    from app import nats_consumer
    src = inspect.getsource(nats_consumer)
    assert "detect_clauses" in src, "consumer must dispatch clause detection"
    assert "chain(" in src, "detection must be chained after embeddings"
```

- [ ] **Step 2: Run to verify failure**

Run: `docker cp services/intelligence/tests sedoc-intelligence:/app/ && docker exec sedoc-intelligence sh -c 'cd /app && python -m pytest tests/test_clause_match.py::test_consumer_chains_detect_after_embed -q'`
Expected: FAIL — "consumer must dispatch clause detection"

- [ ] **Step 3: Modify `_on_ocr_completed`** — replace the single `generate_embeddings.apply_async(...)` line with a chain (imports: add `from celery import chain` and `from app.tasks.clause_match import detect_clauses` next to the other task imports at the top of the file):

```python
            # Embeddings first, THEN clause detection (ADR 0104 Phase 2):
            # detect_clauses searches this document's chunk vectors, so it
            # must not race the upsert. .si() = immutable signature (no
            # parent-result injection).
            chain(
                generate_embeddings.si(**hardened_kwargs).set(queue="intelligence-embed"),
                detect_clauses.si(
                    tenant_id=tid, document_id=did, version_id=vid,
                ).set(queue="intelligence"),
            ).apply_async()
```

- [ ] **Step 4: Run tests**

Run: `docker cp services/intelligence/app sedoc-intelligence:/app/ && docker exec sedoc-intelligence sh -c 'cd /app && python -m pytest tests/test_clause_match.py -q'`
Expected: `5 passed`

- [ ] **Step 5: Commit**

```bash
git add services/intelligence/app/nats_consumer.py services/intelligence/tests/test_clause_match.py
git commit -m "feat(intelligence): chain clause detection after embeddings"
```

---

### Task 4: Document service — matches, variations, approve endpoints (TDD, integration)

**Files:**
- Create: `services/document/internal/handler/clause_matches_handler.go`
- Create: `services/document/internal/handler/clause_matches_integration_test.go`
- Modify: `services/document/cmd/server/main.go` (mount block after the ADR 0104 clause routes, ~line 917)

**Interfaces:**
- Consumes: Task 1 `clause_matches` table; existing `clauses` table; `database.WithTenantTx`, `auth.GetTenantID`, `auth.User`, `writeJSON` (package-local helper used by clauses_handler).
- Produces REST:
  - `GET /api/v1/documents/{id}/clause-matches` → `{"matches":[{clause_id,clause_name,jurisdiction,approved,similarity,chunk_index,matched_text,detected_at}]}`
  - `GET /api/v1/clauses/{id}/variations` → `{"variations":[{normalized_hash,occurrences,sample_text,min_similarity,max_similarity,document_ids}],"total_documents":N}`
  - `POST /api/v1/clauses/{id}/approve` → 200 `{approved_by,approved_at}` (admin/owner)
  - `DELETE /api/v1/clauses/{id}/approve` → 200 `{"status":"revoked"}` (admin/owner)

- [ ] **Step 1: Write the failing integration test** `clause_matches_integration_test.go` (build tag `integration`, reuse the package's seed helpers as `external_key_upsert_test.go` does):

```go
//go:build integration
// +build integration

// ADR 0104 Phases 2/4 + approval — acceptance tests:
//  1. clause-matches endpoint returns rows joined with clause metadata.
//  2. variations groups by normalized text with counts.
//  3. approve sets approved_by/at (admin) and revoke clears them;
//     member role is 403.
package handler_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/jackc/pgx/v5"
)

// seedClauseWorld creates a clause + two documents + three matches
// (two sharing normalized text). Uses the package's testPool/seed
// helpers from document_service_test.go.
func seedClauseWorld(ctx context.Context, t *testing.T, tenant uuid.UUID) (clauseID, docA, docB uuid.UUID) {
	t.Helper()
	clauseID = uuid.New()
	docA = seedDocument(ctx, t, tenant, "Contract A")
	docB = seedDocument(ctx, t, tenant, "Contract B")
	err := database.WithTenantTx(ctx, testPool, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO clauses (tenant_id, id, name, body_text, created_by)
			VALUES ($1, $2, 'Confidentiality', 'Each party shall keep confidential…', $3)`,
			tenant, clauseID, seedUserID); err != nil {
			return err
		}
		rows := [][]any{
			{docA, "Each party shall keep  Confidential…", 0.92},
			{docB, "each party SHALL keep confidential…", 0.88},
			{docB, "wholly different wording", 0.83},
		}
		for _, r := range rows {
			if _, err := tx.Exec(ctx, `
				INSERT INTO clause_matches
					(tenant_id, document_id, version_id, clause_id, similarity, matched_text)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				tenant, r[0], uuid.New(), clauseID, r[2], r[1]); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	return
}

func TestClauseMatches_ListForDocument(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	clauseID, docA, _ := seedClauseWorld(ctx, t, tenant)

	resp := doAuthedJSON(t, "GET", "/api/v1/documents/"+docA.String()+"/clause-matches", nil, "owner")
	require.Equal(t, 200, resp.Code)
	body := decodeMap(t, resp)
	matches := body["matches"].([]any)
	require.Len(t, matches, 1)
	m := matches[0].(map[string]any)
	require.Equal(t, clauseID.String(), m["clause_id"])
	require.Equal(t, "Confidentiality", m["clause_name"])
	require.Equal(t, false, m["approved"])
}

func TestClauseVariations_GroupsNormalizedText(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	clauseID, _, _ := seedClauseWorld(ctx, t, tenant)

	resp := doAuthedJSON(t, "GET", "/api/v1/clauses/"+clauseID.String()+"/variations", nil, "owner")
	require.Equal(t, 200, resp.Code)
	body := decodeMap(t, resp)
	variations := body["variations"].([]any)
	// Two matched_texts normalize identically; the third differs → 2 groups.
	require.Len(t, variations, 2)
	top := variations[0].(map[string]any)
	require.EqualValues(t, 2, top["occurrences"])
	require.EqualValues(t, 2, body["total_documents"])
}

func TestClauseApprove_SetsAndRevokes(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	clauseID, _, _ := seedClauseWorld(ctx, t, tenant)
	_ = ctx

	resp := doAuthedJSON(t, "POST", "/api/v1/clauses/"+clauseID.String()+"/approve", nil, "owner")
	require.Equal(t, 200, resp.Code)
	body := decodeMap(t, resp)
	require.NotEmpty(t, body["approved_at"])

	resp = doAuthedJSON(t, "DELETE", "/api/v1/clauses/"+clauseID.String()+"/approve", nil, "owner")
	require.Equal(t, 200, resp.Code)

	resp = doAuthedJSON(t, "POST", "/api/v1/clauses/"+clauseID.String()+"/approve", nil, "member")
	require.Equal(t, 403, resp.Code)
}
```

NOTE for implementer: `newTestTenant`, `seedDocument`, `seedUserID`, `testPool`, `doAuthedJSON`, `decodeMap` — reuse/adapt the existing helpers in this package's integration tests (`document_service_test.go`, `external_key_upsert_test.go`). If a helper doesn't exist under that exact name, write the thin adapter in the test file rather than editing shared fixtures.

- [ ] **Step 2: Run to verify failure**

Run: `go test -tags integration -run 'TestClauseMatches|TestClauseVariations|TestClauseApprove' ./services/document/internal/handler/... 2>&1 | tail -5`
Expected: compile FAIL (handler routes don't exist yet) or 404s.

- [ ] **Step 3: Implement `clause_matches_handler.go`**

```go
// clause_matches_handler — ADR 0104 Phases 2/4 + approval.
//
//	GET    /api/v1/documents/{id}/clause-matches  — detection results for the panel
//	GET    /api/v1/clauses/{id}/variations        — usage variants (Phase 4)
//	POST   /api/v1/clauses/{id}/approve           — approve (admin/owner)
//	DELETE /api/v1/clauses/{id}/approve           — revoke (admin/owner)
//
// Detection rows are written by the intelligence detect_clauses task.
// Variations group by md5(lower + collapsed whitespace) of matched_text
// — keep in sync with _normalize_text in intelligence clause_match.py.
package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
)

type ClauseMatchesHandler struct{ pool *pgxpool.Pool }

func NewClauseMatchesHandler(pool *pgxpool.Pool) *ClauseMatchesHandler {
	return &ClauseMatchesHandler{pool: pool}
}

func (h *ClauseMatchesHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/clause-matches", h.listForDocument)
	mux.HandleFunc("GET /api/v1/clauses/{id}/variations", h.variations)
	mux.HandleFunc("POST /api/v1/clauses/{id}/approve", h.approve)
	mux.HandleFunc("DELETE /api/v1/clauses/{id}/approve", h.revoke)
}

type clauseMatchRow struct {
	ClauseID     string    `json:"clause_id"`
	ClauseName   string    `json:"clause_name"`
	Jurisdiction string    `json:"jurisdiction"`
	Approved     bool      `json:"approved"`
	Similarity   float32   `json:"similarity"`
	ChunkIndex   int       `json:"chunk_index"`
	MatchedText  string    `json:"matched_text"`
	DetectedAt   time.Time `json:"detected_at"`
}

func (h *ClauseMatchesHandler) listForDocument(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	docID, perr := uuid.Parse(r.PathValue("id"))
	if perr != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid document id"})
		return
	}
	matches := []clauseMatchRow{}
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		rows, e := tx.Query(r.Context(), `
			SELECT m.clause_id, c.name, c.jurisdiction,
			       (c.approved_at IS NOT NULL) AS approved,
			       m.similarity, m.chunk_index, m.matched_text, m.detected_at
			  FROM clause_matches m
			  JOIN clauses c ON c.tenant_id = m.tenant_id AND c.id = m.clause_id
			 WHERE m.tenant_id = $1 AND m.document_id = $2
			   AND c.deleted_at IS NULL
			 ORDER BY m.similarity DESC`,
			tid, docID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var m clauseMatchRow
			var cid uuid.UUID
			if e := rows.Scan(&cid, &m.ClauseName, &m.Jurisdiction, &m.Approved,
				&m.Similarity, &m.ChunkIndex, &m.MatchedText, &m.DetectedAt); e != nil {
				return e
			}
			m.ClauseID = cid.String()
			matches = append(matches, m)
		}
		return rows.Err()
	})
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, 200, map[string]any{"matches": matches})
}

type variationRow struct {
	NormalizedHash string   `json:"normalized_hash"`
	Occurrences    int      `json:"occurrences"`
	SampleText     string   `json:"sample_text"`
	MinSimilarity  float32  `json:"min_similarity"`
	MaxSimilarity  float32  `json:"max_similarity"`
	DocumentIDs    []string `json:"document_ids"`
}

func (h *ClauseMatchesHandler) variations(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	clauseID, perr := uuid.Parse(r.PathValue("id"))
	if perr != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid clause id"})
		return
	}
	variations := []variationRow{}
	var totalDocs int
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		// Normalization MUST mirror intelligence _normalize_text
		// byte-for-byte: lowercase → collapse whitespace runs to a
		// single space → trim. Collapse BEFORE btrim: btrim only
		// strips literal spaces, so edge tabs/newlines must first be
		// collapsed into spaces (review finding on Task 2).
		rows, e := tx.Query(r.Context(), `
			SELECT md5(btrim(regexp_replace(lower(matched_text), '\s+', ' ', 'g'))) AS h,
			       count(*)                                       AS occurrences,
			       min(matched_text)                              AS sample_text,
			       min(similarity)                                AS min_sim,
			       max(similarity)                                AS max_sim,
			       array_agg(DISTINCT document_id::text)          AS doc_ids
			  FROM clause_matches
			 WHERE tenant_id = $1 AND clause_id = $2
			 GROUP BY 1
			 ORDER BY occurrences DESC, max_sim DESC
			 LIMIT 100`,
			tid, clauseID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v variationRow
			if e := rows.Scan(&v.NormalizedHash, &v.Occurrences, &v.SampleText,
				&v.MinSimilarity, &v.MaxSimilarity, &v.DocumentIDs); e != nil {
				return e
			}
			variations = append(variations, v)
		}
		if e := rows.Err(); e != nil {
			return e
		}
		return tx.QueryRow(r.Context(), `
			SELECT count(DISTINCT document_id) FROM clause_matches
			 WHERE tenant_id = $1 AND clause_id = $2`,
			tid, clauseID).Scan(&totalDocs)
	})
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, 200, map[string]any{"variations": variations, "total_documents": totalDocs})
}

// requireAdminOwner mirrors the role gate the clause CRUD mutations use.
func requireAdminOwner(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return uuid.Nil, uuid.Nil, false
	}
	u, _ := auth.User(r.Context())
	if u.Role != "admin" && u.Role != "owner" {
		writeJSON(w, 403, map[string]string{"error": "admin or owner role required"})
		return uuid.Nil, uuid.Nil, false
	}
	return tid, u.ID, true
}

func (h *ClauseMatchesHandler) approve(w http.ResponseWriter, r *http.Request) {
	tid, uid, ok := requireAdminOwner(w, r)
	if !ok {
		return
	}
	clauseID, perr := uuid.Parse(r.PathValue("id"))
	if perr != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid clause id"})
		return
	}
	var approvedAt time.Time
	err := database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			UPDATE clauses
			   SET approved_by = $3, approved_at = now(), updated_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
			 RETURNING approved_at`,
			tid, clauseID, uid).Scan(&approvedAt)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, 404, map[string]string{"error": "clause not found"})
			return
		}
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"approved_by": uid.String(),
		"approved_at": approvedAt,
	})
}

func (h *ClauseMatchesHandler) revoke(w http.ResponseWriter, r *http.Request) {
	tid, _, ok := requireAdminOwner(w, r)
	if !ok {
		return
	}
	clauseID, perr := uuid.Parse(r.PathValue("id"))
	if perr != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid clause id"})
		return
	}
	err := database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		ct, e := tx.Exec(r.Context(), `
			UPDATE clauses
			   SET approved_by = NULL, approved_at = NULL, updated_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
			tid, clauseID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return nil
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, 404, map[string]string{"error": "clause not found"})
			return
		}
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}
```

- [ ] **Step 4: Mount in `main.go`** — immediately after the existing clause routes block (~line 917):

```go
	// ADR 0104 Phases 2/4 + approval — detection results, variations,
	// approve/revoke. Same SessionAuth chain as the CRUD routes;
	// admin/owner gate for approve lives in the handler.
	cmMux := http.NewServeMux()
	handler.NewClauseMatchesHandler(pool).Register(cmMux)
	clauseMatchAuth := middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(cmMux)
	rootMux.Handle("GET /api/v1/documents/{id}/clause-matches", middleware.CorrelationHTTP(clauseMatchAuth))
	rootMux.Handle("GET /api/v1/clauses/{id}/variations", middleware.CorrelationHTTP(clauseMatchAuth))
	rootMux.Handle("POST /api/v1/clauses/{id}/approve", middleware.CorrelationHTTP(clauseMatchAuth))
	rootMux.Handle("DELETE /api/v1/clauses/{id}/approve", middleware.CorrelationHTTP(clauseMatchAuth))
```

- [ ] **Step 5: Run the integration tests**

Run: `go test -tags integration -run 'TestClauseMatches|TestClauseVariations|TestClauseApprove' ./services/document/internal/handler/... 2>&1 | tail -5`
Expected: 3 PASS.

- [ ] **Step 6: Unit-suite regression**

Run: `cd services/document && go test ./... 2>&1 | grep -E '^(ok|FAIL)' | grep FAIL || echo CLEAN`
Expected: `CLEAN`

- [ ] **Step 7: Commit**

```bash
git add services/document/internal/handler/clause_matches_handler.go services/document/internal/handler/clause_matches_integration_test.go services/document/cmd/server/main.go
git commit -m "feat(document): clause matches, variations, approve endpoints (ADR 0104)"
```

---

### Task 5: Web — API client, MatchedClausesPanel, clauses-page actions (TDD)

**Files:**
- Modify: `web/src/api/clauses.ts` (add `approveClause`, `revokeClauseApproval`, `getClauseVariations`, `getDocumentClauseMatches` + types)
- Create: `web/src/components/documents/MatchedClausesPanel.tsx`
- Create: `web/src/components/documents/__tests__/MatchedClausesPanel.test.tsx`
- Modify: `web/src/routes/_authenticated/workspaces/$workspaceId/documents/$documentId.tsx` (render the panel in the sidebar column, after the metadata/custom-fields cards)
- Modify: `web/src/routes/_authenticated/clauses/index.tsx` (Approve/Revoke button + badge, Copy button, Usage section in the preview pane)

**Interfaces:**
- Consumes Task 4 REST shapes verbatim.
- Produces: `getDocumentClauseMatches(docId): Promise<ClauseMatch[]>`; `approveClause(id)`, `revokeClauseApproval(id)`, `getClauseVariations(id): Promise<ClauseVariations>`.

- [ ] **Step 1: Write the failing panel test** `MatchedClausesPanel.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MatchedClausesPanel } from '@/components/documents/MatchedClausesPanel'

const matches = [
  {
    clause_id: 'c-1', clause_name: 'Confidentiality — Standard',
    jurisdiction: 'UAE', approved: true, similarity: 0.92,
    chunk_index: 3, matched_text: 'Each party shall keep confidential…',
    detected_at: new Date().toISOString(),
  },
]

vi.mock('@/api/clauses', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  getDocumentClauseMatches: vi.fn(async () => matches),
}))

function wrap(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>
}

describe('<MatchedClausesPanel>', () => {
  beforeEach(() => {
    Object.assign(navigator, { clipboard: { writeText: vi.fn(async () => {}) } })
  })

  it('renders matches with similarity and approval badge', async () => {
    render(wrap(<MatchedClausesPanel documentId="d-1" />))
    expect(await screen.findByText('Confidentiality — Standard')).toBeInTheDocument()
    expect(screen.getByText('92%')).toBeInTheDocument()
    expect(screen.getByText(/approved/i)).toBeInTheDocument()
  })

  it('copies the clause body on Copy click', async () => {
    const user = userEvent.setup({ writeToClipboard: false })
    render(wrap(<MatchedClausesPanel documentId="d-1" />))
    await user.click(await screen.findByRole('button', { name: /copy/i }))
    expect(navigator.clipboard.writeText).toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && npx vitest run src/components/documents/__tests__/MatchedClausesPanel.test.tsx`
Expected: FAIL — module not found.

- [ ] **Step 3: Add API functions** to `web/src/api/clauses.ts` (append; keep existing exports untouched):

```ts
// ── ADR 0104 Phases 2/4 + approval ──────────────────────────────────

export interface ClauseMatch {
  clause_id: string
  clause_name: string
  jurisdiction: string
  approved: boolean
  similarity: number
  chunk_index: number
  matched_text: string
  detected_at: string
}

export interface ClauseVariation {
  normalized_hash: string
  occurrences: number
  sample_text: string
  min_similarity: number
  max_similarity: number
  document_ids: string[]
}

export interface ClauseVariations {
  variations: ClauseVariation[]
  total_documents: number
}

export async function getDocumentClauseMatches(documentId: string): Promise<ClauseMatch[]> {
  const { data } = await api.get<{ matches: ClauseMatch[] }>(
    `/documents/${documentId}/clause-matches`,
  )
  return data?.matches ?? []
}

export async function getClauseVariations(clauseId: string): Promise<ClauseVariations> {
  const { data } = await api.get<ClauseVariations>(`/clauses/${clauseId}/variations`)
  return data
}

export async function approveClause(clauseId: string): Promise<void> {
  await api.post(`/clauses/${clauseId}/approve`)
}

export async function revokeClauseApproval(clauseId: string): Promise<void> {
  await api.delete(`/clauses/${clauseId}/approve`)
}
```

- [ ] **Step 4: Implement `MatchedClausesPanel.tsx`**

```tsx
// MatchedClausesPanel — ADR 0104 Phase 2 surface. Shows clause-library
// matches detected in this document (written by the intelligence
// detect_clauses task). Self-hides when there are no matches, matching
// the FilingSuggestionPanel idiom. Copy puts the MATCHED text on the
// clipboard (the Phase-3 "reuse" substitute).
import { useQuery } from '@tanstack/react-query'
import { BookMarked, Copy, ShieldCheck } from 'lucide-react'
import { toast } from 'sonner'

import { getDocumentClauseMatches } from '@/api/clauses'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'

export function MatchedClausesPanel({ documentId }: { documentId: string }) {
  const { data: matches } = useQuery({
    queryKey: ['clause-matches', documentId],
    queryFn: () => getDocumentClauseMatches(documentId),
    staleTime: 60_000,
  })

  if (!matches?.length) return null

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      toast.success('Copied to clipboard')
    } catch {
      toast.error('Copy failed')
    }
  }

  return (
    <Card className="p-4" data-testid="matched-clauses-panel">
      <div className="mb-2 flex items-center gap-2 text-sm font-semibold">
        <BookMarked className="h-4 w-4" /> Matched clauses
      </div>
      <ul className="space-y-3">
        {matches.map((m) => (
          <li key={`${m.clause_id}-${m.chunk_index}`} className="rounded-lg border border-border/60 p-2.5">
            <div className="flex items-center justify-between gap-2">
              <span className="truncate text-sm font-medium">{m.clause_name}</span>
              <span className="shrink-0 text-xs text-muted-foreground">
                {Math.round(m.similarity * 100)}%
              </span>
            </div>
            <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
              {m.approved && (
                <span className="inline-flex items-center gap-1 rounded-full bg-emerald-100 px-1.5 py-0.5 text-emerald-700">
                  <ShieldCheck className="h-3 w-3" /> Approved
                </span>
              )}
              {m.jurisdiction && (
                <span className="text-muted-foreground">{m.jurisdiction}</span>
              )}
            </div>
            {m.matched_text && (
              <p className="mt-1.5 line-clamp-3 text-xs text-muted-foreground">{m.matched_text}</p>
            )}
            <Button
              size="sm"
              variant="ghost"
              className="mt-1.5 h-7 gap-1.5 px-2 text-xs"
              onClick={() => copy(m.matched_text)}
            >
              <Copy className="h-3 w-3" /> Copy
            </Button>
          </li>
        ))}
      </ul>
    </Card>
  )
}
```

- [ ] **Step 5: Run panel tests → PASS**

Run: `cd web && npx vitest run src/components/documents/__tests__/MatchedClausesPanel.test.tsx`
Expected: `2 passed`

- [ ] **Step 6: Wire the panel into doc detail** — in `documents/$documentId.tsx`, import `{ MatchedClausesPanel } from '@/components/documents/MatchedClausesPanel'` and render `<MatchedClausesPanel documentId={documentId} />` in the right-hand sidebar column directly after the metadata/custom-fields card. Follow whatever wrapper the neighbors use in that column.

- [ ] **Step 7: Clauses page actions** — in `clauses/index.tsx`:
  - Import `approveClause, revokeClauseApproval, getClauseVariations` + `useAppMutation` + `useQuery`.
  - In the preview pane header row add:
    - Copy button: `onClick={() => navigator.clipboard.writeText(selected.body_text).then(() => toast.success('Clause copied'))}` with `aria-label="Copy clause"`.
    - Approve/Revoke button (render only when `user.role` is admin/owner, matching how the page already gates its Create button): calls `approveClause(selected.id)` / `revokeClauseApproval(selected.id)` via `useAppMutation`, invalidates the clauses list query on success.
    - Badge next to the name when `approved_at` is set: `✓ Approved`.
  - Below the body preview add a **Usage** block: `useQuery({ queryKey: ['clause-variations', selected?.id], queryFn: () => getClauseVariations(selected!.id), enabled: !!selected })`; render `total_documents` ("Used in N documents") and the variations list — each row: `occurrences×`, `line-clamp-2` sample_text, similarity range `min–max%`. Empty state: "No detected uses yet."

- [ ] **Step 8: Full web regression + typecheck**

Run: `cd web && npx tsc --noEmit && npx vitest run 2>&1 | grep -E 'Tests |FAIL'`
Expected: typecheck clean; all tests pass (284+ incl. the 2 new).

- [ ] **Step 9: Commit**

```bash
git add web/src/api/clauses.ts web/src/components/documents/MatchedClausesPanel.tsx web/src/components/documents/__tests__/MatchedClausesPanel.test.tsx "web/src/routes/_authenticated/workspaces/\$workspaceId/documents/\$documentId.tsx" web/src/routes/_authenticated/clauses/index.tsx
git commit -m "feat(web): matched-clauses panel, clause approve/copy/usage (ADR 0104)"
```

---

### Task 6: Rebuild, live e2e, docs

**Files:**
- Modify: `docs/adr/0104-clause-library.md` (status update: Phases 2/4 + approval shipped, Phase 3 substitute; keep honest about the OnlyOffice plugin remaining deferred)

- [ ] **Step 1: Rebuild + restart services**

```bash
docker compose build document intelligence intelligence-worker intelligence-worker-misc
docker compose up -d document intelligence intelligence-worker intelligence-worker-misc
```

- [ ] **Step 2: Live e2e — detection round trip**

1. Ensure a clause exists (seeded "Confidentiality — Standard" qualifies).
2. Upload a small text/PDF document whose body CONTAINS that clause verbatim plus filler, into any workspace (use the UI or the storage upload API with the session cookie).
3. Wait for OCR → embed → detect (poll):
```bash
docker exec sedoc-postgres sh -c 'psql -U $POSTGRES_USER -d $POSTGRES_DB -tAc "SELECT clause_id, similarity FROM clause_matches ORDER BY detected_at DESC LIMIT 3;"'
```
Expected: ≥1 row with similarity ≥ 0.80.
4. `GET /api/v1/documents/{docId}/clause-matches` via the vite proxy with a session → 200 with the match.
5. `GET /api/v1/clauses/{clauseId}/variations` → 200, `total_documents ≥ 1`.
6. `POST /api/v1/clauses/{clauseId}/approve` as owner → 200; re-fetch matches → `approved: true`.

- [ ] **Step 3: Update ADR 0104** — change the status line to `Status: Accepted (Phases 1, 2, 4 + approval shipped; Phase 3 shipped as copy-based picker — in-editor OnlyOffice plugin still deferred)` and add a short "Update (2026-07-14)" section listing: detection task + chain, clause_matches/clause_embeddings tables, endpoints, panel, approve workflow, variations, threshold env var.

- [ ] **Step 4: Commit**

```bash
git add docs/adr/0104-clause-library.md docs/superpowers/
git commit -m "docs: ADR 0104 update — clause phases 2/4 + approval shipped"
```
