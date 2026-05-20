# ADR 0096 — Yjs CRDT real-time collaboration

Status: Accepted (design + foundation; phased implementation)
Date: 2026-05-19
Related: §17.4 of the blueprint; [services/collaboration/src/yjs-server.js](../../services/collaboration/src/yjs-server.js) (skeleton);
ADR 0066 (threaded comments), ADR 0067 (image/video annotations).

## Context

The collaboration service already runs a Yjs WebSocket server (`services/
collaboration/src/yjs-server.js`, 161 lines) implementing per-(tenant, doc)
rooms with the standard `y-protocols` sync + awareness handshake. What it
**doesn't** have yet:

1. **WebSocket auth** — the URL path carries `tenantId` but nothing validates
   that the connecting client actually belongs to that tenant. Today any
   websocket client that knows the URL can join any room. Trivial to exploit
   in a multi-tenant deployment.
2. **Persistence** — the `Y.Doc` for each room lives in process memory.
   When the last client disconnects (or the worker restarts), state is GC'd.
   Fine for transient cursor presence; not fine for collaborative comments.
3. **Frontend integration** — `web/package.json` has zero Yjs deps. No
   `Y.Doc` provider, no client, no consumer wired up.

This ADR covers the design + the foundation work shipped now, and scopes the
remaining work into phases mirroring the alt-DB / license / air-gapped
pattern. The full matrix is multi-session.

## What is shipped now (Phase 1)

- This ADR.
- `yjs` + `y-websocket` added to `web/package.json`.
- `web/src/lib/useYDoc.ts` — React hook that opens a `Y.Doc` over the
  collaboration service WebSocket with the standard y-websocket transport.
  Provides `{ydoc, provider, awareness, status}` and tears down cleanly on
  unmount. Reconnection + exponential backoff are y-websocket built-ins
  (`WebsocketProvider` handles them).
- `services/collaboration/src/yjs-server.js` — WebSocket auth gate added:
  on `Upgrade`, the server reads the `dms_session` cookie, validates against
  Postgres, and rejects the connection if the session's `tenant_id` doesn't
  match the URL's `tenantId`.
- One reference consumer wired to the CRDT: a Y.Array-backed live comment
  feed for documents (`useDocumentComments`). Reads/writes through the
  shared `Y.Doc`; the existing REST `/api/v1/documents/{id}/comments`
  remains the source of truth for persistence (Phase 2 ships the snapshot
  loop that writes the CRDT state back to Postgres).

## What is **not** shipped now

- **Postgres snapshot persistence.** The server still keeps the `Y.Doc` in
  memory only. Scoped as Phase 2; pseudocode + table design below.
- **Annotation layer Yjs wire-up.** Image and video annotations still use
  the REST round-trip. Scoped as Phase 3; the hook pattern from Phase 1
  applies directly.
- **OnlyOffice bridge.** OnlyOffice runs its own real-time protocol against
  the document-server companion process; bridging that to a Yjs `Y.Doc` is
  non-trivial and explicitly out of scope for this ADR. Scoped as Phase 4
  with caveats below.
- **Playwright multi-window test.** Needs Phase 1 visible behaviour to
  assert against; ships with Phase 2.

## Connection model

```
  ┌───────────────────────────────────────────────────────────────┐
  │  Browser (web/)                                               │
  │                                                               │
  │   useYDoc({ docId })                                          │
  │      └─→ new Y.Doc()                                          │
  │      └─→ new WebsocketProvider(                               │
  │              wsUrl,        // /yjs/{tenantId}/{docId}         │
  │              docId,                                           │
  │              ydoc,                                            │
  │              { params: {...}, awareness, ... }                │
  │          )                                                    │
  │                                                               │
  │   Consumers:                                                  │
  │      const comments = ydoc.getArray('comments')               │
  │      const annotations = ydoc.getMap('annotations')           │
  └───────────────────────────────────────────────────────────────┘
                              │  WebSocket
                              │  Upgrade carries dms_session cookie
                              ▼
  ┌───────────────────────────────────────────────────────────────┐
  │  services/collaboration  (Node)                               │
  │                                                               │
  │   verifyClient(info):                                         │
  │      cookie = parse(req.headers.cookie).dms_session           │
  │      session = SELECT FROM sessions WHERE token_hash = ...    │
  │      if session.tenant_id != urlTenantId  → reject 401        │
  │                                                               │
  │   Per-room:                                                   │
  │      rooms[roomId] = { ydoc, awareness, clients: Set<WS> }    │
  │      sync + awareness message handlers (existing)             │
  │                                                               │
  │   Phase 2: every N updates →                                  │
  │      INSERT INTO yjs_snapshots (tenant_id, doc_id, state_bin) │
  │                                                               │
  └───────────────────────────────────────────────────────────────┘
```

## WebSocket auth (Phase 1, shipped)

The Yjs server accepts an upgrade only if:

1. The request has a `dms_session` cookie.
2. The cookie's SHA-256 hash matches a row in `sessions` with
   `revoked_at IS NULL` and `expires_at > now()`.
3. The session's `tenant_id` equals the URL path's `tenantId` segment.

Failure modes:
- No cookie → close with status 4401 (custom code: auth required).
- Bad cookie → 4401.
- Tenant mismatch → 4403 (tenant boundary violation).
- Postgres unreachable → 4503.

The custom close codes let the frontend `useYDoc` hook distinguish auth
failures from network failures and decide whether to retry. y-websocket
retries network errors automatically with exponential backoff; auth
failures should NOT retry (would spin forever).

## Postgres snapshot persistence (Phase 2 — future)

Schema (added by the migration in Phase 2):

```sql
CREATE TABLE yjs_snapshots (
  tenant_id   uuid    NOT NULL,
  doc_id      uuid    NOT NULL,
  state_bin   bytea   NOT NULL,     -- encoded Y.Doc state (Y.encodeStateAsUpdate)
  updated_at  timestamptz NOT NULL DEFAULT now(),
  update_seq  bigint  NOT NULL,     -- monotonically increasing per (tenant, doc)
  PRIMARY KEY (tenant_id, doc_id, update_seq)
);

-- Tenant-scoped via RLS; keep last N snapshots per doc, GC older.
ALTER TABLE yjs_snapshots ENABLE ROW LEVEL SECURITY;
```

Write loop (in yjs-server.js):

```js
const FLUSH_EVERY = 25      // updates
const ROOM_TTL_MS = 60_000  // GC rooms with no clients

room.ydoc.on('update', (update, origin) => {
  room.pending++
  if (room.pending >= FLUSH_EVERY) flush(room)
})

async function flush(room) {
  const state = Y.encodeStateAsUpdate(room.ydoc)
  await pg.query(
    `INSERT INTO yjs_snapshots(tenant_id, doc_id, state_bin, update_seq)
     VALUES ($1, $2, $3, $4)`,
    [room.tenantId, room.docId, state, ++room.seq])
  room.pending = 0
}
```

On room creation (first client connects), the server loads the latest
snapshot and applies it via `Y.applyUpdate(ydoc, latestState)`. So
reconnecting clients see the persisted state, not an empty doc.

Tradeoffs:
- `FLUSH_EVERY=25` is a balance between write amplification and data-loss
  window on worker crash. Tune per workload.
- Storing full state each flush (not deltas) is simpler but bigger. Acceptable
  while we keep last N snapshots per doc. If state grows past a few MB
  per doc, switch to delta logs + periodic compaction.

## Annotation Yjs migration (Phase 3 — future)

Image annotations today are POSTed to
`/api/v1/documents/{id}/versions/{vid}/annotations` and refetched by
`ImageAnnotationLayer`. The Yjs pattern:

1. Open a `Y.Doc` keyed `version:{versionId}`.
2. Wrap each annotation row in a `Y.Map` stored in a `Y.Array` named
   `annotations`.
3. Subscribe to the array; render each entry on change.
4. Local mutations call `array.push([yMap])` instead of POST. The server's
   snapshot loop persists; an outbox row optionally fires the
   `dms.annotation.created.v1` event for downstream consumers (search index,
   audit log).

Same pattern for video annotations.

The migration is mechanical but has a compat horizon: until every client
has the Yjs code, the REST endpoint must keep working too. Plan:
- Phase 3a: dual-write (CRDT + REST) on the client; reads from REST.
- Phase 3b: dual-write; reads from CRDT.
- Phase 3c: CRDT-only, REST endpoint deleted.

## OnlyOffice bridge (Phase 4 — future, deferred with caveat)

OnlyOffice's document server uses its own websocket protocol against the
companion docserver process. It is **not** a Yjs consumer. To bridge:

- One path: run OnlyOffice on top of a custom storage adapter that reads/
  writes via the Yjs `Y.Doc`. Requires implementing the docserver's
  callback contract in our collab service.
- Another path: keep OnlyOffice on its native protocol, and use Yjs only
  for *non-OnlyOffice* surfaces (comments, annotations, cursor presence).

The second path is what we should ship. The blueprint's listing of
"OnlyOffice integration bridge" as a Yjs deliverable conflates two things
that don't compose at the protocol layer — call it out, defer to a future
RFC if/when there's a real need.

## Reconnection + backoff (Phase 1, free)

`y-websocket`'s `WebsocketProvider` handles reconnection automatically:
- Initial retry delay: 1 s
- Max: ~30 s (configurable)
- Exponential with jitter

The `useYDoc` hook exposes the provider's `status` ("connecting" |
"connected" | "disconnected") so UI can show a banner during outages.
Auth-failure close codes (4401/4403) disable retries because spinning on
a permanent rejection is wasteful.

## Open questions deferred to implementation

- Per-feature kill switch: should the Yjs path be gated by the license
  feature-flag (ADR 0095, `feature_flags.realtime_collab`)? Probably yes
  for tier-based pricing, but the gate plumbing is a Phase 2 task.
- Whether comments need eventually-consistent merge across regions
  (multi-region collab — ADR 0050 residency interplay).
- Storage size budget for `yjs_snapshots` — when do we compact / GC?
