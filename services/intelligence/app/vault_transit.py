"""ADR 0064 — Vault Transit client for envelope-encrypted tenant secrets.

When VAULT_ADDR + VAULT_TOKEN + VAULT_TRANSIT_KEY are configured the
key NEVER leaves Vault — every encrypt/decrypt round-trips to
`{VAULT_ADDR}/v1/transit/{op}/{key}`. The KEK material itself is
stored inside Vault's keyring; the deploy holds only a token that
authenticates to Vault.

Falls back gracefully (returns None) on every failure mode so the
caller can route to the local AES-GCM path when Vault is
unreachable, mis-scoped, or simply not configured.

Wire format: Vault returns ciphertext as `vault:v1:...` natively;
we store that string verbatim in the DB column. Decrypt detects
the prefix and routes here; absence of the prefix means the row
was written with the local KEK and decrypts via secrets.py's
in-process AES-GCM. Mixed-mode coexistence is a deliberate part
of the migration story documented in
docs/runbooks/llm-provider-ops.md.
"""
from __future__ import annotations

import base64
import logging
import os
from typing import Optional

import httpx

log = logging.getLogger(__name__)

# Tight timeout — Vault is fast (single-digit ms locally) and we'd
# rather degrade to "no plaintext available" than hang the LLM hot
# path on a slow Vault probe.
_VAULT_TIMEOUT_SECONDS = 3.0

# Marker that distinguishes a Vault-encrypted blob from a local
# AES-GCM blob. Vault's own ciphertext format already begins with
# this prefix, so the round-trip is a no-op.
VAULT_PREFIX = "vault:"


def is_enabled() -> bool:
    """True iff every required env var is set. Callers branch on this
    so a partial configuration falls through to the local path
    rather than half-encrypting in unpredictable ways."""
    return bool(
        os.environ.get("VAULT_ADDR", "").strip()
        and os.environ.get("VAULT_TOKEN", "").strip()
        and os.environ.get("VAULT_TRANSIT_KEY", "").strip()
    )


def _client() -> Optional[httpx.Client]:
    addr = os.environ.get("VAULT_ADDR", "").strip()
    token = os.environ.get("VAULT_TOKEN", "").strip()
    if not addr or not token:
        return None
    return httpx.Client(
        base_url=addr.rstrip("/"),
        timeout=_VAULT_TIMEOUT_SECONDS,
        headers={"X-Vault-Token": token},
    )


def encrypt(plaintext: str) -> Optional[str]:
    """Encrypt via Vault Transit. Returns the `vault:v1:...` ciphertext
    string on success, None on any failure — callers MUST treat None
    as "refuse to write" so we never silently roll back to plaintext.
    """
    if not is_enabled() or not plaintext:
        return None
    key = os.environ["VAULT_TRANSIT_KEY"].strip()
    payload = {"plaintext": base64.b64encode(plaintext.encode("utf-8")).decode("ascii")}
    try:
        with _client() as c:
            if c is None:
                return None
            resp = c.post(f"/v1/transit/encrypt/{key}", json=payload)
            resp.raise_for_status()
            ct = resp.json().get("data", {}).get("ciphertext")
            if not ct or not ct.startswith("vault:"):
                log.warning("vault encrypt: unexpected response shape")
                return None
            return ct
    except httpx.HTTPStatusError as e:
        log.warning("vault encrypt http %s: %s", e.response.status_code, e.response.text[:200])
        return None
    except (httpx.RequestError, KeyError, ValueError) as e:
        log.warning("vault encrypt failed: %s", e)
        return None


def decrypt(ciphertext: str) -> Optional[str]:
    """Decrypt a `vault:v1:...` ciphertext via Vault Transit. Returns
    None on any failure — same contract as the local decrypt helper,
    so the LLM call site can keep going without plaintext rather
    than crashing on a Vault hiccup."""
    if not is_enabled() or not ciphertext:
        return None
    if not ciphertext.startswith(VAULT_PREFIX):
        log.warning("vault decrypt: missing 'vault:' prefix")
        return None
    key = os.environ["VAULT_TRANSIT_KEY"].strip()
    payload = {"ciphertext": ciphertext}
    try:
        with _client() as c:
            if c is None:
                return None
            resp = c.post(f"/v1/transit/decrypt/{key}", json=payload)
            resp.raise_for_status()
            b64_pt = resp.json().get("data", {}).get("plaintext")
            if not b64_pt:
                log.warning("vault decrypt: empty plaintext field")
                return None
            return base64.b64decode(b64_pt).decode("utf-8")
    except httpx.HTTPStatusError as e:
        log.warning("vault decrypt http %s: %s", e.response.status_code, e.response.text[:200])
        return None
    except (httpx.RequestError, KeyError, ValueError, UnicodeDecodeError) as e:
        log.warning("vault decrypt failed: %s", e)
        return None
