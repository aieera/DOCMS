"""H-17 / DEC-4 — the preview REST API is internal-only.

Before: /api/v1/previews/* took the tenant from ?tenant_id=, was routed
by Kong with no auth plugin and checked nothing, so an anonymous caller
got presigned URLs for another tenant's unwatermarked page renders, and
an anonymous POST /regenerate overwrote any ready manifest with
`failed` (it read source_bucket/source_key that were never written).

Now every route needs the internal service key, generate_preview
persists the source location, and /regenerate refuses (without
enqueuing) when the manifest has no source location.
"""
from __future__ import annotations

import json
from unittest import mock

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from app.config import settings

INTERNAL = "internal-key-xyz"
SERVICE = "service-key-abc"
T, D, V = "tenant-1", "doc-1", "ver-1"


class _FakeRedis:
    def __init__(self, store):
        self.store = store

    def get(self, k):
        return self.store.get(k)

    def close(self):
        pass


def _ready_manifest(**extra):
    m = {
        "tenant_id": T, "document_id": D, "version_id": V,
        "content_blob_id": "blob-1", "mime_type": "application/pdf",
        "region": "us-east-1", "status": "ready", "bucket": "dms-us-east-1-previews",
        "thumbnail_key": f"{T}/{D}/{V}/thumb.jpg",
        "preview_page_keys": [f"{T}/{D}/{V}/page_1.png"],
    }
    m.update(extra)
    return m


@pytest.fixture()
def client(monkeypatch):
    monkeypatch.setattr(settings, "internal_api_key", INTERNAL)
    monkeypatch.setattr(settings, "service_api_key", SERVICE)
    from app.api import routes
    store = {f"preview:{T}:{V}": json.dumps(_ready_manifest()).encode()}
    monkeypatch.setattr(routes, "_redis", lambda: _FakeRedis(store))
    monkeypatch.setattr(routes.s3, "presigned_get", lambda b, k, ttl: f"http://s3/{b}/{k}")
    app = FastAPI()
    app.include_router(routes.router)
    c = TestClient(app, follow_redirects=False)
    c.store = store
    return c


def test_preview_public_route_requires_internal_key(client):
    paths = [
        ("get", f"/api/v1/previews/{D}/thumbnail?tenant_id={T}&version_id={V}"),
        ("get", f"/api/v1/previews/{D}/pages/1?tenant_id={T}&version_id={V}"),
        ("get", f"/api/v1/previews/{D}/status?tenant_id={T}&version_id={V}"),
        ("post", f"/api/v1/previews/{D}/regenerate?tenant_id={T}&version_id={V}"),
    ]
    with mock.patch("app.api.routes.generate_preview") as gp:
        for method, url in paths:
            # Anonymous, forged identity headers, wrong key, empty key.
            for hdrs in ({}, {"X-Tenant-ID": T, "X-User-Role": "owner"},
                         {"X-Internal-Service-Key": "wrong"},
                         {"X-Internal-Service-Key": ""},
                         {"X-Gateway-Signature": "anything"}):
                r = getattr(client, method)(url, headers=hdrs)
                assert r.status_code == 401, (method, url, hdrs, r.status_code)
        gp.apply_async.assert_not_called()

    # The internal service key (or the document service's existing
    # preview X-Service-Key) unlocks the routes.
    r = client.get(f"/api/v1/previews/{D}/thumbnail?tenant_id={T}&version_id={V}",
                   headers={"X-Internal-Service-Key": INTERNAL})
    assert r.status_code == 302
    r = client.get(f"/api/v1/previews/{D}/status?tenant_id={T}&version_id={V}",
                   headers={"X-Service-Key": SERVICE})
    assert r.status_code == 200 and r.json()["status"] == "ready"


def test_internal_key_unset_rejects_everything(client, monkeypatch):
    monkeypatch.setattr(settings, "internal_api_key", "")
    monkeypatch.setattr(settings, "service_api_key", "")
    r = client.get(f"/api/v1/previews/{D}/status?tenant_id={T}&version_id={V}",
                   headers={"X-Internal-Service-Key": "", "X-Service-Key": ""})
    assert r.status_code == 401


def test_regenerate_keeps_ready_manifest(client):
    # A ready manifest written before source_bucket/source_key were
    # persisted: regenerate must NOT enqueue a job that would overwrite
    # it with `failed`.
    with mock.patch("app.api.routes.generate_preview") as gp:
        r = client.post(f"/api/v1/previews/{D}/regenerate?tenant_id={T}&version_id={V}",
                        headers={"X-Internal-Service-Key": INTERNAL})
        assert r.status_code == 409
        gp.apply_async.assert_not_called()

    # With the persisted source location, regenerate uses exactly it.
    client.store[f"preview:{T}:{V}"] = json.dumps(_ready_manifest(
        source_bucket="dms-us-east-1-hot", source_key="t/k/src.pdf")).encode()
    with mock.patch("app.api.routes.generate_preview") as gp:
        r = client.post(f"/api/v1/previews/{D}/regenerate?tenant_id={T}&version_id={V}",
                        headers={"X-Internal-Service-Key": INTERNAL})
        assert r.status_code == 200
        kw = gp.apply_async.call_args.kwargs["kwargs"]
        assert kw["storage_bucket"] == "dms-us-east-1-hot"
        assert kw["storage_key"] == "t/k/src.pdf"


def test_generate_preview_persists_source_location(monkeypatch):
    """generate_preview writes source_bucket/source_key into every manifest
    it caches, so a later regenerate can find the source."""
    from app.tasks import preview as task_mod

    cached = []
    monkeypatch.setattr(task_mod, "_cache_manifest",
                        lambda url, t, v, m: cached.append(dict(m)))
    monkeypatch.setattr(task_mod.s3, "head", lambda b, k: {"ContentLength": 10})
    monkeypatch.setattr(task_mod.s3, "download_to", lambda b, k, p: open(p, "wb").write(b"x"))
    monkeypatch.setattr(task_mod, "_select_processor", lambda m: (None, {}))
    task_mod.generate_preview.run(
        tenant_id=T, document_id=D, version_id=V, content_blob_id="",
        mime_type="application/x-unknown", region_pin="us-east-1",
        storage_bucket="dms-us-east-1-hot", storage_key="t/k/src.bin",
    )
    assert cached and cached[-1]["source_bucket"] == "dms-us-east-1-hot"
    assert cached[-1]["source_key"] == "t/k/src.bin"
