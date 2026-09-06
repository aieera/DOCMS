"""Routing policy for detected document languages (QA SD-18).

langdetect happily calls an English PDF Estonian at 50% — a coin flip.
The detection (language + confidence) is stored and displayed as
"uncertain", but anything that ROUTES on language — translation source,
NER model choice — must treat a below-floor detection as "unknown" and
fall back to its default. Same floor the viewer uses to hide the badge.
Dependency-free on purpose (unit-testable without the worker stack).
"""
from __future__ import annotations

ROUTING_MIN_CONFIDENCE = 0.75


def routable_language(detected: str | None, confidence: float | None) -> str | None:
    """The language routing may act on, or None to use the default."""
    if not detected or confidence is None:
        return None
    if confidence < ROUTING_MIN_CONFIDENCE:
        return None
    return detected
