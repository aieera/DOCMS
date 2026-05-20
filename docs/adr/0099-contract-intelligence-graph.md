# ADR 0099 — Contract intelligence graph

Status: Accepted (design + Phase 1 backend + tab UI shipped; reference
extractor deferred)
Date: 2026-05-19
Related: §18 Feature 4 of the blueprint; ADR 0052 (auto-tag),
ADR 0054 (compliance findings), ADR 0067 (annotations),
ADR 0078 (LLM NER tier toggle).

## Context

Contracts in a real DMS rarely live alone: amendments, addenda,
side-letters, master agreements, vendor changes — every contract sits
in a web of references. §18 Feature 4 of the blueprint calls for a
graph view: pick a contract, see what amends it, what supersedes it,
what it references and what references it.

This ADR is the design + Phase 1 implementation. **No Neo4j.** Adding
a graph database is an infrastructure dependency we don't want to take
on for a feature that Postgres handles cleanly with one edge table +
a recursive CTE. The blueprint hint at Neo4j is a perfectly reasonable
suggestion for a shop that already has it; we don't, and forcing it
would mean a new container + new ops surface + new failure mode for
benefit that doesn't materialize until the graph is huge.

## Postgres vs Neo4j — when to revisit

Postgres with one edge table + recursive CTE handles graphs in the
millions-of-edges range with predictable latency. The cutover to a
dedicated graph DB makes sense when:

1. Traversal queries dominate write load AND you need ≥3-hop traversal
   on >10M edges per query.
2. You have property-graph queries where the WHERE clause is a path
   pattern (rare in this domain).
3. Operations team is already running Neo4j for other reasons.

Until any of those, **Postgres**. If/when we cross the line, the edge
table maps trivially to Neo4j; the migration is a script, not a
rewrite. Documented here so the question doesn't get re-opened
every quarter.

## What is shipped now (Phase 1)

- This ADR.
- Migration `000048_contract_graph_edges.up.sql` — single edge table,
  tenant-scoped via RLS, with the four edge types from the blueprint:
  `amends`, `supersedes`, `references`, `references-by`.
- `services/document/internal/handler/contract_graph_handler.go`:
  - `GET  /api/v1/contracts/{document_id}/graph?depth=N` — returns
    the (nodes, edges) for the N-hop neighborhood. Default depth 1,
    cap 3 to avoid runaway traversals.
  - `POST /api/v1/contracts/{document_id}/edges` — admin-only, manual
    edge creation (sales/legal can hand-link contracts that the
    extractor missed).
  - `DELETE /api/v1/contracts/{document_id}/edges/{edge_id}` —
    admin-only, edge removal.
- Frontend Relationships tab on the doc detail page using
  cytoscape.js. Click a node → navigate to that contract; click an
  edge → see relationship type + extractor confidence.
- `web/src/api/contractGraph.ts` typed client.

## What is **not** shipped now

- **Automated reference extraction.** Today an edge is created either
  manually (via the POST endpoint) or by a future NLP task we haven't
  written yet. ADR Phase 2 ships a `tasks/contract_refs.py` that:
  1. Reads ocr_results for the version.
  2. Runs a regex pass for explicit references ("Amendment to MSA
     dated YYYY-MM-DD", "supersedes Agreement #X", etc.).
  3. Feeds candidates to the NER LLM tier (ADR 0078) for
     disambiguation.
  4. Writes confidence-scored edges to `contract_graph_edges`.
  Phase 1 ships the table + API + viz so the engineering team can
  hand-load test data and the UI can be reviewed.
- **Bidirectional edges.** `amends` and `supersedes` are directional
  (newer→older). `references` is also directional. `references-by`
  isn't a separate edge type — it's the same edge viewed from the
  other side. The API does the inverse-lookup transparently at query
  time so callers don't need to think about edge direction.
- **Edge approval workflow.** When the future extractor lands, it
  writes edges with a `confidence < 0.8` flag that a human reviews
  before they go live. Phase 1 has no extractor so the flag exists
  but isn't exercised.

## Edge schema

```sql
CREATE TABLE contract_graph_edges (
    tenant_id     uuid        NOT NULL REFERENCES organizations(id),
    id            uuid        NOT NULL DEFAULT gen_random_uuid(),
    src_document  uuid        NOT NULL,      -- "from" side of the edge
    dst_document  uuid        NOT NULL,      -- "to" side
    edge_type     text        NOT NULL,      -- amends | supersedes | references
    confidence    real        NOT NULL DEFAULT 1.0,  -- 1.0 = manual; <1.0 = extractor-suggested
    metadata      jsonb       NOT NULL DEFAULT '{}', -- e.g. {"effective_date":"2024-..."}
    created_by    uuid,                              -- nullable: extractor-created
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    CHECK (src_document <> dst_document),
    CHECK (edge_type IN ('amends', 'supersedes', 'references'))
);
```

Two indexes for the two traversal directions:

```sql
CREATE INDEX ON contract_graph_edges (tenant_id, src_document);
CREATE INDEX ON contract_graph_edges (tenant_id, dst_document);
```

RLS on `app.current_tenant` — same pattern as every other tenant
table.

## API

```http
GET /api/v1/contracts/{document_id}/graph?depth=2

200 OK
{
  "root_id": "uuid",
  "nodes": [
    { "id": "uuid", "title": "MSA — Acme Corp", "mime_type": "application/pdf",
      "created_at": "..." },
    …
  ],
  "edges": [
    { "id": "uuid", "src": "uuid", "dst": "uuid", "type": "amends",
      "confidence": 1.0, "metadata": {} },
    …
  ]
}
```

The handler does a single recursive CTE for both outgoing and incoming
edges within `depth` hops. Nodes are de-duped by id. Subgraph capped
at 500 nodes to avoid runaway responses; if the cap trips, the
response includes `"truncated": true` so the frontend can offer
"expand more."

```sql
WITH RECURSIVE walk(doc_id, depth, root) AS (
  SELECT $1::uuid, 0, $1::uuid
  UNION ALL
  SELECT e.dst_document, w.depth + 1, w.root
    FROM contract_graph_edges e
    JOIN walk w ON e.src_document = w.doc_id
   WHERE w.depth < $2
     AND e.tenant_id::text = current_setting('app.current_tenant', true)
  UNION ALL
  SELECT e.src_document, w.depth + 1, w.root
    FROM contract_graph_edges e
    JOIN walk w ON e.dst_document = w.doc_id
   WHERE w.depth < $2
     AND e.tenant_id::text = current_setting('app.current_tenant', true)
)
SELECT DISTINCT doc_id FROM walk LIMIT 500;
```

The CTE walks edges in *both* directions (line 5 follows `src→dst`,
line 9 follows `dst→src`) so a single traversal covers `amends`,
`supersedes`, `references`, AND `referenced by` without needing a
separate edge type. Each edge_type stays single-direction in the
data; the API exposes the inverse view via the traversal, not via
duplicated rows.

## Frontend — Relationships tab

`web/src/components/documents/RelationshipsGraph.tsx`:
- Calls `GET /contracts/{id}/graph?depth=2`.
- Renders via cytoscape.js with the dagre layout (top-down, good for
  amendment chains).
- Node colors per state (active=green, superseded=grey, retained=blue).
- Edge labels show the edge type.
- Click a node → `navigate({to: '/workspaces/.../documents/{id}'})`.
- Click an edge → open a side panel with metadata (effective date,
  extractor confidence, created_by).
- Admin/owner sees an "Add edge" button → small modal with target-doc
  picker + edge_type select; POSTs to the new edges endpoint.

The tab is hidden when the document has no edges AND the user can't
create them, so non-contract documents don't get a useless empty
viz. (Plan B was: always show the tab; rejected because most
documents aren't contracts and the tab would be noise.)

## Open questions deferred to implementation

- **Workspace boundary**: an edge currently can cross workspaces
  inside the same tenant. Should it? Phase 1 says yes — contracts
  legitimately live in different workspaces (e.g. legal vs sales)
  and still amend each other. If it's a problem we add a workspace
  filter on the traversal.
- **Edge from a deleted document**: today we soft-delete documents
  but the edge stays. The graph view filters them out at query time.
  If you'd want to see "this contract amended a now-deleted contract"
  we revisit.
- **Confidence threshold for visibility**: when the extractor lands,
  do we show all edges or only `confidence >= X`? Probably an admin
  setting per tenant; not in Phase 1.
- **Versioning**: edges point at document_id, not version_id. So an
  edge "amends X" stays valid through X's version history. The
  trade-off: you can't link "amends X v3" specifically. Document
  if/when that comes up.
