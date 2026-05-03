# 0052 — Auto-tagging from NER + classification

- **Status:** Accepted
- **Date:** 2026-05-03
- **Supersedes:** —
- **Deciders:** core eng + intelligence

## Context

Tags on documents are a `TEXT[]` array column (`documents.tags`) with a
GIN index. They drive faceted search, smart folders, and the
share-link audience. Today every tag is added manually — a friction
point for high-throughput tenants (legal eDiscovery, accounts payable
inboxes) where a 5k-doc upload requires 5k manual tagging passes.

Intelligence already produces two signals that contain ~90% of the
useful tag content:

  * **`document_classifications`** (per-version category + top3) —
    written by `app.tasks.classify`. One row per version.
  * **`entities`** (NER over OCR text) — written by `app.tasks.ner`.
    Multiple rows per version: `entity_type`, `entity_value`,
    `confidence`.

Neither signal is currently surfaced as a tag. We want a closed-loop
auto-tagging pipeline that promotes high-confidence intelligence
output into `documents.tags`, surfaces medium-confidence picks as
review suggestions, and learns nothing without explicit admin
override.

## Decision

A new Celery task (`app.tasks.auto_tag`) chained from
`dms.classify.completed.v1` and `dms.ner.completed.v1`. Runs once per
trigger event, but is idempotent: at-least-once redelivery is
suppressed by the `intel_processed_events` ledger (consumer
`auto_tag`) plus a partial unique index on
`tag_suggestions(tenant_id, document_id, tag_name, source) WHERE
status='pending'`.

### Two write targets

  1. **`tag_suggestions`** (new table) — every candidate tag the
     pipeline generates, with `source`, `confidence`, and a
     `source_detail` JSONB capturing why it was suggested
     (`{entity_type, entity_text}` for NER; `{category_key,
     model_version}` for classification).
  2. **`documents.tags`** (existing array) — only when the candidate's
     confidence ≥ tenant's `auto_apply_threshold`. The suggestion row
     is written with `status='auto_applied'` so the audit chain is
     complete.

### Per-tenant config

Single row in `auto_tag_config` keyed by `tenant_id`. Defaults:

  * `enabled = true`
  * `auto_apply_threshold = 0.95`
  * `suggest_threshold = 0.60`
  * `max_tags_per_document = 20`
  * `blocked_tags = []`
  * `source_weights = {ner:1.0, classification:0.8, llm:0.9, pattern:0.7}`

A CHECK constraint enforces `auto_apply_threshold >= suggest_threshold`
so the table can never represent an inverted gating.

### Source-weight rationale

Source weights multiply the model's raw confidence before threshold
comparison. NER weights highest because entity confidence is well-
calibrated on our model; classification gets 0.8 because category
ambiguity is real (Invoice vs Receipt vs Statement). LLM and pattern
sources are placeholders for future expansion.

### Tag generation rules (NER → tag)

Codified in `_tag_from_entity`:

| Entity type | Rule |
|---|---|
| `ORG`, `COMPANY` | tag = lowercase value |
| `PERSON` | tag = `person:<lowercased>` *only if confidence > 0.80* |
| `LOCATION`, `GPE` | tag = lowercase value |
| `DATE`, `MONEY`, `CARDINAL`, `ORDINAL` | skip — too noisy |
| other | tag = `<type>:<value>` *only if confidence > 0.85* |

The `person:` and `<type>:` prefixes prevent collision with manual
tags and let the search facet group them.

### Tag generation rules (classification → tag)

  * Top-1 category becomes a tag with the model's confidence
  * Top-2 and Top-3 from `top3` array become tags with their
    individual scores (typically much lower; will fall under
    `suggest_threshold` for routine docs)

### Outbox

The Celery task writes the suggestion row(s) AND the
`dms.autotag.completed.v1` outbox row in the same asyncpg
transaction. The intelligence service has access to the same
Postgres as document service; we deliberately reuse the
document-service `outbox` table rather than duplicate one for
intelligence — the publisher already polls it, and the event type
namespace is unambiguous.

### Frontend gating

Suggestions panel polls every 10s via TanStack Query. Three
confidence bands:

  * green ≥ 0.90 (Accept-all-high-confidence button)
  * yellow ≥ 0.70
  * orange ≥ `suggest_threshold` (tenant-configured, default 0.60)

Auto-applied tags render with a checkmark + "Auto-applied" pill so
users can distinguish manual from machine origin.

### Audit chain

Every state transition on a `tag_suggestions` row writes
`reviewed_by` + `reviewed_at`. Combined with the outbox events
(`dms.autotag.completed.v1` on creation, `dms.autotag.reviewed.v1`
on accept/reject), the audit service can reconstruct the full tag
history for any document.

## Consequences

  * `documents.tags` gains machine-origin tags. Search facets need no
    change — the array is already indexed and searched the same way.
  * The unique index on `(tenant, doc, tag, source) WHERE pending`
    means a re-run of intelligence never creates duplicate pending
    rows. Once a suggestion is accepted/rejected, future runs can
    re-suggest the same tag (allowed — model improvements are real).
  * Auto-apply is deliberately conservative (0.95 default) to keep
    user trust. Tightening or loosening is a per-tenant slider —
    no code change.
  * Cross-tenant safety is identical to existing intelligence tasks:
    `set_config('app.current_tenant', $1, true)` on every connection
    before the first write; RLS denies any leak.

## Alternatives considered

  * **Embedding-based tag generation** — extract candidate tags by
    nearest-neighbour search against an existing tag corpus.
    Rejected for v1: requires building a per-tenant tag-vector
    index. Revisit if NER+classification miss a class of documents.
  * **LLM-only tagging** — prompt an LLM with the OCR text and ask
    for tags. Rejected: cost per doc is ~10× the existing pipeline,
    and the LLM gateway already exists for higher-value features
    (Q&A, summarization). Reserve `source='llm'` in the schema for
    future use.
  * **Auto-apply without admin override** — rejected. Threshold
    alone doesn't catch domain-specific false positives; per-tenant
    `blocked_tags` is the safety valve. The admin queue is the
    feedback loop.
