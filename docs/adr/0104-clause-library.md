# ADR 0104 — Clause library + reuse

Status: Accepted (Phase 1 CRUD shipped; detection, editor side-panel,
variation tracking deferred)
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
