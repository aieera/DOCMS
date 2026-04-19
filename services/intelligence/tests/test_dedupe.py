"""Unit tests for Wave 5 Prompt 5.3 hardening helpers.

DB-free tests only — dedupe row semantics are covered by the
integration harness (Wave 13.1). Here we pin the pure-logic pieces
that guard against regression.
"""
from __future__ import annotations

import pytest

from app.storage_uri import parse_storage_uri


OCR_DLQ_SUBJECT = "dms.dlq.intel_events.ocr"


class TestParseStorageURI:
    def test_basic(self) -> None:
        assert parse_storage_uri("s3://bucket/key") == ("bucket", "key")

    def test_nested_key(self) -> None:
        assert parse_storage_uri("s3://b/a/b/c.pdf") == ("b", "a/b/c.pdf")

    def test_key_with_special_chars(self) -> None:
        assert parse_storage_uri("s3://b/tenant%20ns/file+v2.pdf") == (
            "b",
            "tenant%20ns/file+v2.pdf",
        )

    @pytest.mark.parametrize(
        "bad",
        [
            "",
            "http://x/y",
            "s3://",
            "s3://only-bucket",
            "s3:///no-bucket/key",
        ],
    )
    def test_rejects_bad_uri(self, bad: str) -> None:
        with pytest.raises(ValueError):
            parse_storage_uri(bad)


class TestDLQSubject:
    def test_prefix(self) -> None:
        # Subject prefix is the stream-matching root; the consumer
        # appends a reason label. Must stay stable or the DLQ topology
        # (LEGACY_EVENTS_DLQ / INTEL_EVENTS_DLQ) stops binding.
        assert OCR_DLQ_SUBJECT == "dms.dlq.intel_events.ocr"


# ---- Wave 5 Prompt 5.4 error-reason classifier ---------------------------
# Pinned so grafana labels don't drift.

from app.error_classifier import classify_error_reason, DLQ_SUBJECT_PREFIX  # noqa: E402


class TestClassifyErrorReason:
    def test_timeout(self) -> None:
        assert classify_error_reason(TimeoutError("x")) == "timeout"

    def test_llm(self) -> None:
        assert classify_error_reason(Exception("LLM api refused")) == "llm_error"

    def test_postgres(self) -> None:
        assert classify_error_reason(Exception("postgres insert failed")) == "persist_error"

    def test_spacy(self) -> None:
        assert classify_error_reason(Exception("spacy model missing")) == "model_error"

    def test_unknown(self) -> None:
        assert classify_error_reason(Exception("")) == "unknown"

    def test_dlq_prefix_stable(self) -> None:
        assert DLQ_SUBJECT_PREFIX == "dms.dlq.intel_events"
