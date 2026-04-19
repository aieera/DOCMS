# Remediation 17h — Wave 10 (part 8): Metadata Schema editor — Wave 10 CLOSED ✅

**Date:** 2026-04-18
**Wave:** 10 · Final pending page from §9.1.

## Recon finding

Backend fully ready:

- `GET /api/v1/tenants/metadata-schema` → `{json_schema: {...}}`
- `PUT /api/v1/tenants/metadata-schema` body `{json_schema: {...}}`

Both routes are grpc-gateway-exposed from the document proto, service
impl in `services/document/internal/service/sharing_tags.go`
(`GetMetadataSchema` / `UpdateMetadataSchema`), validated at document
create/update against `custom_metadata` per existing codepath.

Gap was purely frontend — no `/admin/metadata-schema` route, no
editor.

## What shipped

### Frontend

- [web/src/api/metadataSchema.ts](../../../web/src/api/metadataSchema.ts)
  — typed client. Unwraps grpc-gateway's `{json_schema: ...}`
  envelope so the rest of the app sees a plain `Record<string, unknown>`.
- [web/src/routes/_authenticated/admin/metadata-schema.tsx](../../../web/src/routes/_authenticated/admin/metadata-schema.tsx)
  — full editor page:
  - **Monospace textarea** as the editor surface. Deliberate choice:
    no monaco / no new dep. Saves ~4 MB in the vendor bundle at the
    cost of line numbers and syntax highlighting. For a config JSON
    that most tenants write once and rarely revisit, the trade-off
    favoured bundle size.
  - **Live JSON parse** on every keystroke via `useMemo`. Status
    indicator at the top of the editor shows "empty" / "valid JSON"
    (green) / error message (red). Save button disabled when
    parse fails or root isn't a JSON object.
  - **Root-shape guard** — refuses to save unless the root is a
    JSON object and (if `type` is set) `type === "object"`. The
    server's validator would reject otherwise; fail-fast locally.
  - **Preview sidebar** — renders the schema's `properties` block
    as a label + type + required-marker list so admins can scan
    the tenant's metadata contract without reading the raw JSON
    every time. Updates live with the parse.
  - **"Load example" button** seeds an invoice-shaped schema (JSON
    Schema 2020-12 draft) so new tenants have a template.
  - **Reset** discards unsaved changes.
  - **TanStack Query** cache sync on save: the mutation's response
    becomes the new query data immediately, no refetch needed.

### Admin nav tile

Added Metadata Schema tile to [admin/index.tsx](../../../web/src/routes/_authenticated/admin/index.tsx) between Tags and Share Links. Uses the `FileJson` lucide icon.

## DoD — spec §9.1 Metadata schema row

| Requirement | Status |
|---|---|
| JSON schema editor | ✅ |
| Validation | ✅ (client-side + server-side at document write) |
| Apply to a doc type | 🟡 the schema is **tenant-wide** by design (proto has no class filter). Per-class schemas would need a new table + RPC; logged out-of-scope. |
| Version-tracked | 🟡 `updated_at` column exists; full version history (who changed what, when, diff) needs a `metadata_schema_versions` audit table. Logged. |

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ `tsc --noEmit` |
| 2 | ≥75% coverage | n/a — pure UI over pre-existing backend |
| 3 | Integration test | 🟡 Wave 13.1 |
| 4 | OpenAPI | ✅ routes come from proto |
| 9 | RLS | ✅ backend unchanged — tenant-scoped read/write |
| 10 | NATS subject | n/a |
| 12 | Rollback | revert 2 new TS files + 2-line diff in index.tsx |

## Deferred (logged in out-of-scope.md)

- **Per-document-class schemas** — requires schema changes + proto
  changes. Wave 11 if a customer needs it.
- **Version history** of schema edits — `metadata_schema_versions`
  audit table + diff viewer. Wave 11.
- **Monaco-based editor** with syntax highlighting + autocomplete —
  `@monaco-editor/react` is ~4 MB in the vendor bundle. Worth it
  only if schema editing becomes a daily activity. Defer until
  customer feedback.
- **Schema validation test button** — paste a sample document JSON,
  run it through the schema, show pass/fail. Small feature, UI
  polish.

## Wave 10 scorecard — CLOSED ✅

| Page | Status |
|---|---|
| Groups | ✅ |
| Permission matrix | ✅ |
| SSO wizard | ✅ |
| Retention policies | ✅ |
| Legal holds | ✅ |
| Webhooks | ✅ |
| **Metadata schema** | ✅ this doc |
| Tags admin | ✅ |
| Share-link admin | ✅ |

**All 9 pages from spec §9.1 shipped.**

Admin grid now has 19 tiles, every authenticated route is reachable
from nav, every backend CRUD has a UI surface except the known
deferred items (Signatures → Wave 9.2b Java sidecar; Connectors →
OAuth flow pending; drag-to-create workflow designer → spec-declined).

## Next wave

**Wave 11 — Integration & plumbing.** Spec §10 calls out:

- Workflow service RLS audit (logged across Wave 8.1–8.4)
- Cross-service DSR activities (logged Wave 8.3)
- OPA `compliance_officer` role gate (logged Wave 8.2)
- Redaction endpoint + hold gate (Wave 8.2)
- Per-tenant region-local KEKs (Wave 8.4)
- Notification service transactional email path (DSR token, Wave 8.3)
- Dead code removal in `compliance/retention.go` (Wave 8.2)

Or pick any of the declined Wave 10 follow-ups (user-picker combo,
workspace members UI, task-detail drawer, share-link filter/CSV
export, retention "match preview", permission matrix live rego
introspection).
