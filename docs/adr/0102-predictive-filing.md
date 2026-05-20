# ADR 0102 — Predictive filing suggestions

Status: Accepted (Phase 1 backend + upload UI shipped; frequency
heuristic only; real ML model deferred to Phase 2)
Date: 2026-05-19
Related: §18 Feature 9 of the blueprint; ADR 0052 (auto-tag),
ADR 0053 (smart routing), ADR 0058 (anomaly), ADR 0059 (corrections
ledger), ADR 0060 (per-tenant classifier), ADR 0078 (LLM NER).

## Context

When a user drags a file into the uploader, the system already has
enough information at the OCR-completed moment to produce three useful
suggestions:

1. **Classification** — what *kind* of doc this is (contract, invoice,
   policy, …). Shipped via ADR 0060 per-tenant classifier.
2. **Tags** — auto-tag suggestions from ADR 0052.
3. **Folder** — *where* to file the doc. **Not yet shipped.** This is
   the new piece in §18 F9.

The blueprint asks for "predict folder via per-tenant trained model on
filing history." That's real ML work (build a `filing_decisions`
corpus, train a per-tenant model, evaluate, ship). Phase 1 ships a
**frequency-based heuristic** that's good enough for the common case
("if every invoice from vendor X gets filed into Folder Y, suggest
Folder Y for the next one") and gets the UX in place so we can collect
the labelled data that a future model needs.

The blueprint also asks for an active-learning loop. We have the
plumbing (`training_examples`, `active_learning` tables from ADR 0060)
but Phase 1 just logs accept/reject decisions to a new
`filing_decisions` table; the loop that feeds those decisions back
into a model is Phase 2.

## What is shipped now (Phase 1)

- This ADR.
- Migration `000049_filing_decisions.up.sql` — append-only table that
  records every (suggestion, decision) pair. Becomes the training
  corpus for the Phase 2 model.
- `POST /api/v1/uploads/predict`
  - Body: `{filename, mime_type, sha256?, workspace_id?}` (file bytes
    NOT sent here — predictions run pre-OCR on filename + MIME and
    post-OCR on extracted text).
  - Returns: `{classification, classification_confidence,
    suggested_folder: {id, name, score, reason}, suggested_tags: [...]}`.
  - Classification + tags come from existing intelligence-service
    tasks (no new ML work). Folder is the frequency heuristic below.
- `POST /api/v1/uploads/predict/feedback`
  - Body: `{prediction_id, classification_accepted, folder_accepted,
    tags_accepted: [...], tags_rejected: [...], final_folder_id,
    final_classification, final_tags: [...]}`.
  - Appends to `filing_decisions` for future training.
- Frontend upload modal: "Suggested filing" panel renders after file
  selection, with one-click "Accept all" or per-suggestion adjust.
  On submit, the modal POSTs feedback alongside the actual upload.

## What is **not** shipped now

- **Per-tenant trained folder model.** The heuristic below works fine
  when filing patterns are stable; it doesn't generalize to "this is a
  new vendor's first invoice — file it where you'd file invoices."
  Phase 2 trains a real model (LightGBM or fine-tuned classifier) on
  the `filing_decisions` corpus. The corpus needs ~500-1000 decisions
  per tenant to train usefully — Phase 1 is what collects them.
- **Active learning feedback loop.** Phase 1 *logs* every decision but
  doesn't re-train anything. Phase 2 wires the loop: nightly job
  trains the model on the corpus + last-30-days decisions, deploys to
  the registry (ADR 0060 model_registry), evaluation gate based on
  accept rate.
- **Confidence-based auto-file.** When confidence is high (>0.95 say),
  the playbook implies the file lands without asking. Phase 1 always
  asks. Auto-file is a Phase 3 trust-model decision, not a code one.
- **Cross-tenant model warm-start.** Some tenants have very little
  filing history. A future option is a base model trained on
  consenting tenants' anonymized patterns. Not in scope.
- **Playwright** multi-step test.

## Folder-suggestion heuristic (Phase 1)

The heuristic answers: "given a candidate file with predicted
classification `C` and predicted tags `T`, which folder has the
highest historical filing affinity for that combination?"

```sql
WITH candidate_docs AS (
  -- All documents in the tenant that share the candidate's classification
  -- and at least one of the candidate's tags.
  SELECT d.folder_id, COUNT(*) AS hits
  FROM documents d
  WHERE d.tenant_id = $1
    AND d.deleted_at IS NULL
    AND (d.document_class = $2 OR d.tags && $3::text[])
  GROUP BY d.folder_id
)
SELECT f.id, f.name, c.hits,
       c.hits::float / NULLIF((SELECT SUM(hits) FROM candidate_docs), 0) AS score
FROM candidate_docs c
JOIN folders f ON f.tenant_id = $1 AND f.id = c.folder_id
ORDER BY c.hits DESC
LIMIT 1;
```

Returns `{id, name, score (0-1), reason}` where `reason` is a
human-readable explanation like:

- `"73% of documents classified as 'invoice' are filed here"`
- `"15 documents with tag 'acme-vendor' live here"`

Or `null` when there's not enough history (we don't show a folder
suggestion below some threshold; better silent than wrong).

### Why heuristic before model

A frequency heuristic gets to "useful most of the time" in 3 hours of
work. A trained model is multi-week and needs the corpus the heuristic
builds. Ship the heuristic, collect data, train the model. The API
shape doesn't change when the model lands — only the code behind
`suggested_folder` does.

## DB schema (Phase 1)

```sql
CREATE TABLE filing_decisions (
  tenant_id              uuid        NOT NULL REFERENCES organizations(id),
  id                     uuid        NOT NULL DEFAULT gen_random_uuid(),
  user_id                uuid        NOT NULL,
  prediction_id          uuid        NOT NULL,                -- groups predict + feedback pair
  -- Predicted values (what the system suggested)
  predicted_class        text,
  predicted_class_score  real,
  predicted_folder_id    uuid,
  predicted_folder_score real,
  predicted_tags         text[]      NOT NULL DEFAULT '{}',
  -- Final values (what the user actually picked)
  final_class            text,
  final_folder_id        uuid,
  final_tags             text[]      NOT NULL DEFAULT '{}',
  -- Decision summary — booleans for the easy training-data extract
  class_accepted         boolean     NOT NULL,
  folder_accepted        boolean     NOT NULL,
  tags_accept_count      integer     NOT NULL DEFAULT 0,
  tags_reject_count      integer     NOT NULL DEFAULT 0,
  -- Context for the future model
  filename               text        NOT NULL DEFAULT '',
  mime_type              text        NOT NULL DEFAULT '',
  workspace_id           uuid,
  created_at             timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);

CREATE INDEX idx_filing_decisions_corpus
    ON filing_decisions (tenant_id, created_at DESC);
CREATE INDEX idx_filing_decisions_user
    ON filing_decisions (tenant_id, user_id, created_at DESC);

ALTER TABLE filing_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE filing_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY filing_decisions_tenant_isolation ON filing_decisions
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));
```

The Phase 2 trainer reads this table directly. Schema is intentionally
training-friendly (booleans for the easy `WHERE class_accepted = true`
labels) and includes filename/mime so the model can use them as
features alongside the predicted values.

## API contract

```http
POST /api/v1/uploads/predict
{
  "filename":    "Acme MSA 2026 v3.pdf",
  "mime_type":   "application/pdf",
  "workspace_id": "<uuid>"
}

200 OK
{
  "prediction_id": "<uuid>",
  "classification": {
    "predicted_class": "contract",
    "confidence":      0.91
  },
  "suggested_folder": {
    "id":     "<uuid>",
    "name":   "Vendor Agreements > Active",
    "score":  0.73,
    "reason": "73% of documents classified as 'contract' are filed here"
  },
  "suggested_tags": [
    {"tag": "msa",        "confidence": 0.88},
    {"tag": "acme",       "confidence": 0.81},
    {"tag": "vendor",     "confidence": 0.74}
  ]
}
```

```http
POST /api/v1/uploads/predict/feedback
{
  "prediction_id":          "<uuid>",
  "class_accepted":         true,
  "folder_accepted":        false,
  "tags_accepted":          ["msa", "vendor"],
  "tags_rejected":          ["acme"],
  "final_class":            "contract",
  "final_folder_id":        "<uuid>",
  "final_tags":             ["msa", "vendor", "renewable"]
}

204 No Content
```

## Open questions deferred

- **Should classification suggestion run on filename alone, or wait for
  OCR text?** Today the answer is "filename + MIME pre-OCR; refined
  after OCR completes." Phase 1 returns the filename-only prediction;
  the post-OCR refinement happens via the existing classify task that
  re-runs on `dms.version.ocr_completed.v1`. Frontend doesn't have a
  "refine" callback yet — Phase 2.
- **License gating.** Probably yes (`feature_flags.predictive_filing`)
  but not enforced until ADR 0095 license enforcement spreads beyond
  doc-service.
- **Privacy.** Filing decisions include filename + workspace context
  + tags. That's tenant-scoped (RLS) but the Phase 2 trainer needs
  to keep it that way — no cross-tenant feature mixing without
  explicit consent.
