"""The preview NATS consumer accepts the dms.version.uploaded.v1 events the
document service actually emits.

The document outbox publishes a CloudEvents envelope (pkg/database
OutboxPublisher.publishOne) whose `data` is model.VersionUploadedPayload:
storage_uri (s3://bucket/key), content_blob_id, mime_type, ... — no
storage_bucket / storage_key / region_pin. The consumer required those
three, so it term()'d every real event and previews never started.
"""
from __future__ import annotations

import asyncio
import json
from unittest import mock

from app import nats_consumer

TENANT = "11111111-1111-1111-1111-111111111111"
DOC = "22222222-2222-2222-2222-222222222222"
VER = "33333333-3333-3333-3333-333333333333"
BLOB = "44444444-4444-4444-4444-444444444444"


def _outbox_envelope(**data_overrides) -> bytes:
    """Recorded shape: cloudEvent{...,data: VersionUploadedPayload} as
    written by services/document CreateVersion via the outbox."""
    data = {
        "event_id": "55555555-5555-5555-5555-555555555555",
        "tenant_id": TENANT,
        "document_id": DOC,
        "version_id": VER,
        "version_number": 1,
        "content_blob_id": BLOB,
        "storage_uri": "s3://dms-eu-west-1-hot/" + TENANT + "/2026/10/abc.pdf",
        "mime_type": "application/pdf",
        "size_bytes": 1024,
        "sha256": "ab" * 32,
        "uploaded_by_user_id": "66666666-6666-6666-6666-666666666666",
        "uploaded_at": "2026-10-12T10:00:00Z",
    }
    data.update(data_overrides)
    return json.dumps({
        "specversion": "1.0",
        "id": "77777777-7777-7777-7777-777777777777",
        "source": "vaultdms.document",
        "type": "dms.version.uploaded.v1",
        "subject": "version/" + VER,
        "time": "2026-10-12T10:00:00Z",
        "datacontenttype": "application/json",
        "tenantid": TENANT,
        "data": data,
    }).encode()


class _Msg:
    def __init__(self, body: bytes):
        self.data = body
        self.acked = self.termed = False
        self.naked = None

    async def ack(self):
        self.acked = True

    async def term(self):
        self.termed = True

    async def nak(self, delay=None):
        self.naked = delay


def _deliver(body: bytes):
    msg = _Msg(body)
    consumer = nats_consumer.PreviewConsumer()
    with mock.patch.object(nats_consumer, "generate_preview") as gp:
        asyncio.run(consumer._on_message(msg))
    return msg, gp


def test_document_outbox_event_enqueues_preview():
    msg, gp = _deliver(_outbox_envelope())
    assert msg.acked and not msg.termed
    gp.apply_async.assert_called_once()
    kw = gp.apply_async.call_args.kwargs["kwargs"]
    assert kw["tenant_id"] == TENANT and kw["document_id"] == DOC and kw["version_id"] == VER
    assert kw["content_blob_id"] == BLOB
    assert kw["storage_bucket"] == "dms-eu-west-1-hot"
    assert kw["storage_key"] == TENANT + "/2026/10/abc.pdf"
    assert kw["mime_type"] == "application/pdf"
    assert gp.apply_async.call_args.kwargs["queue"] == "preview"


def test_legacy_bucket_key_event_still_accepted():
    body = _outbox_envelope(storage_uri=None, storage_bucket="b", storage_key="k",
                            region_pin="eu-west-1")
    msg, gp = _deliver(body)
    assert msg.acked
    kw = gp.apply_async.call_args.kwargs["kwargs"]
    assert (kw["storage_bucket"], kw["storage_key"], kw["region_pin"]) == ("b", "k", "eu-west-1")


def test_unusable_events_are_terminated():
    for body in (b"not json",
                 _outbox_envelope(version_id=""),
                 _outbox_envelope(storage_uri="ftp://x/y", content_blob_id="")):
        msg, gp = _deliver(body)
        assert msg.termed, body[:40]
        gp.apply_async.assert_not_called()


def test_subscription_does_not_hardcode_a_stream():
    """The document service publishes on the DOC_EVENTS stream; the old
    code looked up / created its consumer on a non-existent "SEDOC" stream
    and crashed before subscribing. Subscribing by subject lets JetStream
    resolve the stream."""
    js = mock.MagicMock()
    js.subscribe = mock.AsyncMock()
    nc = mock.MagicMock()
    nc.jetstream.return_value = js

    async def fake_connect(*a, **k):
        return nc

    with mock.patch.object(nats_consumer.nats, "connect", new=fake_connect):
        asyncio.run(nats_consumer.PreviewConsumer().start())
    js.add_consumer.assert_not_called()
    js.consumer_info.assert_not_called()
    args, kwargs = js.subscribe.call_args
    assert (kwargs.get("subject") or args[0]) == "dms.version.uploaded.v1"
    assert kwargs["durable"] and kwargs["manual_ack"] is True
    assert "stream" not in kwargs
