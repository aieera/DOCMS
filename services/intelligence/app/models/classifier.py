"""DistilBERT document classifier — loaded once per worker."""
from __future__ import annotations
import logging
import torch
from app.config import settings

log = logging.getLogger(__name__)
_pipeline = None

def _load():
    global _pipeline
    if _pipeline is None:
        from transformers import pipeline
        device = 0 if torch.cuda.is_available() else -1
        log.info("loading classifier model %s (device=%d)", settings.classifier_model, device)
        _pipeline = pipeline("text-classification", model=settings.classifier_model, device=device)
    return _pipeline

def classify(text: str) -> tuple[str, float]:
    pipe = _load()
    result = pipe(text[:512])
    if result:
        top = result[0]
        return top["label"], top["score"]
    return "other", 0.0
