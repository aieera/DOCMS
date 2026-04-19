"""Pure error-type classifier shared by the hardened intel consumers.

Kept in its own module so unit tests can import it without pulling the
DB or NATS stack.
"""
from __future__ import annotations


def classify_error_reason(exc: Exception) -> str:
    """Map a raised exception to a stable DLQ label.

    Labels drive Grafana panels + alerts — resist renaming. Order of
    checks matters: specific before generic.
    """
    name = type(exc).__name__
    msg = str(exc).lower()
    if "timeout" in msg or "timeout" in name.lower():
        return "timeout"
    if "llm" in msg or "completion" in msg or "api" in msg:
        return "llm_error"
    if "postgres" in msg or "pgx" in msg or "relation" in msg or "insert" in msg:
        return "persist_error"
    if "spacy" in msg or "ner" in msg or "model" in msg:
        return "model_error"
    return "unknown"


DLQ_SUBJECT_PREFIX = "dms.dlq.intel_events"
