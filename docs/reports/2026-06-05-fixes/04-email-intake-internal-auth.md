# 04 — Internal-service auth so email/intake can create documents

**Commit:** `63a7ce9` · **Type:** fix (connector + auth) · **Date:** 2026-06-05

## Summary

Email ingestion (ADR 0087) + watched-folder intake (ADR 0088) create documents
via the connector's `DocumentClient` using a gateway signature + identity
headers — but `POST /api/v1/documents` is gated by `SessionOrAPIKey`, which only
accepts a session cookie or a `Bearer vdms_` API key. These are autonomous
workers with neither, so **every createDocument 401'd** (ingestion silently
created nothing). Found while building Drive import (which has a triggering
user's session to forward; workers don't).

## Root cause

`pkg/middleware.SessionOrAPIKey` dispatch: `Bearer vdms_*` → API-key path; else
→ session-cookie path. A gateway-signed request with identity headers matched
neither → fell through to session auth → no cookie → 401.

## Fix

Added an **internal-service auth path** to `SessionOrAPIKey`: when the request
carries `X-Internal-Service-Key == SEDOC_INTERNAL_API_KEY` (constant-time
compared), trust the identity it acts on behalf of from `X-Auth-Tenant-ID /
X-User-ID / X-User-Role` (the email/intake config's `CreatedBy` owner).

**Security:** identity headers are trusted **only** when the key matches. The
gateway's request-transformer now **strips inbound** `X-Internal-Service-Key`
(and `X-Tenant-ID`) so external callers can never present them — only in-cluster
services (which call backends directly, bypassing Kong) carry the key. Mirrors
the existing `/internal/v1/retention/sweep` trust model. `SEDOC_INTERNAL_API_KEY`
wired into the compose `x-go-env` anchor.

## Verified

- createDocument via internal-service auth → **200** (was 401).
- Without the key → **401** (boundary holds).
- A spoofed key **through Kong :8080** → **401** (gateway strips the header).

Middleware tests + gateway archtest pass.

## Files changed
`pkg/middleware/session_or_apikey.go`, `docker-compose.yml`,
`deploy/gateway/kong.yaml`,
`services/connector/internal/email/document_client.go`.
