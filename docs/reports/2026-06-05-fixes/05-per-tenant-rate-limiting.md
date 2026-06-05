# 05 — Per-tenant rate limiting on iPaaS trigger + MCP endpoints

**Commit:** `369aa9a` · **Type:** feature (ADR 0090 §16.4) · **Date:** 2026-06-05

## Summary

The `/api/v1/integrations/triggers/*` poll endpoints (documents, signatures,
workflows) and the MCP server were API-key-authed but had **no app-level rate
limit** — only Kong's global 1000/min, which keys per-IP (Kong has no key-auth).
So any valid `integrations:read` key could poll unboundedly with no per-tenant
ceiling. Capped them at 60/min/tenant.

## Root cause / key insight

The Redis limiter (`pkg/middleware/ratelimit.go`) was **already per-tenant** —
its bucket key is `ratelimit:{tenant}:{group}:{minute}` with a per-tenant
override at `ratelimit:config:{tenant}:{group}` — but only wired to `/auth` and
`/graphql`. So this was **coverage, not new limiter logic**.

## Fix

- Added `RateLimitPerTenantHTTP`: identical to `RateLimitHTTP` except it fails
  **closed** (500) when no tenant is on ctx. On an always-authenticated route a
  missing tenant means a wiring/bypass bug, not a public path. (`Allow()` still
  fails **open** on Redis errors so a cache blip can't break ingest.)
- Wrapped the three iPaaS trigger routes (document/signature/workflow) +
  `/api/v1/mcp(/sse)` with it at **60/min/tenant**, nested **inside**
  `APIKeyAuth` so the key's tenant is on ctx. mcp-server gained a Redis client
  (it had none).

## Verified

| Check | Result |
|---|---|
| Unit: tenant isolation (A→429 doesn't throttle B) | pass |
| Unit: fail-closed on missing tenant | pass |
| Unit: per-tenant override | pass |
| **Live**: hammer the real endpoint with a real key | 60×200 → 429 at request #61 |
| Redis key is per-tenant | `ratelimit:{tenant}:integrations:{min}` |

## Note

The dual-auth ERP push routes (`POST /documents`, `/versions`, `/folders` via
`SessionOrAPIKey`) were deliberately left on Kong-global only, to avoid
throttling web-UI session callers; revisit if ERP abuse appears.

## Files changed
`pkg/middleware/ratelimit.go`, `pkg/middleware/ratelimit_test.go` (new),
`services/document/cmd/server/main.go`,
`services/mcp-server/cmd/server/main.go`,
`services/signature/cmd/server/main.go`,
`services/workflow/cmd/server/main.go`.
