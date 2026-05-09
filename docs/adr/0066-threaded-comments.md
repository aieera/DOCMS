# ADR 0066 — Threaded Comments + @Mentions + Reactions

Date: 2026-05-08
Status: Accepted

## Context

The `comments` table has shipped since the initial schema
([services/document/migrations/000001_initial_schema.up.sql:428](../../services/document/migrations/000001_initial_schema.up.sql#L428))
but no service/handler/repo bindings exist — comments are pure
schema today. The blueprint §10.4 calls for a complete commenting
surface:

- Threaded comments (parent_comment_id) with reply chains
- Resolve / unresolve with audit (resolved_by, resolved_at)
- @mention parsing → `dms.notify.comment.mention.v1` event the
  notification service consumes
- Reactions: 1 row per (comment, user, emoji) so a user can leave
  one reaction per emoji per comment, but multiple emoji per comment
- Real-time fan-out via the existing collaboration WebSocket
  service. We emit `dms.comment.{created,updated,resolved,deleted}.v1`
  + `dms.comment.reaction.v1` to NATS; collaboration service
  forwards to subscribed clients.

Annotations ([services/document/internal/handler/annotation_handler.go](../../services/document/internal/handler/annotation_handler.go))
are the closest existing surface and the template we mirror.

## Decision

### Schema deltas

```sql
CREATE TABLE comment_reactions (
  tenant_id    UUID NOT NULL,
  comment_id   UUID NOT NULL,
  user_id      UUID NOT NULL,
  emoji        TEXT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, comment_id, user_id, emoji),
  FOREIGN KEY (tenant_id, comment_id) REFERENCES comments(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, user_id)    REFERENCES users(tenant_id, id)    ON DELETE CASCADE
);
```

The composite PK `(tenant, comment, user, emoji)` enforces "one
user-emoji per comment" — toggling a reaction is `INSERT ... ON
CONFLICT DO NOTHING` to add, `DELETE` matching the four columns to
remove.

The existing `comments` table is unchanged. The `body` column is
plain text; @mention tokens stay as text in the body and the
service layer extracts them for notification purposes.

### Routes

```
POST   /api/v1/documents/{doc_id}/comments               — create top-level
POST   /api/v1/comments/{id}/replies                     — create reply
GET    /api/v1/documents/{doc_id}/comments               — list (threaded; resolved=false default)
PATCH  /api/v1/comments/{id}                             — edit body (author-only)
DELETE /api/v1/comments/{id}                             — soft-delete (author or admin)
POST   /api/v1/comments/{id}/resolve                     — mark resolved
POST   /api/v1/comments/{id}/unresolve

POST   /api/v1/comments/{id}/reactions  {emoji}           — toggle (idempotent add)
DELETE /api/v1/comments/{id}/reactions  {emoji}           — toggle off
GET    /api/v1/comments/{id}/reactions                   — list (rolled up)
```

### @mention parsing

Body format: `@[Display Name](user-uuid)`. The frontend's mention
autocomplete writes that exact shape; the backend parses on every
create + update with regex `\@\[([^\]]+)\]\(([0-9a-f-]{36})\)` and
emits `dms.notify.comment.mention.v1` with `{tenant_id, comment_id,
mentioned_user_id, by_user_id, document_id, body_excerpt}`.

Edge cases:
- Same user mentioned twice → one event per unique mentioned user
  (deduped at parse time).
- Self-mention → no event (user mentioning themselves doesn't need
  to be told).
- Edit that adds new mentions → event fires for new mentions only;
  the diff against the prior body's mentioned set is computed in
  the service layer.

### Events

| Subject | Producer | Consumer |
|---|---|---|
| `dms.comment.created.v1`         | doc service via outbox | collab WS, audit |
| `dms.comment.updated.v1`         | "                       | "  |
| `dms.comment.resolved.v1`        | "                       | "  |
| `dms.comment.deleted.v1`         | "                       | "  |
| `dms.comment.reaction.v1`        | "                       | "  |
| `dms.notify.comment.mention.v1`  | "                       | notification service |

Every event carries `{tenant_id, document_id, comment_id, actor_id,
at, ...event-specific}`. The collaboration service already speaks
the `dms.*.v1` subject taxonomy and adds a fanout-by-document
filter.

### Resolve semantics

- A top-level comment can be resolved/unresolved any number of
  times; the last (`resolved_by`, `resolved_at`) pair wins.
- Replies cannot be resolved independently — the resolve flag lives
  on the thread root. Frontend disables the resolve button on
  replies.
- Resolving fans out a `dms.comment.resolved.v1` event so other
  open browsers move the thread to the "resolved" filter.

### Permissions

Comment routes inherit the document's read/write permission as
checked by the existing policy gRPC pipeline:

- **Read** on the document → may list comments and read individual
  comments.
- **Comment** on the document (a new capability — for now equals
  Read) → may create, edit own, react.
- **Resolve** is delegated to the comment author OR users with
  Edit on the document. Anyone with Edit can resolve someone
  else's question. Reasonable default; surfaced in the admin
  permissions matrix.

### Real-time

The collaboration service (`services/collaboration/`, Node)
already exposes a WebSocket endpoint per ADR 0024 with
per-document rooms. The frontend opens a WS for the open document
and listens on the same channel for comment events. No new
WebSocket plumbing in the document service.

## Consequences

- One new table (`comment_reactions`) + one new event subject
  family. The existing `comments` schema needs no DDL.
- @mentions parsed server-side ensures clients can't fake a
  mention notification (writing `@[Bob](other-uuid)` directly into
  the body is harmless because the user_id is what we look up; if
  Bob isn't in the tenant's user table the parse step skips it).
- Reaction toggles are O(1) Postgres + one outbox row. At very high
  concurrency a single comment with hundreds of reactors might
  thrash the outbox — fine for v1, future optimization is a
  per-emoji counter row.
- Resolve as a fanout event means a stale "open comments" count in
  another tab updates within the WebSocket round-trip latency.

## Out of scope

- Inline anchored comments (selection-bound). Annotations
  (ADR 0024) cover that surface.
- Comment moderation / hide-by-admin. Comments belong to whoever
  authored them; admin override = soft-delete with an audit row.
- Comment search. Out-of-band; the search service indexes documents
  not comments today.
