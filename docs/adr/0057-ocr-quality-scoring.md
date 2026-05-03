# 0057 — OCR quality scoring

- **Status:** Accepted
- **Date:** 2026-05-03
- **Supersedes:** —
- **Deciders:** core eng + intelligence

## Context

The OCR pipeline (`app.tasks.ocr`) writes a `confidence` column per
page on `ocr_results.pages`, but that single number is misleading
on its own:

  * Tesseract's word-level confidence is well-calibrated on clean
    scans but reports ~85% on garbled output that humans would
    immediately reject.
  * Surya doesn't always populate confidence in our current
    integration; many pages land at 0.0 even when the text is fine.
  * A perfectly-confident OCR pass over a near-blank scan
    produces a high "confidence" score but no usable text.

We want a composite quality grade per page so:

  1. Users see "this scan is poor, consider rescanning" before
     they invest time reading.
  2. Admins get a tenant-wide review queue.
  3. The pipeline can auto-retry OCR with a different engine when
     scores are catastrophically bad.

## Decision

A new Celery task `app.tasks.ocr_quality.score` chained from
`dms.version.ocr_completed.v1` with its own NATS durable. Five
sub-scores combined into one composite, four-tier grade, per-page
issues list, optional auto-retry, optional notification.

### Five sub-scores, one composite

```
char_confidence  35%   OCR engine's own number (averaged + normalised)
word_density     15%   words / 100 (capped at 1.0). Catches blank/near-blank pages.
line_regularity  15%   1 - stddev(line_heights) / mean(line_heights). Detects skewed scans.
noise_ratio      15%   1 - non_alphanum / total. Detects garbled OCR.
language_score   20%   real-word ratio via length + alpha-only heuristic.
```

Weights live in code (`QUALITY_WEIGHTS` constant) — not user-tunable
in v1. The four grade thresholds are tunable per tenant via
`ocr_quality_config`. CHECK constraints enforce
`excellent_threshold >= good_threshold >= fair_threshold` so the
table can never represent inverted gating.

### Why not a real dictionary check

The spec floats `enchant` or `nltk.words` for `language_score`.
Both pull system libraries (libenchant, NLTK corpora ~50 MB) and
neither helps with multilingual documents — an Arabic page would
have ~0% English-dictionary coverage and look "garbled" by that
metric. We use a structural heuristic instead:

  * Token length 2–20 chars
  * ≥80% alphabetic characters per token
  * Token ratio = `valid_tokens / total_tokens`

Cheap, language-agnostic, captures the actual signal (real-word
shape vs OCR garbage like `&^%$#@!`). When dictionary-quality
grading lands, it'll be a per-language plug-in not a swap.

### Auto-retry policy

When `overall_score < auto_retry_below` AND the original engine
was Tesseract AND we haven't already retried, the task emits
`dms.version.ocr_retry_requested.v1` with `force_engine='surya'`.
The OCR rerun handler picks it up via the existing rerun path
(same code that handles a manual rerun).

Retry-of-retry is suppressed by checking `ocr_quality_summary.
auto_retried` — once true, no more automatic retries even if
the new score is still poor. Manual rerun is always available.

If the original engine was already Surya, no retry — there's no
better engine to fall back to. The page goes into the review queue.

### Notification

When `notify_on_poor = true` and the document grade is `poor`,
the task emits `dms.notification.send.v1` to the outbox. The
notification service already subscribes to that subject.

Default off — opt-in to avoid spamming notifications for tenants
who routinely upload poor-quality scans (legacy archives, etc.).

### Persistence shape

Two write targets in the same tx:

  * `ocr_quality_scores` — one row per page (UPSERT on
    `(tenant, version, page)`). Preserves per-page review state
    across re-scoring.
  * `ocr_quality_summary` — one row per document (UPSERT on
    `(tenant, document)`). Replaced wholesale on each scan; the
    `auto_retried` flag survives across versions because we lift
    its prior value before the UPSERT.

### Tenant isolation

  * RLS + FORCE on all three tables.
  * `set_config('app.current_tenant', $1)` on every connection.
  * Composite FKs prevent cross-tenant document/version
    references.

## Consequences

  * The composite score is a single number that hides which
    sub-score dropped it. The per-row `issues TEXT[]` is the
    user-facing explainer (`['low_confidence','noisy']`). UI
    surfaces both.
  * Re-scoring an already-reviewed page keeps the `reviewed`
    flag — UPSERT only updates the score columns, not the
    review state.
  * The auto-retry path crosses two systems (intelligence emits
    the event, the document/storage rerun path handles the
    retry). Failures are observable via the existing OCR rerun
    metrics; no new dashboards.
  * OCR engines that don't report confidence (Surya in some
    versions) get `char_confidence = NULL` on the row but the
    composite still works — the weight just shifts to the
    other four sub-scores via re-normalisation in the scorer.

## Alternatives considered

  * **ML-based quality model** — train a small classifier on
    labelled OCR output. Rejected for v1: no labelled corpus,
    and the heuristic + composite gives a strong baseline. The
    schema doesn't preclude swapping the scorer later.
  * **Surface raw OCR confidence only** — what exists today.
    Rejected because it's misleading on the failure modes the
    user actually cares about (blank pages, garbled output).
  * **Auto-retry on every poor page** — rejected. Cost +
    redelivery loops. The single-shot retry with engine swap
    is a clean cap.
  * **Score during OCR, not post-OCR** — rejected. Keeps the
    OCR task tight (no scoring deps in `tasks/ocr.py`); the
    chained scoring task stays independently retriable and
    deployable.
