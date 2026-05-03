# 0058 — Anomaly detection (workspace-level outliers)

- **Status:** Accepted
- **Date:** 2026-05-03
- **Supersedes:** —
- **Deciders:** core eng + intelligence

## Context

Per-document intelligence (classify, NER, compliance, OCR quality)
catches problems with individual files. None of it answers "does
this document *belong* in this workspace?" — the cross-document
question that catches misfiled invoices in a contracts folder, a
247 MB scan in a workspace where the median is 3 MB, or a 3 a.m.
Sunday upload spree by a user who normally only files Mon–Fri.

We want a workspace-scoped outlier scan that:

  * Aggregates metadata, content embeddings, and upload-behavior
    into one report.
  * Runs on demand (admin clicks a button) for v1; scheduled
    weekly cron is a follow-up that depends on Celery Beat
    actually running in the deployed stack.
  * Never auto-acts — every finding requires human review.

## Decision

A new Celery task `app.tasks.anomaly_detect.run` invoked by REST
(`POST /api/v1/admin/anomalies/run`). Three independent strategies
fan into one report:

| Strategy     | Signal                                                    |
|--------------|-----------------------------------------------------------|
| `metadata`   | z-score on `size_bytes` and OCR `word_count` over the workspace's documents. Any doc with `\|z\| > z_score_threshold` is flagged. Plus *misclassified* — a document whose `document_class` appears in <10% of the workspace. |
| `content`    | Pull every doc's first chunk vector from Qdrant (one per doc), compute the workspace centroid, flag docs with cosine distance to centroid > `content_distance_threshold`. |
| `behavioral` | Two checks: *unusual_upload_time* (uploads outside 06:00–22:00 local-time-naive) and *rapid_uploads* (one user uploading > 50 docs/hour). |

A `combined` analysis runs all three in one report. The frontend
defaults to `combined`.

### Why not auto-action

Same conservative pattern as ADRs 0052, 0053, 0054, 0055, 0057:
intelligence flags, the human decides. Anomalies are the most
expensive false positives in the platform — auto-deleting or
auto-moving an outlier on a model's say-so is a foot-gun.

### Min-documents floor

`min_documents_for_analysis` (default 20, minimum 5 by CHECK)
guards against statistically meaningless analysis on a 3-doc
workspace. Below the floor the report completes immediately with
status='completed' + `summary={'reason': 'insufficient_documents'}`
and zero findings.

### Schedule

`schedule_cron` lives in `anomaly_config` for completeness, but
v1 doesn't actually run a scheduler. Celery Beat isn't currently
deployed alongside the workers. The manual REST trigger covers
the use case; scheduled execution is a Wave-N follow-up.

### Persistence

  * `anomaly_reports` — one per run. Status state machine
    `pending → processing → (completed | failed)`. `summary` JSONB
    holds counts per strategy + tunables used (so a re-read shows
    which thresholds the report was generated under).
  * `anomaly_findings` — one row per (report, document, anomaly_type)
    via UNIQUE constraint. The same document can appear under
    multiple types (size_outlier AND content_outlier are independent
    signals). ON DELETE CASCADE from the report.

### Tenant isolation

  * RLS + FORCE on all three tables.
  * The task accepts `tenant_id` from the REST handler (which
    derives it from the session); no cross-tenant iteration
    inside the task itself in v1.
  * Qdrant scroll filters on `tenant_id` payload (existing
    pattern from `dup_embedding`/`smart_route`).

## Consequences

  * Each `combined` run pulls every document's first chunk vector
    from Qdrant. For a 10k-doc workspace that's 10k×384 floats
    ≈ 15 MB at the embedder's default dim. We cap workspace
    size at 50k docs in the task; above that, the run completes
    with `summary={'reason': 'workspace_too_large'}` and a
    pointer to the per-workspace path (not yet implemented).
  * The `behavioral` strategy needs upload timestamps and uploader
    ids — those live on `documents.created_at` and
    `documents.created_by`, which we already query.
  * The `misclassified` heuristic (<10% of workspace shares this
    class) is intentionally crude — a workspace can legitimately
    have a long tail of one-off classifications. The UI surfaces
    this as low-severity; the user judges.
  * Reports accumulate. The schema doesn't auto-prune; a future
    Temporal cron will drop reports older than 90 days. Tenants
    who run analyses daily will accumulate ~500 reports/year.

## Alternatives considered

  * **Isolation Forest / OneClassSVM for content anomalies** —
    rejected for v1. The implementation is heavier (sklearn
    dep, model training per workspace), and centroid distance
    catches the same gross outliers. Revisit if the false-
    positive rate is too high.
  * **Tenant-wide Temporal cron** — rejected. Temporal already
    runs in the stack but adding a per-tenant scheduled
    workflow per intelligence feature is a coupling we don't
    want to take on yet.
  * **Inline anomaly check during upload** — rejected. The
    workspace-level statistics need a meaningful sample size;
    per-upload checks against an empty workspace produce noise.
    This is the right shape for a periodic scan.
