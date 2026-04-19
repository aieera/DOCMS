"""Cross-encoder re-ranker for RAG pipeline."""
from __future__ import annotations
import logging
from app.config import settings

log = logging.getLogger(__name__)
_model = None

def _load():
    global _model
    if _model is None:
        from sentence_transformers import CrossEncoder
        log.info("loading reranker %s", settings.reranker_model)
        _model = CrossEncoder(settings.reranker_model)
    return _model

def rerank(question: str, passages: list[str]) -> list[float]:
    model = _load()
    pairs = [(question, p) for p in passages]
    return model.predict(pairs).tolist()
