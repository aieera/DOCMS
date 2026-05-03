# ADR 0061 — NER Pipeline (SpaCy + Regex + LLM Ensemble)

Date: 2026-05-03
Status: Accepted

## Context

Auto-tagging (ADR 0052), Compliance (ADR 0054), and Active Learning
(ADR 0060) all consume rows from `document_entities`. Today the
intelligence service writes that table from a SpaCy + regex pipeline
that detects only PERSON / ORG / LOC / DATE plus a handful of regex
PII types (email, phone, SSN, credit card, DOB).

Blueprint §6.6 expands the entity taxonomy in three ways the existing
implementation doesn't cover:

1. **Financial** — amount, currency, account_number, tax_id
2. **Legal** — party_name, effective_date, jurisdiction, governing_law
3. **Medical** — patient_id, ICD codes, CPT codes

These types are either context-dependent (an "amount" only counts when
a currency anchor is nearby; a "party_name" requires understanding of
contract structure) or operate in a closed vocabulary that regex
libraries already cover (ICD-10, CPT). SpaCy's pretrained model can be
fine-tuned for the closed-vocab cases, but the legal/contract types
need real reading comprehension.

## Decision

Run a **three-tier ensemble** in this order on every OCR-completed
document:

1. **Regex** (fast, deterministic) — email, phone, SSN, credit card,
   DOB, ICD-10 (`[A-TV-Z]\d{2}(\.\d{1,4})?`), CPT (`\d{5}`), tax_id
   patterns by jurisdiction. Source recorded as `'regex'`.
2. **SpaCy `en_core_web_trf`** — PERSON / ORG / GPE / LOC / DATE /
   MONEY / PERCENT / CARDINAL. Maps to our taxonomy:
   PERSON→`name`, ORG→`party_name` (when in a contract context), GPE→
   `jurisdiction`, MONEY→`amount`+`currency`. Source `'spacy'`.
3. **LLM (opt-in, per-tenant)** — only the types that survive the
   first two passes ungathered: `governing_law`, `effective_date`,
   `address`, `national_id`, `account_number`, `patient_id`. Source
   `'llm'`.

The three passes write to the **same** `document_entities` table with
a new `source` column so downstream consumers (compliance dashboard,
auto-tag, active learning, redaction) don't have to learn a new
schema. Dedupe is character-offset based — if regex and LLM both find
the same span, regex wins (cheaper + deterministic).

### LLM provider

`litellm` is already a dependency. Default model: `claude-haiku-4-5`
(Anthropic offers a BAA which is required for the medical use case).
Per-tenant override via `ner_config.llm_model` accepts any
litellm-supported provider string, including `ollama/llama3.1:8b` for
on-prem / air-gapped tenants.

LLM batching: pack up to `ner_config.llm_batch_size` (default 5) short
documents into one prompt, structured-output enforced via Anthropic
tool-calling so the model can't return free-form garbage. Validate
that every emitted `(value, char_start, char_end)` triple actually
matches the source text — LLMs hallucinate offsets. Drop on mismatch.

### Why one table, not `ner_results` JSONB array

Blueprint suggested a `ner_results` table with `entities_json` JSONB
array. We chose to extend `document_entities` instead because:

- Compliance's `WHERE entity_type IN ('email','phone',...)` queries
  use `idx_document_entities_type`. JSONB containment queries can't
  use that index without a GIN rebuild.
- Active Learning's per-label retrain trigger reads counts grouped by
  type — same indexed-grouping consideration.
- Auto-tag joins `document_entities` to `tag_suggestions` with
  per-row dedupe semantics. JSONB array would require lateral
  unnesting on every read.

The `source` column gives us the per-row provenance the JSONB shape
was meant to provide, without forcing every existing reader to
change.

### Active learning signal

Manual relabel / add / delete actions land in `entity_corrections`
(append-only ledger, mirrors `classification_corrections`). The
training collector (ADR 0060) gets a new branch that ingests this
table for tenant-specific NER head fine-tuning when a tenant has
enough corrections. Until then the LLM call carries the tenant
through novel-type cases.

## Consequences

**Pro**

- One write path, four readers — no migration cost for compliance /
  auto-tag / AL / redaction.
- LLM cost is bounded: only runs on types regex+SpaCy missed, and
  only for tenants who opted in.
- Same correction-ledger pattern Active Learning already understands.

**Con**

- `document_entities` is now multi-source. Readers that need
  "highest-confidence-only" semantics have to filter by source.
- LLM offset validation requires re-finding the substring server-
  side, which is O(N×M) for N entities in M-char text. Capped at
  short docs in the batch path; long docs run regex+SpaCy only.

## Out of scope (follow-ups)

- `pkg/llm` — currently we call `litellm.acompletion` inline from
  `ner.py`. A proper provider-agnostic abstraction with retries,
  caching, and per-tenant rate limiting belongs in `pkg/llm` once we
  have a second consumer (Doc Q&A is a candidate).
- PDF coordinate-overlay highlighting. Today the Entities tab and
  text panel highlight by character offset in the OCR text view; PDF-
  canvas overlay needs OCR word-bbox metadata which we don't persist
  today.
- ICD-10 closed-vocab validation. The regex matches the *shape*; a
  follow-up should join against an ICD-10 table to filter false
  positives like "M00.00" inside a measurement.
