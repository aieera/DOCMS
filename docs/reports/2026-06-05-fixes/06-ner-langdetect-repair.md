# 06 — Repair NER + lang_detect pipeline tasks

**Commit:** `df8eec0` · **Type:** fix (intelligence) · **Date:** 2026-06-05

## Summary

Both intelligence-enrichment tasks crashed on every `dms.version.ocr_completed.v1`
event and retry-looped forever (found while debugging the vector-search
pipeline). Repaired both.

## Fix 1 — NER `OSError [E050]`

**Root cause:** `app/models/ner_model.py` hard-loaded `en_core_web_trf`, but the
Dockerfile never installs it — `python -m spacy download` resolves a model-index
URL that 404s on spacy 3.7.4, so the build step was commented out, and there was
no fallback. Every `detect_entities` task hit
`Can't find model 'en_core_web_trf'`.

**Fix:**
- Install `en_core_web_sm` at build time from its **pinned release wheel**
  (`github.com/explosion/spacy-models/.../en_core_web_sm-3.7.1-py3-none-any.whl`)
  — bypasses the broken model-index.
- Make the model configurable via `SEDOC_NER_SPACY_MODEL` (default
  `en_core_web_sm`; `en_core_web_trf` is an opt-in needing spacy-transformers +
  torch).
- **Graceful fallback:** configured model → `en_core_web_sm` → blank pipeline
  (no entities) so the task **succeeds** instead of crash-looping if a model is
  ever missing.

## Fix 2 — lang_detect `UndefinedColumnError`

**Root cause:** `_fetch_ocr_text` selected a non-existent `full_text` column from
`ocr_results`. The table is **per-page** with a `text_content` column.

**Fix:** `string_agg(text_content ORDER BY page_number)` so detection sees the
whole document, not one arbitrary page. (This closes the "Language panel column
mismatch" open item from the 2026-05-20 status snapshot.)

## Verified

Live in the worker: `extract_entities('Barack Obama visited Paris…')` returns
PERSON/GPE/DATE/ORG; `lang_detect` on a real OCR'd version returns
`{status: completed, language: en}`. Worker error log clean after recreate.
`test_ner.py` mocks `extract_entities`, so it's unaffected.

> Note: the Dockerfile copies only `app/` into the image, so pytest can't run
> inside the container — verify intelligence changes via `docker exec … python -c`.

## Files changed
`services/intelligence/Dockerfile`,
`services/intelligence/app/models/ner_model.py`,
`services/intelligence/app/tasks/lang_detect.py`.
