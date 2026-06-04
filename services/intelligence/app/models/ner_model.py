"""SpaCy NER model — loaded once per worker process.

Model is configurable via SEDOC_NER_SPACY_MODEL (default en_core_web_sm,
which the Dockerfile installs). The transformer model en_core_web_trf is a
better-but-heavy opt-in (needs spacy-transformers + torch); set the env var
to use it once installed. Loading degrades gracefully — a missing model
falls back to en_core_web_sm and finally to a blank English pipeline (no
entities) rather than crash-looping the NER task, which is what happened
when the hard-coded en_core_web_trf was never installed (its model-index
404s on spacy 3.7.4, so the Dockerfile download was disabled).
"""
from __future__ import annotations
import logging
import os

log = logging.getLogger(__name__)
_nlp = None

_DEFAULT_MODEL = "en_core_web_sm"


def _load():
    global _nlp
    if _nlp is not None:
        return _nlp
    import spacy

    configured = os.getenv("SEDOC_NER_SPACY_MODEL", _DEFAULT_MODEL)
    for model in (configured, _DEFAULT_MODEL):
        try:
            log.info("loading spacy model %s", model)
            _nlp = spacy.load(model)
            return _nlp
        except OSError:
            log.warning("spacy model %s not installed; trying fallback", model)
    # Last resort: a blank pipeline has no NER component, so extract_entities
    # returns []. NER finds nothing, but the task SUCCEEDS instead of
    # retry-looping forever — far better than taking the pipeline down.
    log.error("no spacy NER model available; NER will return no entities")
    _nlp = spacy.blank("en")
    return _nlp

def extract_entities(text: str, max_chars: int = 100_000) -> list[dict]:
    nlp = _load()
    doc = nlp(text[:max_chars])
    results = []
    for ent in doc.ents:
        results.append({
            "entity_type": ent.label_,
            "entity_value": ent.text,
            "start_offset": ent.start_char,
            "end_offset": ent.end_char,
            "confidence": 0.9,
            "is_pii": ent.label_ in ("PERSON", "GPE", "DATE", "CARDINAL"),
        })
    return results
