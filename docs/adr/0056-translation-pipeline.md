# 0056 — Translation pipeline

- **Status:** Accepted
- **Date:** 2026-05-03
- **Supersedes:** —
- **Deciders:** core eng + intelligence

## Context

Tenants in MENA / EU / APAC routinely upload documents in their
local language but search and review them in English (or vice
versa). Today the OCR text is stored verbatim and the whole
intelligence pipeline (classify, NER, RAG) operates on the source
language. That works when models are multilingual; it doesn't when
the user just wants to *read* the doc in their language.

We want:

  * Auto language detection on every OCR'd version (cheap, ~10ms).
  * On-demand translation to a user-chosen target language using the
    existing `llm_gateway` (so tenants get OpenAI / Ollama / their
    own provider — no new vendor lock-in).
  * Persisted translations so re-asking is free.
  * A clear cost guard — translation is the most expensive operation
    in the pipeline per token.

## Decision

Two new Celery tasks plus three REST endpoints. Translations are
**separate artifacts** keyed on `(version_id, target_language)`; the
source document is never modified.

### Two tables, one config

  * `document_languages` — one row per version. Detected language +
    confidence + a JSONB list of secondary languages (multilingual
    docs). Auto-populated post-OCR.
  * `document_translations` — one row per (version, target_language)
    with status state machine (pending → processing → completed |
    failed). The translated text lives inline in `translated_text`;
    `translated_blob_id` is reserved for a v2 PDF render that
    preserves layout.
  * `translation_config` — per-tenant: enable, allowed target
    languages, default target, **max_chars_per_doc** (cost guard),
    optional `model_override` for translations specifically.

UNIQUE on `(tenant_id, version_id, target_language)` collapses
duplicate translation requests — re-clicking "Translate to Arabic"
on a doc that already has one is a no-op (the API returns the
existing row).

### Detection: langdetect

`pip install langdetect` (added to `services/intelligence/requirements.txt`).
~3 MB pure-Python, deterministic when seeded. Returns ISO 639-1 codes.
For multilingual docs we call `detect_langs()` and surface the top-3
in `secondary_languages`.

We considered lingua-py (more accurate, ~140 MB) and rejected it —
the accuracy gap matters at <50 chars; OCR'd documents are always
larger so langdetect is a clean win on size + cold-start time.

### Translation: chunked LLM calls

`tasks/translate.translate` workflow:

  1. Fetch OCR text (`ocr_results.full_text`).
  2. Reject if `len(text) > config.max_chars_per_doc` — surfaces in
     the row's `error_message`, `status='failed'`.
  3. Chunk at paragraph boundaries via the existing
     `app.chunker.chunk_text` with a token budget of 4000 per
     chunk (well under any modern LLM context window).
  4. Translate each chunk via `llm_gateway.completion` with a
     deterministic prompt (temperature 0). System prompt fixes the
     output discipline (no commentary, preserve formatting).
  5. Concatenate chunks, persist `translated_text`, mark `completed`,
     emit `dms.translation.completed.v1` outbox event.

A failure on any chunk fails the whole translation (status='failed',
error_message captures the cause). We don't partial-emit — half-
translated docs are worse than no translation.

### Tenant isolation

  * `set_config('app.current_tenant', $1)` on every connection.
  * `requested_by` recorded on every `document_translations` row.
  * The translation REST endpoint requires a valid session
    (X-Tenant-ID + X-User-ID).

### Cost guard

The 100 000-char default ≈ 25 000 input tokens ≈ $0.10 with GPT-4o
mini. Tenants who want larger jobs set `max_chars_per_doc` higher
explicitly, putting the spend on their own decision. Observability
inherits `llm_gateway._meter_usage` — every translation flows
through the existing per-tenant Redis usage hash.

### Why translation is *not* event-triggered

Auto-translating every uploaded document on every OCR completion
would be ruinous on cost. Translation is always user-initiated.
Language detection is the only auto step (cheap, no LLM call).

### Why translations are inline TEXT, not a content_blob

For v1 the translated text is rendered in a side-by-side viewer in
the web app. No PDF rendering yet. When that lands (v2), the
existing `translated_blob_id UUID` column points at a
content_blobs row with the layout-preserving PDF — same envelope
encryption, same content addressing as any other blob.

## Consequences

  * Each translation costs real money. The dashboard must surface
    the running cost (existing usage hash already feeds the billing
    UI; nothing new here).
  * Reviews + classifications run on the *source* language only.
    A translated doc isn't re-classified or re-NER'd — the original
    is still the source of truth for everything else.
  * Right-to-left languages (Arabic, Hebrew, Persian) need a
    `dir="rtl"` attribute on the viewer's right panel; this is a UI
    detail the frontend handles per ISO code.
  * Re-uploading a new version triggers fresh language detection
    on the new version_id; existing translations stay attached to
    their original version_id, so users can still see them but
    they won't reflect the new content. The UI shows a "translation
    out of date" indicator when the latest version_id differs.

## Alternatives considered

  * **Self-hosted NMT (Helsinki-NLP / Opus-MT)** — rejected for v1.
    Would add ~2 GB of model weights per language pair to the
    intelligence container. The LLM gateway already exists and
    handles every language pair through one model.
  * **Auto-translate on every upload** — rejected. Cost.
  * **Streaming translation** — rejected. Translations are a
    one-shot artifact (you save it and re-read it); the streaming
    UX from the Q&A panel doesn't add value here.
  * **Translation memory / glossary** — deferred. A per-tenant
    glossary table where "VaultDMS" always becomes "VaultDMS"
    (not "ManoArchivo Documentaria") is the obvious v2.
