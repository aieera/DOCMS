"""Sentence-transformer embedding model — loaded once per worker process."""
from __future__ import annotations
import logging
import numpy as np
from app.config import settings

log = logging.getLogger(__name__)

_model = None

def _load():
    global _model
    if _model is None:
        from sentence_transformers import SentenceTransformer
        log.info("loading embedding model %s", settings.embedding_model)
        _model = SentenceTransformer(settings.embedding_model)
    return _model

def embed(texts: list[str], batch_size: int = 32) -> np.ndarray:
    model = _load()
    return model.encode(texts, batch_size=batch_size, show_progress_bar=False, normalize_embeddings=True)

def embed_single(text: str) -> list[float]:
    return embed([text])[0].tolist()
