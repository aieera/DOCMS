"""M-24 — decrypt an envelope produced by the Go storage code itself.

tests/fixtures/go_envelope.json was generated once with
pkg/crypto.LocalKeyManager.GenerateDataKey + crypto.EncryptData (fixed
master key, region-scoped kek_id), so this pins byte-compatibility with
what services/storage writes, not just with a Python re-implementation.
"""
from __future__ import annotations

import base64
import json
from pathlib import Path

import pytest

from app import envelope

FIXTURE = json.loads((Path(__file__).parent / "fixtures" / "go_envelope.json").read_text())


def _b(k):
    return base64.b64decode(FIXTURE[k])


def test_decrypts_go_produced_envelope(monkeypatch):
    monkeypatch.setenv("SEDOC_LOCAL_KEK", FIXTURE["master_key"])
    pt = envelope.decrypt_blob(_b("ciphertext"), _b("dek_nonce"), _b("encrypted_dek"), FIXTURE["kek_id"])
    assert pt == _b("plaintext")
    # The kek_id is part of the derivation: another tenant's id cannot unwrap.
    with pytest.raises(envelope.EnvelopeError):
        envelope.decrypt_blob(_b("ciphertext"), _b("dek_nonce"), _b("encrypted_dek"),
                              FIXTURE["kek_id"].replace("5f0c", "0000"))
