# 0053 — Smart routing

- **Status:** Accepted
- **Date:** 2026-05-03
- **Supersedes:** —
- **Deciders:** core eng + intelligence

## Context

Folder placement is one of the higher-friction steps in a high-volume
DMS workflow. The classification pipeline already produces a
strongly-typed `category_key` per version (Invoice, Contract, Resume,
Memo, …); folders already exist; users routinely file the same
classes into the same folders. Wiring those two pieces is mechanical
and high-leverage.

Auto-tagging (ADR 0052) shipped first because tags are additive on
the documents row and reversible. Routing changes the parent folder
— a heavier action with permission, lifecycle, and notification
implications. We want the same review-then-promote pattern, this
time with a stricter auto-apply default.

## Decision

A new Celery task `app.tasks.smart_route`, chained from
`dms.classify.completed.v1`. Generates folder suggestions through
three independent strategies, persists ranked candidates, and
**never auto-moves in v1** even when confidence clears the
auto-move threshold — the threshold drives UI emphasis, not action.

### Three strategies

| Source       | Mechanism                                                                                  |
|--------------|--------------------------------------------------------------------------------------------|
| `rule`       | `routing_rules` matches `category_key`. Confidence = `classification.confidence × 0.95`.   |
| `history`    | `filing_history` GROUP BY folder for the same `category_key`. Confidence = `frequency / total_for_category × classification.confidence`. |
| `similarity` | Qdrant top-K neighbors filtered by tenant; folder counted from each neighbor's documents row. Confidence = `(neighbor_count / K) × mean_score`. |

Each strategy emits its candidates independently. The merge step
deduplicates by `suggested_folder_id`, keeping the highest-confidence
candidate (with its source recorded). The partial unique index
`(tenant, document, folder, source) WHERE pending` collapses
at-least-once redeliveries.

### Why not auto-move

Three reasons:

1. Folder placement crosses workspace boundaries; permission checks
   live in the document service's gRPC, not in intelligence.
2. Lifecycle states (`legal_hold`, `superseded`, `retained`) gate
   moves. Replicating the gate in intelligence is a bug factory.
3. The acceptance flow already records `filing_history` with
   `was_suggestion=true`, so the learning signal is identical
   whether the user accepts the suggestion or moves manually.

When auto-move lands (post-v1), it will be an internal-API
`POST /internal/v1/documents/{id}/auto-move` that the intelligence
task calls, with the document service enforcing all the gates.

### Filing history is the learning loop

Every successful `MoveDocument` and `CreateDocument` (with a folder
on creation) writes one row to `filing_history`. If the move matches
a `pending` `route_suggestion`, the matching suggestion is flipped to
`accepted` and `was_suggestion=true` / `suggestion_rank` are recorded
in the same tx. Without this loop the history strategy never
improves — every signal must trace back to a user action.

### Tenant isolation

  * RLS + FORCE on all four new tables.
  * The intelligence task uses `set_config('app.current_tenant', $1)`
    on every connection before the first read or write.
  * Qdrant similarity queries filter on `tenant_id` payload (existing
    pattern from `dup_embedding`).
  * The composite FKs on every folder/document column prevent
    cross-tenant references at the DB layer regardless of code bugs.

### Outbox

The smart_route task writes all suggestions and one
`dms.routing.completed.v1` outbox row in the same tx. The
document-service publisher already polls the outbox; no separate
infrastructure for intelligence-emitted events.

The accept path (handler) writes `dms.routing.accepted.v1` for the
audit chain. Reject is `dms.routing.dismissed.v1`.

## Consequences

  * Two routes (`accept`, `dismiss`) on the document side; the move
    itself reuses the existing `MoveDocument` service method.
    Permission, lifecycle, and audit guarantees come for free.
  * `filing_history` will grow linearly with document volume. Index
    by `(tenant_id, category_key, filed_folder_id)` makes the
    history strategy O(distinct_folders_for_category) per lookup.
    Future: per-user / per-workspace history slicing.
  * The similarity strategy depends on the embedding pipeline
    completing for both this doc and its neighbors. For the first
    few hundred docs in a fresh tenant the similarity signal is
    weak; rule + history carry the load.
  * UI signals confidence with a band (green ≥ auto_move_threshold,
    amber ≥ 0.80, orange below) — `auto_move_threshold` here is
    UI-only since we don't auto-move; it lets admins tune what
    "very confident" means without changing code.

## Alternatives considered

  * **Per-user routing rules** — rejected for v1. Tenant-wide rules
    cover ~80% of the value; per-user is a Wave-N feature.
  * **Move documents in bulk on rule creation** — rejected. A new
    rule shouldn't retroactively reorganise the corpus; users
    expect rules to apply forward only. Backfill would be a
    separate explicit admin action.
  * **LLM-driven routing decisions** — rejected. Cost/latency
    not justified when explicit rules + history already cover the
    common case. Reserve for the long-tail bucket later.
