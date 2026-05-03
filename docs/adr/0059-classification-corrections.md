# 0059 — Classification corrections ledger

- **Status:** Accepted
- **Date:** 2026-05-03
- **Supersedes:** —
- **Deciders:** core eng + intelligence

## Context

The classify task writes its current best guess into
`document_classifications`. When a user disagrees and re-classifies,
that re-classification has historically been an in-place overwrite —
no record of "the model said X, the user said Y". That makes it
impossible to:

  * Drive the active-learning loop (ADR 0060) — which needs labelled
    training pairs.
  * Audit how often the model is wrong, in which direction.
  * Roll back a bad bulk-reclassify operation.

## Decision

A new append-only `classification_corrections` table that records
every (`original_category`, `corrected_category`) pair with the user
who made the change. Single-document corrections come from the
existing tag-suggestion review surface; bulk corrections come from
a future admin UI; both write rows here.

Each correction emits `dms.classify.corrected.v1` via the outbox so
downstream consumers (active-learning training collector, audit log,
search reindex) react asynchronously.

### Why a separate table, not version on `document_classifications`

`document_classifications` is the *current* state. Versioning it would
either:
  * Bloat its hot-path read with an extra `is_current` filter, or
  * Need a parallel `document_classification_history` table that's
    nearly identical to what we'd write here anyway.

The corrections ledger is purpose-built: every row is a labelled
training example by construction, so the training collector reads
straight from it without a join.

### Source enum

`correction_source` distinguishes the entry point:

  * `manual` — single-document review via tag-suggestion or doc-detail flow
  * `bulk` — admin reclassify queue (future UI)
  * `api` — programmatic correction via REST

The active-learning collector treats them identically; the
distinction is for audit + UI display.

### Tenant isolation

  * RLS + FORCE on the table.
  * Composite FKs to documents/versions/users prevent cross-tenant
    references.
  * The handler always runs inside `database.WithTenantTx` so the
    outbox row + the corrections row + the
    `document_classifications` UPDATE happen atomically.

## Consequences

  * Every correction is a permanent ledger entry. No DELETE — admins
    can flag a correction as bad through the active-learning admin
    UI (drops it from the training set), but the row stays for audit.
  * The `document_classifications` table still holds the *current*
    label. The corrections table holds the *history*. The two are
    consistent because every UPDATE on `document_classifications`
    that comes from a user action MUST be paired with a correction
    row in the same tx.
  * Volume scales with correction frequency, not document count.
    A power-user tenant might generate ~1000 corrections/month;
    100k rows takes ~20 MB.

## Alternatives considered

  * **Re-use the `audit_events` partitioned table** — rejected.
    Audit events are unstructured key/value; the active-learning
    pipeline needs structured columns (text_content, label) that
    would force every consumer to JSON-parse.
  * **Trigger-based history** — Postgres trigger on
    `document_classifications` UPDATE that copies into a history
    table. Rejected: triggers hide the side effect from the
    application, and we want correction-source attribution which
    a trigger can't see.
