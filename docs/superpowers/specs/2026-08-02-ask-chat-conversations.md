# Ask → cross-document chat with server-side memory + history

**Status:** design for review (scope approved: full backend memory), pre-implementation
**Date:** 2026-08-02
**Surfaces:** Python `services/intelligence`, `services/document` migrations, `web/` Ask page + `api/doc-qa.ts`

## Goal

Turn the one-shot Ask page (`web/src/routes/_authenticated/ask.tsx`, `queryRAG`) into a
Claude/ChatGPT-style chat: a message thread with streamed answers + citations, a left
**history sidebar** (new / past / rename / delete), and **true multi-turn memory** across
documents — persisted server-side.

## Key insight (why this is tractable)

The per-document QA chat (`DocQAChat` → `doc-qa.ts` → Python `/intelligence/qa*`) already
has everything except cross-document scope:
- **Conversation storage** — `qa_conversations` + `qa_messages` (document svc migration
  `000015_qa_history`). `qa_messages` is keyed only by `conversation_id` — **no change**.
- **Multi-turn assembly** — `tasks/rag.py:_build_messages` prepends the last 6 turns.
- **SSE streaming** — `routes.py:qa_stream_endpoint` (+ `_sse`); event shapes already match
  the FE parser in `doc-qa.ts`.
- **Scoped, permission-filtered retrieval** — `_vector_search`/`_retrieve` already support
  `scope ∈ document|workspace|tenant` **and** an `allowed_doc_ids` filter; `rag_persist.
  list_allowed_doc_ids` computes the user's readable docs (workspace or tenant-wide).

**The only blocker:** `qa_conversations.document_id UUID NOT NULL` + composite FK, and the
handlers/`stream_ask` hard-pinning `scope="document"`.

## Data model change (one new migration, document service)

`services/document/migrations/0001NN_qa_conversation_scope.{up,down}.sql`:
- `ALTER TABLE qa_conversations ALTER COLUMN document_id DROP NOT NULL;`
- Add `scope TEXT NOT NULL DEFAULT 'document' CHECK (scope IN ('document','workspace','global'))`.
- Add `workspace_id UUID` (nullable) + composite FK `(tenant_id, workspace_id) → workspaces`
  ON DELETE CASCADE.
- CHECK: `scope='document' ⇒ document_id NOT NULL`; `scope='workspace' ⇒ workspace_id NOT NULL`;
  `scope='global' ⇒ both NULL`.
- Index `(tenant_id, user_id, scope, workspace_id, updated_at DESC)` for the history list.
- Keep the existing doc composite FK (a NULL `document_id` is simply not checked; CASCADE on
  doc delete still correctly purges doc-scoped chats). `qa_messages` unchanged.
- `.down.sql` reverses (drop col/checks/index; note: rows with NULL document_id must be
  deleted or the NOT NULL restore fails — down handles that).

## Backend (Python `services/intelligence`)

- `qa_persist.py` — `ensure_conversation`: make `document_id` optional, add `scope` +
  `workspace_id` to lookup WHERE + INSERT. `list_conversations`: parameterize by scope
  (+ workspace_id) instead of `document_id`.
- `tasks/rag.py` — generalize `stream_ask(scope, scope_id/workspace_id, allowed_doc_ids)`
  instead of hard-pinning document scope; for `workspace`/`global` pass `allowed_doc_ids`
  (from `list_allowed_doc_ids`) into retrieval. Multi-turn already handled via
  `conversation_history`. Route new chat through `stream_ask` (stateful), **not**
  `workspace_query` (stateless).
- `routes.py` — `QARequest`: optional `document_id`, add `scope` + `workspace_id`; drop the
  `if not document_id` 400s in `/qa` + `/qa/sync`; for workspace/global resolve
  `allowed_doc_ids` (+ reuse the `workspace_ai_settings` enable/quota block from `/rag/query`
  for `scope='workspace'`; `global` uses a tenant-level daily cap). Add
  `GET /qa/history` (query: `scope`, optional `workspace_id`) alongside the existing
  path-keyed `/qa/history/{document_id}`.

## Frontend

- `web/src/api/doc-qa.ts` — add `scope`/`workspaceId` to `AskParams` + request bodies; add a
  scope-aware `listConversations`/`getQAHistory` (query-param variant). SSE parsing unchanged.
- `web/src/routes/_authenticated/ask.tsx` — rebuild as two-pane chat:
  - **History sidebar**: New chat, list of the user's workspace+global conversations
    (title = first question, relative time), select, rename, delete. `lg` vertical; collapses
    on mobile.
  - **Thread**: user + assistant messages; assistant answers **stream** (reuse the SSE flow
    from `DocQAChat`), render markdown via `AnswerMarkdown`, show citations + up/down/flag
    feedback (`sendRAGFeedback` stays for one-off, or per-message feedback via qa path).
  - **Composer** (bottom): textarea, workspace-scope selector (All workspaces = global, or a
    workspace = workspace scope — fixes the scope for a new conversation), 4000-char budget,
    ⌘/Ctrl+Enter submit.
  - **Empty state**: keep the suggested-question chips; clicking one starts a new chat.

## Scope semantics

`All workspaces` → `scope='global'`. A specific workspace → `scope='workspace'` +
`workspace_id`. Document chat (DocQAChat) is untouched (`scope='document'`). A conversation's
scope is fixed at creation; the sidebar shows global + workspace chats for the current user.

## Guardrails / non-goals

- Reuse existing retrieval, rerank, SSE, multi-turn, RLS, and `list_allowed_doc_ids`
  permission filtering — **no new retrieval code**. `allowed_doc_ids` is mandatory for
  workspace/global (defense-in-depth beyond the `readable_by` group filter).
- No gRPC/proto changes (these are REST + Kong; `make proto-gen` N/A).
- Keep `queryRAG` one-shot endpoint working (other callers may use it).
- No streaming-protocol change (event shapes already shared).

## Phased delivery (each verified before the next)

- **A. Migration** — up/down; `make migrate-up SERVICE=document` locally; verify schema.
- **B. Python backend** — persist + rag.py + routes.py; unit/integration where present;
  manual curl of `/qa` (global + workspace) with identity headers.
- **C. FE api** — `doc-qa.ts` scope params + history variant; tsc/lint.
- **D. FE chat UI** — Ask page two-pane chat + history; tsc/lint/vitest.

Backend (A+B) lands first and is independently testable; the FE (C+D) builds on it.
