# 0060 — Active learning pipeline

- **Status:** Accepted
- **Date:** 2026-05-03
- **Supersedes:** —
- **Deciders:** core eng + intelligence + ML

## Context

The classify task today uses a fixed DistilBERT checkpoint — no
tenant adaptation, no learning from user corrections. The
classification corrections ledger (ADR 0059) captures every "the
model said X, the user said Y" pair, which is exactly the supervised
training signal a fine-tune needs. We want to close the loop:
correction → training set growth → automatic re-train → eval against
production → admin-gated promotion.

## Decision

Three Celery tasks chained off the corrections ledger plus a model
registry:

```
dms.classify.corrected.v1 (ADR 0059 outbox)
  → app.tasks.training_collector
       inserts into training_examples (with train/val/test split)
       checks count thresholds, dispatches retrain when met
  → app.tasks.model_retrain   (runs on dedicated GPU queue)
       fine-tunes DistilBERT on tenant's accumulated examples
       writes a model_versions row with status='evaluating'
       uploads weights to MinIO under models/{tenant}/{model_type}/{version}/
  → app.tasks.model_evaluate
       loads new + current production models, runs both on test split
       computes accuracy/precision/recall/F1 + per-class breakdown
       flips status to 'candidate' (or 'retired' if not better)
       optionally auto-promotes when config.auto_promote_if_better
```

The classify task gains a per-tenant model loader: when a
`production` model exists for `(tenant, classification)` it loads
from MinIO into an LRU cache (max 10 tenant models in memory) and
uses it. Otherwise it falls through to the existing default
classifier (no behavior change for tenants who haven't opted in).

### Why DistilBERT

  * Already in `requirements.txt` via `transformers`.
  * Small enough to fine-tune on CPU in 5–15 min for 100–1000
    examples; GPU shrinks that to seconds.
  * Strong baseline accuracy on text classification.
  * Per-tenant fine-tunes typically need only a classification
    head swap + a few epochs; the base encoder weights stay
    largely frozen.

We considered larger encoders (RoBERTa, DeBERTa) and rejected for
v1: they're 2–4× the size on disk + memory and the accuracy gap on
tenant-scale data (often <1000 examples) is small.

### Per-tenant isolation

  * Models are stored under `models/{tenant_uuid}/{model_type}/{version}/`
    in MinIO. Cross-tenant read is impossible because the path
    contains the tenant id and the storage role's policies scope
    by path prefix.
  * Each tenant has independent `model_versions` rows (RLS).
  * The training_examples table is RLS+FORCE.
  * The in-memory LRU cache in the classify task keys by tenant id.

### Threshold defaults

  * `min_examples_for_retrain = 50` — DistilBERT needs at least
    a few dozen labelled examples per class to outperform the
    base model with frozen encoder + classifier head.
  * `retrain_increment = 25` — after the first train, every 25
    new corrections triggers a re-train.
  * `min_accuracy_improvement = 0.02` — a candidate model must
    beat the current production model by at least 2 percentage
    points on the held-out test set to even be marked
    `candidate`. Below that it's `retired` immediately.

### Train / val / test split

90/5/5 by default — most tenants are training-data-poor, so a 10%
val + 10% test would leave 40 docs for training out of 50. We use
random assignment at example-collection time (deterministic per
correction id) so re-running the trainer doesn't reshuffle.

### Promotion model

Manual by default. Admin sees a "candidate" row in the model
dashboard with old vs new metrics side-by-side, clicks Promote,
which:
  1. Sets new model `status='production'` (the partial unique
     index ensures the previous production row gets atomically
     swapped).
  2. Sets the previous production model `status='retired'`.
  3. Emits `dms.model.promoted.v1` so the classify task workers
     evict their cached old model.

`config.auto_promote_if_better` flips the eval task to do this
automatically. Off by default — accuracy regression caught by
later corrections costs more than a manual click.

### GPU resource management

`config.gpu_queue` (default `intelligence-gpu`) directs the
retrain task to a dedicated Celery queue. Operators run a
GPU-attached worker subscribed to that queue. CPU-only deployment
just uses the default `intelligence` queue and accepts the slower
training time.

### Rollback

A retired model row keeps its `s3_artifact_path` indefinitely.
Reactivating an old version is a manual UPDATE
`status='production'` on its row + the partial unique index will
prevent two production rows existing at once. The migration
doesn't garbage-collect retired model artifacts; an operator
cleanup script handles that on a per-tenant basis.

## Consequences

  * Each retrain is real cost — CPU minutes (or GPU seconds) +
    MinIO storage (~50 MB per fine-tuned DistilBERT). Tenants
    opt in via `enabled=true`; default off keeps the cost
    predictable for the platform.
  * Cold-start: a new tenant has no production model. The classify
    task falls through to the default. Once they accumulate 50
    corrections + the first retrain completes + an admin promotes
    it, subsequent classifications use the tenant model.
  * The unique partial index `(tenant_id, model_type) WHERE
    status='production'` is the only way to enforce "exactly one
    production model per tenant per model_type". Application
    code MUST flip the previous production to retired in the
    same tx as the new one's promotion — partial-promotion
    failure leaves the unique index violation rolled back, so
    the inconsistency is impossible.
  * Per-class accuracy in eval_metrics is the real promotion
    signal. A model that improves overall accuracy by 3% but
    drops a critical class (Invoice → 50% recall) should NOT be
    promoted. The admin UI surfaces per-class metrics
    prominently for this reason.
  * No SHAP/LIME explanations in v1. A wrong production model
    produces wrong classifications until corrected; the
    correction loop is the explanation surface.

## Alternatives considered

  * **Continual learning (no full re-train)** — rejected. The
    catastrophic-forgetting risk on small per-tenant data is
    real, and full re-train every 25 corrections is cheap
    enough.
  * **Shared model with tenant-conditional embedding** — rejected
    because it pulls all tenants' corrections into one corpus,
    which both leaks data conceptually (model parameters
    encode all tenants) and prevents per-tenant promotion
    decisions.
  * **Active sampling (let the model pick what to label next)** —
    deferred. The corrections we get are user-driven, not
    model-driven; a separate pre-labelling queue is a future
    feature.
