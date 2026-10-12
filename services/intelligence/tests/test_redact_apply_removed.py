"""M-18 — the orphaned /redact/apply endpoint is gone.

It took storage_bucket/storage_key from the request body and the worker
read and wrote that S3 location with the service's global credentials:
any caller could point the redactor at another tenant's object. It had
no legitimate caller (the legal-hold and candidate-review flows resolve
the source blob server-side), so the route and its task are removed.
"""
from __future__ import annotations

from fastapi import FastAPI
from fastapi.testclient import TestClient


def test_redact_apply_route_absent():
    from app.api.routes import router

    paths = {getattr(r, "path", "") for r in router.routes}
    assert "/api/v1/intelligence/redact/apply" not in paths

    app = FastAPI()
    app.include_router(router)
    resp = TestClient(app).post(
        "/api/v1/intelligence/redact/apply",
        json={"document_id": "d", "version_id": "v", "storage_bucket": "b",
              "storage_key": "k", "entities": []},
    )
    assert resp.status_code in (404, 405)


def test_body_addressed_redaction_task_removed():
    from app.tasks import redact

    assert not hasattr(redact, "apply_redactions")
