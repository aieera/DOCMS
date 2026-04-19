"""Parse storage URIs produced by Wave 5 Prompt 5.1.

Standalone so unit tests can import without pulling the DB, NATS, or
Celery stack.
"""
from __future__ import annotations


def parse_storage_uri(uri: str) -> tuple[str, str]:
    """Parse `s3://<bucket>/<key...>` → (bucket, key).

    Keys may contain slashes; everything after the bucket boundary is
    taken verbatim. Raises ValueError for any non-s3 URI or a URI
    missing bucket or key.
    """
    if not uri or not uri.startswith("s3://"):
        raise ValueError(f"not an s3 uri: {uri!r}")
    without_scheme = uri[len("s3://"):]
    slash = without_scheme.find("/")
    if slash <= 0:
        raise ValueError(f"missing bucket or key in s3 uri: {uri!r}")
    key = without_scheme[slash + 1:]
    if not key:
        raise ValueError(f"missing bucket or key in s3 uri: {uri!r}")
    return without_scheme[:slash], key
