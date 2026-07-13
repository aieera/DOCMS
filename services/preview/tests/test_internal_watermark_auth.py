"""Internal watermark endpoint auth + render (§5).

The document service calls POST /api/v1/previews/internal/watermark/pdf
with the shared X-Service-Key. These pin the auth path the deploy fix
provisions (SEDOC_SERVICE_API_KEY to both preview and its document
caller):

  - no key / wrong key            -> 401 (fail closed);
  - correct key                   -> 200 with a valid stamped PDF;
  - empty server-side key         -> 401 for everyone (fail closed).

The /watermark/pdf endpoint is self-contained (upload -> stamp ->
return), so this needs no S3/Redis — it exercises exactly the
service-auth gate + the real PyMuPDF stamp.
"""
from __future__ import annotations

import io

import fitz  # PyMuPDF
import pytest
from fastapi.testclient import TestClient

from app.config import settings


KEY = "test-service-key-123"


def _pdf_bytes() -> bytes:
    doc = fitz.open()
    page = doc.new_page(width=612, height=792)
    page.insert_text((72, 100), "Confidential quarterly figures", fontsize=12)
    out = io.BytesIO()
    doc.save(out)
    doc.close()
    return out.getvalue()


@pytest.fixture()
def client_with_key(monkeypatch):
    monkeypatch.setattr(settings, "service_api_key", KEY)
    # Import the app AFTER the key is set; build a client without lifespan
    # (no NATS/Redis needed for these endpoint tests).
    from app.api.internal import router
    from fastapi import FastAPI

    app = FastAPI()
    app.include_router(router)
    return TestClient(app)


def _post_pdf(client, key):
    headers = {"X-Service-Key": key} if key is not None else {}
    return client.post(
        "/api/v1/previews/internal/watermark/pdf",
        headers=headers,
        files={"file": ("in.pdf", _pdf_bytes(), "application/pdf")},
        data={"text": "Alice — 2026-07-12"},
    )


def test_missing_key_rejected(client_with_key):
    r = _post_pdf(client_with_key, None)
    assert r.status_code == 401


def test_wrong_key_rejected(client_with_key):
    r = _post_pdf(client_with_key, "not-the-key")
    assert r.status_code == 401


def test_correct_key_returns_stamped_pdf(client_with_key):
    r = _post_pdf(client_with_key, KEY)
    assert r.status_code == 200, r.text
    assert r.headers["content-type"] == "application/pdf"
    assert r.headers.get("cache-control") == "no-store"
    # The response is a real, non-empty PDF (the stamp ran).
    out = r.content
    assert out[:5] == b"%PDF-"
    doc = fitz.open(stream=out, filetype="pdf")
    assert doc.page_count == 1
    # Original content survives the overlay stamp.
    assert "Confidential" in doc[0].get_text("text")
    doc.close()


def test_empty_server_key_fails_closed(monkeypatch):
    # An unset server-side key must reject every request, even one that
    # supplies an empty key.
    monkeypatch.setattr(settings, "service_api_key", "")
    from app.api.internal import router
    from fastapi import FastAPI

    app = FastAPI()
    app.include_router(router)
    client = TestClient(app)
    assert _post_pdf(client, "").status_code == 401
    assert _post_pdf(client, "anything").status_code == 401
