# Fix Reports — 2026-06-05

One report per fix shipped on branch `fix/vector-search-and-pipeline-blockers`
(merged to `main` at commit `164a32c`). Each report covers the root cause, the
change, the files touched, and how it was verified.

| # | Report | Commit | Type |
|---|--------|--------|------|
| 01 | [Vector/semantic search + upload pipeline](01-vector-search-upload-pipeline.md) | `8897645` | bundle (5 fixes) |
| 02 | [Google Drive import + ingest pipeline](02-google-drive-import.md) | `3f7f5a7` | feature |
| 03 | [Kong route parity (prod 404s)](03-kong-route-parity.md) | `60e9fde` | fix |
| 04 | [email/intake internal-service auth](04-email-intake-internal-auth.md) | `63a7ce9` | fix |
| 05 | [Per-tenant rate limiting](05-per-tenant-rate-limiting.md) | `369aa9a` | feature |
| 06 | [NER + lang_detect repair](06-ner-langdetect-repair.md) | `df8eec0` | fix |
| 07 | [email/intake real versions + stream/OCR fixes](07-email-intake-real-versions.md) | `164a32c` | feature + 2 fixes |

> Commit `1049595` (PROJECT_STATUS doc update) is documentation only and has no
> separate report.

## One-line summaries

- **01** — semantic search returned nothing (Qdrant collection mismatch + a
  `readable_by` filter that hid public docs); uploads 500'd (missing
  `scan_results` migration); Postgres saturated at 100 connections; WebAuthn
  was already built but missing its Kong route.
- **02** — built the Google Drive import action on top of the existing OAuth
  slice, plus a reusable server-side ingest pipeline (storage gRPC +
  presigned-host dialer + session-cookie auth).
- **03** — Kong's prod gateway had drifted from the real routes; ~21 prefixes
  404'd in prod (worked in dev via the Vite proxy). Mirrored the proxy map +
  added 5 missing upstreams.
- **04** — email/intake workers couldn't create documents (401) because
  `POST /api/v1/documents` requires `SessionOrAPIKey`; added an internal-service
  auth path.
- **05** — the iPaaS poll-trigger + MCP endpoints had no per-tenant rate limit;
  wrapped them with a fail-closed `RateLimitPerTenantHTTP` at 60/min/tenant.
- **06** — NER crash-looped (the spaCy model was never installed) and
  lang_detect queried a non-existent column; both repaired.
- **07** — email + intake now create real documents-with-versions (OCR'd,
  searchable). Surfaced + fixed two pre-existing bugs: missing JetStream
  streams jamming the shared outbox, and OCR skipping text files.
