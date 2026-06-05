# 01 — Vector/semantic search + upload pipeline

**Commit:** `8897645` · **Type:** bundle (5 distinct fixes) · **Date:** 2026-06-05

This commit unblocked the document upload + semantic-search path end-to-end.
It bundles five independent fixes that were all blocking the same flow.

---

## Fix 1 — Vector/semantic search returned nothing

**Root cause (two bugs):**
1. **Qdrant collection mismatch.** The Python `intelligence` containers had
   `SEDOC_QDRANT_COLLECTION` unset, so they fell back to the config default
   `dms_vectors`, while the Go `search` service read `vaultdms_chunks`. Ingest
   wrote one collection, query read another → semantic always empty, hybrid
   silently degraded to lexical.
2. **`readable_by` "everyone" filter.** The vector query passed only the user's
   group IDs to Qdrant's `readable_by` match; public docs are tagged
   `readable_by:["everyone"]`, so they were invisible to semantic search. The
   lexical path (`opensearch/query.go`) special-cases `"everyone"`; the vector
   path didn't.

**Fix:**
- Set `SEDOC_QDRANT_COLLECTION: vaultdms_chunks` on both Python services in
  `docker-compose.yml`.
- Added `readablePrincipals()` in `search/internal/service/hybrid.go` =
  `[userID] + groups + ["everyone"]`.
- `web/.../search.tsx` now sends `search_mode:"hybrid"` (it was hardcoded
  lexical despite advertising "semantic").
- Corrected the misleading `model="bge-m3"` label in the embed endpoint to the
  actual `all-MiniLM-L6-v2`.

**Verified:** Playwright — a paraphrase query with zero keyword overlap returns
the doc via semantic + hybrid (raw Qdrant score ~0.48).

## Fix 2 — Uploads 500'd on CompleteUpload

**Root cause:** the storage service writes scan outcomes to a `scan_results`
table that **had no migration** → `relation "scan_results" does not exist`.

**Fix:** added `services/document/migrations/000067_scan_results.{up,down}.sql`
(tenant-first PK, FORCE RLS, isolation policies; storage tables live in the
document migration dir by convention).

## Fix 3 — Postgres connection saturation

**Root cause:** `postgres:16` default `max_connections=100` is too low for 13 Go
service pools (`MinConns 5, MaxConns 50` each) + Python pools → `FATAL: too many
clients already`, starving freshly-started services.

**Fix:** `command: ["postgres", "-c", "max_connections=400"]` in compose.

## Fix 4 — WebAuthn Kong route

**Root cause:** WebAuthn/passkeys was already fully implemented (flows, repo,
routes, lib wiring, frontend) — the PROJECT_STATUS doc was stale. But Kong had
no `/api/v1/auth/webauthn` route, so passkey endpoints 404'd in prod (dev worked
via the Vite proxy).

**Fix:** added the `auth-webauthn` route (with a rate-limit) to
`deploy/gateway/kong.yaml`.

## Fix 5 — gitignore the generated build index

`web/src/generated/load-test-reports.ts` is regenerated on every `npm run
dev`/`build`; tracking it only produced timestamp churn. Untracked + gitignored.

---

## Files changed
`.gitignore`, `deploy/gateway/kong.yaml`, `docker-compose.yml`,
`docs/PROJECT_STATUS.md`, `services/document/migrations/000067_scan_results.{up,down}.sql`,
`services/intelligence/app/api/routes.py`,
`services/search/internal/service/{hybrid,service}.go`,
`web/src/routes/_authenticated/search.tsx`,
`web/src/generated/load-test-reports.ts` (removed).
