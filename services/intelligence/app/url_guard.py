"""M-23 — SSRF guard for the tenant-admin LLM `base_url`.

A tenant admin's base_url becomes litellm's api_base, so without a guard
the intelligence pod POSTs to whatever the admin names: cloud metadata,
in-cluster services, the host. check_base_url resolves the host and
refuses any non-public address (private, loopback, link-local, CGNAT,
ULA, reserved, multicast, IPv4-mapped forms of those) — unless the
provider is the on-prem `vllm_local` AND the host is on the operator's
SEDOC_LLM_PRIVATE_BASE_URL_ALLOWLIST.

It runs when the admin saves the config AND before every provider call
(stored values and changed DNS answers are re-checked). Error messages
never echo the resolved addresses.
"""
from __future__ import annotations

import ipaddress
import socket
from urllib.parse import urlsplit

from app.config import settings


class UnsafeBaseURLError(ValueError):
    """base_url is malformed, unresolvable, or resolves to a non-public
    address the provider is not allowed to use."""


def _allowlist() -> set[str]:
    raw = settings.llm_private_base_url_allowlist or ""
    return {h.strip().lower().rstrip(".") for h in raw.split(",") if h.strip()}


def _is_public(addr: str) -> bool:
    ip = ipaddress.ip_address(addr.split("%", 1)[0])
    if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped is not None:
        ip = ip.ipv4_mapped
    return ip.is_global and not ip.is_multicast


def check_base_url(url: str, *, provider: str) -> None:
    """Raise UnsafeBaseURLError unless `url` may be used as an LLM api_base."""
    try:
        parts = urlsplit((url or "").strip())
        port = parts.port
    except ValueError as exc:
        raise UnsafeBaseURLError("base_url is not a valid URL") from exc
    if parts.scheme not in ("http", "https"):
        raise UnsafeBaseURLError("base_url must be an http(s) URL")
    host = (parts.hostname or "").lower().rstrip(".")
    if not host:
        raise UnsafeBaseURLError("base_url has no host")
    try:
        infos = socket.getaddrinfo(host, port or (443 if parts.scheme == "https" else 80),
                                   proto=socket.IPPROTO_TCP)
    except (socket.gaierror, UnicodeError, OSError) as exc:
        raise UnsafeBaseURLError("base_url host does not resolve") from exc
    addrs = {info[4][0] for info in infos}
    if not addrs:
        raise UnsafeBaseURLError("base_url host does not resolve")
    try:
        all_public = all(_is_public(a) for a in addrs)
    except ValueError as exc:
        raise UnsafeBaseURLError("base_url host does not resolve") from exc
    if all_public:
        return
    if provider == "vllm_local" and host in _allowlist():
        return
    raise UnsafeBaseURLError(
        "base_url resolves to a private or reserved address; only an "
        "allow-listed vllm_local host may do that"
    )
