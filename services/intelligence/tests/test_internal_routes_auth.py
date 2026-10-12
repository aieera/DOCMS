"""C-8 / DEC-4 — the service-to-service /internal/v1 routes require the
internal service key (X-Internal-Service-Key == SEDOC_INTERNAL_API_KEY,
constant time) and fail closed when the key is not configured."""
from __future__ import annotations

from unittest import mock

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from app.config import settings

KEY = "internal-key-789"
ROUTES = [
    ("/internal/v1/embed-query", {"query": "hello"}),
    ("/internal/v1/capture/analyze", {"bundle_b64": "!!!not-base64"}),
    ("/internal/v1/capture/split", {"bundle_b64": "!!!not-base64", "page_groups": [[0]]}),
]


@pytest.fixture()
def client(monkeypatch):
    monkeypatch.setattr(settings, "internal_api_key", KEY)
    from app.api.routes import internal_router
    app = FastAPI()
    app.include_router(internal_router)
    return TestClient(app)


def test_internal_routes_require_internal_key(client):
    with mock.patch("app.models.embedder.embed_single", return_value=[0.1, 0.2]):
        for path, body in ROUTES:
            assert client.post(path, json=body).status_code == 401, path
            r = client.post(path, json=body, headers={"X-Internal-Service-Key": "wrong"})
            assert r.status_code == 401, path
            # Identity/gateway headers never substitute for the key.
            r = client.post(path, json=body, headers={"X-Gateway-Signature": "x",
                                                      "X-Auth-Tenant-ID": "t"})
            assert r.status_code == 401, path
            r = client.post(path, json=body, headers={"X-Internal-Service-Key": KEY})
            assert r.status_code != 401, (path, r.status_code)


def test_internal_routes_fail_closed_without_configured_key(client, monkeypatch):
    monkeypatch.setattr(settings, "internal_api_key", "")
    for path, body in ROUTES:
        r = client.post(path, json=body, headers={"X-Internal-Service-Key": ""})
        assert r.status_code == 503, path
