# 0055 — Document Q&A chat panel + SSE streaming

- **Status:** Accepted
- **Date:** 2026-05-03
- **Supersedes:** —
- **Deciders:** core eng + intelligence + frontend

## Context

The RAG pipeline (`app.tasks.rag.ask`) already exists and exposes a
synchronous `POST /api/v1/intelligence/ask`. It does hybrid retrieval
(vector + RRF fusion), reranking via cross-encoder, and an LLM call
through `llm_gateway.completion`. What it doesn't do:

  * Surface results in a conversational UI on the document detail page
  * Stream the LLM response (typing-indicator UX)
  * Persist conversations so a user can return later
  * Return citations rich enough for the PDF viewer to highlight a
    specific cited passage

This ADR records the additions, not a rewrite.

## Decision

Two new HTTP endpoints on the intelligence service plus a Q&A chat
panel on the document detail page. Conversation history lives in two
new tables on the document service's Postgres (same schema, same RLS
pattern).

### Endpoints

  * `POST /api/v1/intelligence/qa` — **streaming** SSE.
    Each event is a JSON-encoded `data:` line. Three event types:
    `chunk` (text token), `citations` (single emit before chunks
    stream — gives the UI the source list to render under the answer),
    `done` (completion, with totals). Errors arrive as `error` events.
  * `POST /api/v1/intelligence/qa/sync` — non-streaming variant for
    clients that can't consume SSE. Same request body, returns the
    full response object.
  * `GET /api/v1/intelligence/qa/history/{document_id}` — list the
    user's conversations for this document (most recent first), each
    with their messages.

Both POST endpoints reuse `app.tasks.rag.ask`/the new `stream_ask`
helper — no parallel pipeline.

### Streaming model

`llm_gateway` gains `stream_completion(...)` that sets
`litellm.completion(stream=True)` and yields token chunks. The /qa
endpoint:

  1. Performs retrieval + reranking synchronously (fast; ~50–300ms).
  2. Emits a `citations` event with the top-k chunk metadata so the
     UI can render the source list immediately.
  3. Streams LLM tokens via `stream_completion`, emitting one `chunk`
     event per token batch.
  4. On final yield, persists the full assistant message + the user
     message in one tx, emits `done` with token totals + cost.

If the LLM call fails partway, the partial answer is still persisted
(role='assistant', model_used carries the model, tokens_used is what
we got) so the conversation thread doesn't lose context. An `error`
event is emitted before connection close.

### Citations

`stream_ask` returns each top chunk as a structured citation:

```
{
  "chunk_index": 7,
  "text": "...the cited passage...",
  "page": 3,
  "start_char": 120,
  "end_char": 250,
  "similarity_score": 0.87,
  "document_id": "...",
  "version_id": "..."
}
```

The fields come from the existing Qdrant payload (`text_snippet`,
`page_number`, `start_char`, `end_char`) — no schema change in
Qdrant. The frontend uses `start_char` + `end_char` to scroll the
PDF viewer to the cited range and highlight it.

### Persistence

  * `qa_conversations` — one row per (document, user) thread. Title
    is auto-generated from the first user question (first 60 chars).
  * `qa_messages` — append-only (role, content, citations JSONB,
    model_used, tokens_used). `ON DELETE CASCADE` from the
    conversation.

Both tables are RLS-enabled with FORCE; intelligence writes them via
`set_config('app.current_tenant', $1)` in the same connection.

### Tenant isolation

  * Qdrant filter (existing in `_vector_search`) enforces both
    `tenant_id` and `readable_by` (group ACL). `scope='document'`
    adds a `document_id` payload filter so cross-document leakage
    is impossible even within a tenant.
  * The /qa endpoints require both `X-Tenant-ID` and `X-User-ID`.
    User identity drives both the ACL filter and the
    `qa_conversations.user_id` write.
  * RLS on the new tables blocks any DB-side leak.

### Multi-turn context

The frontend sends `conversation_id` (or `null` for a new thread).
On the server, `_load_conversation_history(conversation_id)` fetches
the last 6 messages and prepends them to the LLM messages array
before the `Documents:` block — same shape the existing `ask()`
function already accepts. The 6-message cap keeps prompt size bounded.

## Consequences

  * Only LLM providers that support streaming work end-to-end. Local
    Ollama and OpenAI both do; Anthropic via `litellm` does. The
    sync endpoint is the universal fallback.
  * Conversation history is per-user, not shared. A document with
    50 users could accumulate 50 parallel threads; UI exposes only
    "your conversations" by default. Tenant admins see all via a
    follow-up admin endpoint (not in this ADR).
  * Token + cost metering still flows through `llm_gateway._meter_usage`
    — the streaming variant accumulates token counts per chunk and
    fires the meter once at stream end.
  * No retry on stream failure. If the LLM connection drops mid-stream
    we close the SSE with an `error` event; the user re-asks. Adding
    server-side retry would mean replaying tokens already sent to the
    client, which is not idempotent.

## Alternatives considered

  * **WebSocket instead of SSE** — rejected. SSE is one-direction
    (server → client), which exactly matches the streaming response
    shape. WebSocket adds reconnect complexity for no win.
  * **Long-polling** — rejected. Same UX problem the streaming
    answer solves, just with worse latency.
  * **Per-tenant prompt customization** — deferred. Today the system
    prompt is hard-coded in `rag.py` (`SYSTEM_PROMPT` constant).
    Tenants asking for branded persona answers will get a config
    column on `compliance_config`-style row in a follow-up.
  * **Cross-document Q&A from the chat panel** — rejected for v1.
    The panel lives on the document detail page and is scoped to
    that document. Tenant-wide Q&A already exists via the existing
    `/ask` endpoint with `scope='tenant'`.
