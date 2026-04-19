"""SpaCy NER model — loaded once per worker process."""
from __future__ import annotations
import logging

log = logging.getLogger(__name__)
_nlp = None

def _load():
    global _nlp
    if _nlp is None:
        import spacy
        log.info("loading spacy en_core_web_trf")
        _nlp = spacy.load("en_core_web_trf")
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
