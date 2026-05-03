"""Anomaly detection — pure scorers + orchestration. Qdrant + DB stubbed."""
from __future__ import annotations

from datetime import datetime, timedelta, timezone
from unittest import mock

import pytest

from app.tasks.anomaly_detect import (
    DEFAULT_CONFIG,
    RAPID_UPLOAD_WINDOW_SEC,
    _behavioral_findings,
    _cosine_distance,
    _mean_vector,
    _metadata_findings,
    _run_async,
    _severity_from_z,
)


# ---- math helpers --------------------------------------------------------

def test_mean_vector():
    assert _mean_vector([[1.0, 0.0], [0.0, 1.0]]) == [0.5, 0.5]


def test_cosine_distance_identical_is_zero():
    assert _cosine_distance([1.0, 0.0], [1.0, 0.0]) == pytest.approx(0.0)


def test_cosine_distance_orthogonal_is_one():
    assert _cosine_distance([1.0, 0.0], [0.0, 1.0]) == pytest.approx(1.0)


def test_cosine_distance_opposite_is_two():
    assert _cosine_distance([1.0, 0.0], [-1.0, 0.0]) == pytest.approx(2.0)


def test_severity_thresholds():
    assert _severity_from_z(2.5) == "low"
    assert _severity_from_z(3.5) == "medium"
    assert _severity_from_z(5.0) == "high"


# ---- metadata strategy --------------------------------------------------

def _doc(id_, **kwargs):
    """Build a minimal doc dict; defaults make the doc 'normal'."""
    base = {
        "id": id_,
        "workspace_id": "w1",
        "created_by": "u1",
        "created_at": datetime(2026, 5, 1, 12, 0, tzinfo=timezone.utc),
        "size_bytes": 1_000_000,
        "document_class": "invoice",
        "word_count": 500,
    }
    base.update(kwargs)
    return base


def test_metadata_flags_size_outlier():
    docs = [_doc(f"d{i}", size_bytes=1_000_000) for i in range(20)]
    docs.append(_doc("huge", size_bytes=500_000_000))
    findings = _metadata_findings(docs, DEFAULT_CONFIG)
    flagged_ids = {f["document_id"] for f in findings if f["anomaly_type"] == "size_outlier"}
    assert "huge" in flagged_ids
    huge = next(f for f in findings if f["document_id"] == "huge")
    assert huge["severity"] in ("medium", "high")
    assert huge["evidence"]["metric"] == "size_bytes"
    assert huge["z_score"] > DEFAULT_CONFIG["z_score_threshold"]


def test_metadata_flags_rare_classification():
    """A class appearing in <10% AND ≤2 documents → misclassified."""
    docs = [_doc(f"d{i}", document_class="invoice") for i in range(30)]
    docs.append(_doc("odd", document_class="medical_record"))
    findings = _metadata_findings(docs, DEFAULT_CONFIG)
    misc = [f for f in findings if f["anomaly_type"] == "misclassified"]
    assert any(f["document_id"] == "odd" for f in misc)


def test_metadata_does_not_flag_uniform_workspace():
    docs = [_doc(f"d{i}") for i in range(30)]
    findings = _metadata_findings(docs, DEFAULT_CONFIG)
    # Same class everywhere → not rare. All sizes uniform → no z-score.
    assert findings == []


# ---- behavioral strategy ------------------------------------------------

def test_behavioral_flags_unusual_hour():
    docs = [_doc("d1", created_at=datetime(2026, 5, 1, 3, 30, tzinfo=timezone.utc))]
    findings = _behavioral_findings(docs, DEFAULT_CONFIG)
    assert any(f["anomaly_type"] == "unusual_upload_time" for f in findings)


def test_behavioral_does_not_flag_business_hours():
    docs = [_doc("d1", created_at=datetime(2026, 5, 1, 14, 0, tzinfo=timezone.utc))]
    findings = _behavioral_findings(docs, DEFAULT_CONFIG)
    assert all(f["anomaly_type"] != "unusual_upload_time" for f in findings)


def test_behavioral_flags_rapid_uploads():
    """51 docs by one user inside one hour → all flagged with rapid_uploads."""
    base = datetime(2026, 5, 1, 14, 0, tzinfo=timezone.utc)
    docs = [
        _doc(f"d{i}", created_by="spammer",
             created_at=base + timedelta(seconds=i * 30))   # 30s apart, 51 docs in ~25 min
        for i in range(51)
    ]
    findings = _behavioral_findings(docs, DEFAULT_CONFIG)
    rapid = [f for f in findings if f["anomaly_type"] == "rapid_uploads"]
    assert len(rapid) >= 51
    # Window count carried in evidence.
    assert any(f["evidence"]["window_count"] > 50 for f in rapid)


def test_behavioral_does_not_flag_spread_uploads():
    """50 docs by one user spread over 24h → no rapid_uploads finding."""
    base = datetime(2026, 5, 1, 14, 0, tzinfo=timezone.utc)
    docs = [
        _doc(f"d{i}", created_by="legit",
             created_at=base + timedelta(minutes=i * 30))   # 30 min apart
        for i in range(50)
    ]
    findings = _behavioral_findings(docs, DEFAULT_CONFIG)
    assert all(f["anomaly_type"] != "rapid_uploads" for f in findings)


# ---- orchestration ------------------------------------------------------

@pytest.mark.asyncio
async def test_disabled_marks_failed():
    cfg = {**DEFAULT_CONFIG, "enabled": False}
    failures: list[tuple] = []

    async def fake_failed(t, r, msg):
        failures.append((t, r, msg))

    with mock.patch("app.tasks.anomaly_detect.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.anomaly_detect.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.anomaly_detect.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.anomaly_detect._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.anomaly_detect._mark_report_failed", new=fake_failed):
        out = await _run_async(
            tenant_id="t1", report_id="r1", workspace_id="w1",
            analysis_type="combined", event_id="e1",
            correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "disabled"
    assert failures and failures[0][1] == "r1"


@pytest.mark.asyncio
async def test_insufficient_documents_completes_without_findings():
    """Workspace with fewer than min_documents_for_analysis docs ends
    cleanly without a failure — schema lets it; the summary explains."""
    cfg = {**DEFAULT_CONFIG, "min_documents_for_analysis": 20}
    persisted: dict = {}

    async def fake_persist(*, total_docs, findings, summary, **_):
        persisted["total_docs"] = total_docs
        persisted["findings"] = findings
        persisted["summary"] = summary

    docs = [_doc(f"d{i}") for i in range(5)]
    with mock.patch("app.tasks.anomaly_detect.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.anomaly_detect.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.anomaly_detect.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.anomaly_detect._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.anomaly_detect._set_report_status", new=mock.AsyncMock()), \
         mock.patch("app.tasks.anomaly_detect._fetch_workspace_docs",
                    new=mock.AsyncMock(return_value=docs)), \
         mock.patch("app.tasks.anomaly_detect._persist_completion", new=fake_persist):
        out = await _run_async(
            tenant_id="t1", report_id="r1", workspace_id="w1",
            analysis_type="combined", event_id="e1",
            correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "completed"
    assert out["anomalies_found"] == 0
    assert out["reason"] == "insufficient_documents"
    assert persisted["summary"]["reason"] == "insufficient_documents"
    assert persisted["findings"] == []


@pytest.mark.asyncio
async def test_combined_run_aggregates_three_strategies():
    """Combined analysis runs all three strategies and counts each."""
    cfg = {**DEFAULT_CONFIG, "min_documents_for_analysis": 10,
           "analyze_content": False}  # skip content (Qdrant) for this test
    persisted: dict = {}

    async def fake_persist(*, total_docs, findings, summary, **_):
        persisted["findings"] = findings
        persisted["summary"] = summary

    # Build a workspace with one size outlier and one rapid-upload spammer.
    base = datetime(2026, 5, 1, 14, 0, tzinfo=timezone.utc)
    docs = [_doc(f"normal{i}", created_at=base + timedelta(hours=i)) for i in range(20)]
    docs.append(_doc("HUGE", size_bytes=999_999_999, created_at=base))
    # Add 51 rapid uploads by another user.
    for i in range(51):
        docs.append(_doc(f"spam{i}", created_by="spammer",
                         created_at=base + timedelta(seconds=i * 30)))

    with mock.patch("app.tasks.anomaly_detect.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.anomaly_detect.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.anomaly_detect.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.anomaly_detect._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.anomaly_detect._set_report_status", new=mock.AsyncMock()), \
         mock.patch("app.tasks.anomaly_detect._fetch_workspace_docs",
                    new=mock.AsyncMock(return_value=docs)), \
         mock.patch("app.tasks.anomaly_detect._persist_completion", new=fake_persist):
        out = await _run_async(
            tenant_id="t1", report_id="r1", workspace_id="w1",
            analysis_type="combined", event_id="e1",
            correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "completed"
    types = {f["anomaly_type"] for f in persisted["findings"]}
    assert "size_outlier" in types
    assert "rapid_uploads" in types
    # per_strategy counts present on summary
    per = persisted["summary"]["per_strategy"]
    assert per["metadata"] >= 1
    assert per["behavioral"] >= 1


@pytest.mark.asyncio
async def test_duplicate_event_short_circuits():
    with mock.patch("app.tasks.anomaly_detect.already_completed",
                    new=mock.AsyncMock(return_value=True)), \
         mock.patch("app.tasks.anomaly_detect.mark_enqueued",
                    new=mock.AsyncMock()) as me:
        out = await _run_async(
            tenant_id="t1", report_id="r1", workspace_id="w1",
            analysis_type="combined", event_id="e1",
            correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "duplicate"
    me.assert_not_awaited()
