"""M-20 — document-scoped Q&A and /translate check the caller can read
the document.

Chunks are indexed readable_by ["everyone"], so the readable_by filter
alone let any user in the tenant read any document's content by asking
about its UUID (document-scope Q&A) or translating it. Both now gate on
the same allowed-doc-ids query workspace/global Q&A already use.
"""
from __future__ import annotations

from unittest import mock

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from app.identity import Identity, require_identity

TENANT = "11111111-1111-1111-1111-111111111111"
USER = "22222222-2222-2222-2222-222222222222"
DOC = "33333333-3333-3333-3333-333333333333"
VER = "44444444-4444-4444-4444-444444444444"


@pytest.fixture()
def client():
    from app.api.routes import router
    app = FastAPI()
    app.include_router(router)
    app.dependency_overrides[require_identity] = lambda: Identity(
        tenant_id=TENANT, user_id=USER, role="member", groups=("g1",))
    return TestClient(app)


def _allowed(ids):
    calls = []

    async def fake(**kw):
        calls.append(kw)
        if kw.get("document_id") and kw["document_id"] not in ids:
            return []
        return list(ids)

    fake.calls = calls
    return fake


def _no_db():
    async def boom():
        raise AssertionError("must not touch the DB for an unreadable document")
    return boom


def test_qa_document_scope_unreadable_denied(client):
    allowed = _allowed([])  # the user can read nothing
    with mock.patch("app.rag_persist.list_allowed_doc_ids", new=allowed), \
         mock.patch("app.api.routes.ensure_conversation") as conv, \
         mock.patch("app.api.routes.stream_ask") as sa:
        for path in ("/api/v1/intelligence/qa", "/api/v1/intelligence/qa/sync"):
            r = client.post(path, json={"question": "what is the salary?",
                                        "scope": "document", "document_id": DOC})
            assert r.status_code == 404, (path, r.status_code)
    conv.assert_not_called()
    sa.assert_not_called()
    assert allowed.calls and allowed.calls[0]["user_id"] == USER
    assert allowed.calls[0]["document_id"] == DOC


def test_qa_document_scope_readable_allowed(client):
    async def _conv(**_kw):
        return "conv-1"

    async def _msgs(**_kw):
        return []

    def _stream(**_kw):
        yield "done", {"full_text": "ok", "citations": []}

    with mock.patch("app.rag_persist.list_allowed_doc_ids", new=_allowed([DOC])), \
         mock.patch("app.api.routes.ensure_conversation", new=_conv), \
         mock.patch("app.api.routes.list_messages", new=_msgs), \
         mock.patch("app.api.routes.append_message", new=_msgs), \
         mock.patch("app.api.routes.stream_ask", new=_stream):
        r = client.post("/api/v1/intelligence/qa/sync",
                        json={"question": "q", "scope": "document", "document_id": DOC})
    assert r.status_code == 200, r.text
    assert r.json()["answer"] == "ok"


def test_translate_unreadable_denied(client):
    allowed = _allowed([])
    with mock.patch("app.rag_persist.list_allowed_doc_ids", new=allowed), \
         mock.patch("app.db.pool.get_pool", new=_no_db()), \
         mock.patch("app.api.routes.translate_task") as task:
        r = client.post("/api/v1/intelligence/translate", json={
            "document_id": DOC, "version_id": VER, "target_language": "ar"})
    assert r.status_code == 404
    task.apply_async.assert_not_called()
    # The version is checked against the document too.
    assert allowed.calls[0]["version_id"] == VER


def test_translations_listing_unreadable_denied(client):
    with mock.patch("app.rag_persist.list_allowed_doc_ids", new=_allowed([])), \
         mock.patch("app.db.pool.get_pool", new=_no_db()):
        r = client.get(f"/api/v1/intelligence/translations/{DOC}")
    assert r.status_code == 404


def test_ask_is_permission_filtered(client):
    captured = {}

    def fake_ask(**kw):
        captured.update(kw)
        return {"answer": "x", "sources": [], "citations": []}

    with mock.patch("app.rag_persist.list_allowed_doc_ids", new=_allowed([DOC])), \
         mock.patch("app.api.routes.ask", side_effect=fake_ask):
        r = client.post("/api/v1/intelligence/ask", json={"question": "q"})
    assert r.status_code == 200
    assert captured["allowed_doc_ids"] == [DOC]


def test_document_readable_rejects_non_uuid():
    import asyncio

    from app import rag_persist

    with mock.patch("app.rag_persist.get_pool", new=_no_db()):
        assert asyncio.run(rag_persist.document_readable(
            tenant_id=TENANT, user_id=USER, document_id="not-a-uuid")) is False
