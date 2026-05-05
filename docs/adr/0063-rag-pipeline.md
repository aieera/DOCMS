# ADR 0063 — Workspace-Scoped RAG Pipeline

Date: 2026-05-05
Status: Accepted

## Context

Two RAG-shaped surfaces have been adding up in the codebase:

1. **ADR 0055 Doc Q&A** — per-document chat panel. The user opens a
   single doc, asks a question, the LLM answers grounded in that
   doc's chunks. Lives at the `/qa` Celery task + the
   `DocQAChat` component.
2. **Blueprint §6.8 RAG** — workspace-scoped Q&A. The user lands on
   a top-level `/ask` page, asks a question, the LLM answers
   grounded in *every* document the user can read across the chosen
   workspace (or tenant-wide when no workspace specified).

The two share the same retrieval primitives (Qdrant hybrid search +
RRF fusion + cross-encoder rerank + LLM via litellm), but they
diverge on:

- Scope: one document vs many
- Audit ledger: `qa_messages` per-conversation vs append-only
  `rag_query_log` per query
- Rate limit: §6.8 mandates `rag_queries_per_day` per user;
  Doc Q&A had no quota
- Permission filter: per-doc Q&A trusted that the caller already
  passed the doc-level `view` check; workspace RAG must
  pre-filter the candidate doc set against every doc the caller
  can read

## Decision

Ship a **separate** `POST /api/v1/rag/query` endpoint and a
**separate** `app.tasks.rag.workspace_query` Celery task, leaving
the existing `/qa` and `stream_ask` paths intact.

### Why not consolidate

Tempting in the abstract, but:

- The two paths' permission semantics genuinely differ. Per-doc Q&A
  has already passed the `view` gate by the time it runs; merging
  the paths would make every per-doc query do the workspace
  permission scan unnecessarily.
- Per-message conversational history makes sense for a single doc
  ("what does this say about X?" → "and what about Y?"). Workspace
  RAG is intentionally stateless per query — cross-doc context
  shouldn't leak between unrelated questions.
- The Doc Q&A surface ships in production already; consolidation
  would force a migration of `qa_conversations`/`qa_messages` rows
  into `rag_query_log` shape. The cost outweighs the benefit.

The two paths share the same retrieval primitives (vector search,
RRF, reranker) — those live in `services/intelligence/app/models/`
and stay common.

### Schema additions

`document_chunks` gains `section_path`, `char_offset_start`,
`char_offset_end`, `page_number`. Backfill: existing chunks have
NULL for all four (no migration of historical data — the new
columns are nullable). Rechunking is opt-in per workspace via
`workspace_ai_settings.embedding_model` change.

Two new tables:

- `rag_query_log` — append-only audit trail. Captures the question,
  the answer, the citations (snapshotted), the model used, and the
  cost. Doubles as the rate-limit window source: count rows per
  (tenant, user, last 24 h) → compare to quota.
- `workspace_ai_settings` — per-workspace toggle + embedding/answer
  model + rate limit. An admin can disable RAG entirely on a
  sensitive workspace, switch models per workspace, or raise/lower
  the per-day quota.

### Switching embedding models

If a workspace switches `embedding_model` (e.g.
`bge-large-en-v1.5` → `text-embedding-3-large`), existing chunks
keep the original embedding. New chunks use the new model. RAG
query MUST embed the question with the model that matches each
chunk's `embedding_model` — or the workspace must run a re-embed
pass before retrieval will hit. The §6.8 implementation goes with
the second strategy (re-embed pass): simpler, more correct, the
workspace is already paying the cost of the model swap.

### Permission-filtered retrieval

Two-step:

1. **Document-level pre-filter**: before hitting Qdrant, the
   service calls `policy.BatchCheckPermission` for every doc in
   the workspace (or tenant-wide) the caller might be allowed to
   see, and assembles the allowed `doc_ids` set. Hot path; expect
   thousands of doc IDs at this stage.
2. **Vector filter**: the Qdrant query passes the allowed `doc_ids`
   set as a `MatchAny` payload condition. Even if a chunk slipped
   into the index without the right `readable_by` (which is the
   ADR 0055 fallback), it can't surface in retrieval.

Both filters are required: the pre-filter handles the per-document
ACL grants that don't fit cleanly in `readable_by` group payloads
(e.g. specific user grants); the payload filter handles the
defense-in-depth case where a doc moved between workspaces between
embed and query.

### Rate limiting

Stored in `rag_query_log`. The handler counts rows for
`(tenant, user, > now() - 24h)` and compares to
`workspace_ai_settings.rag_queries_per_day`. When over, return
HTTP 429 with the standard `Retry-After` header. Quota is
per-user, per-tenant; per-workspace if a workspace_id is provided.

## Consequences

**Pro**

- One shared retrieval layer, two distinct surfaces. Each surface
  optimizes for its own use case without forcing the other to
  conform.
- Auditable trail: every workspace RAG query lands in
  `rag_query_log` with snapshotted citations. Works for compliance
  reviews independent of the LLM provider.
- Per-workspace settings let an operator disable RAG on a single
  sensitive workspace without touching other tenants.

**Con**

- Schema slightly bigger; two tables + 4 column additions.
- Two RAG endpoints means two surfaces to keep in sync as the
  retrieval primitives evolve (cross-encoder upgrade, re-rank
  algorithm change, etc.). The shared `models/` package is the
  natural place to keep this consistent.

## Out of scope (follow-ups)

- Multi-turn workspace RAG (treating /ask as a conversational
  chat). The current decision is intentionally stateless per
  query; multi-turn is a follow-up that would lift the
  `qa_conversations` model into a workspace-scoped table.
- Streaming responses for the `/rag/query` endpoint. Doc Q&A
  streams via SSE; RAG returns the full answer in one shot for
  simpler clients (curl, dashboards). A streaming variant lives at
  `/rag/query/stream` if the spec asks for it later.
- Per-tenant model fine-tuning of the cross-encoder reranker.
  Feasible but the cross-encoder we ship is already
  domain-flexible; a tenant-specific reranker is an ADR of its own.
