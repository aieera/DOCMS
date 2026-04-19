# Remediation 17c — Wave 10 (part 3): Tags admin page

**Date:** 2026-04-17
**Wave:** 10 · Scope: 1 of the pending 6 pages from §9.1.

## Recon finding

Tags backend is complete:

- `tags_catalog` table with RLS (migration 000001).
- Service methods `CreateTag` / `ListTags` / `DeleteTag` in
  `services/document/internal/service/sharing_tags.go`.
- Repository with doc-count aggregate + scrub-on-delete in
  `services/document/internal/repository/tag_repo.go`.
- gRPC + **grpc-gateway REST routes** already exposed via
  `google.api.http` annotations in the proto:
  `POST /api/v1/tags`, `GET /api/v1/tags`, `DELETE /api/v1/tags/{tag_id}`.

Gap was purely frontend: no `/admin/tags` route, no API client.

## What shipped

### Frontend — API client

[web/src/api/tags.ts](../../../web/src/api/tags.ts):

- Typed `Tag` interface mirroring the proto (id, name, color, document_count).
- `listTags` unwraps the grpc-gateway `{tags: [...]}` envelope so the
  rest of the app sees a plain array.
- `createTag({name, color})`, `deleteTag(id)`.

### Frontend — admin page

[web/src/routes/_authenticated/admin/tags.tsx](../../../web/src/routes/_authenticated/admin/tags.tsx)
replaces the EmptyState placeholder with:

- Create form: name input + 12-preset color picker + live preview
  chip.
- Table listing every tag with its coloured chip + document count
  (from the repo's aggregate subquery) + delete button.
- Destructive-delete confirmation includes the doc count — operators
  see "this is applied to N documents; the tag will be removed from
  them too" before confirming, since the backend scrubs in the same
  transaction.
- TanStack Query + cache invalidation on mutation.

### Admin landing — nav tile

[web/src/routes/_authenticated/admin/index.tsx](../../../web/src/routes/_authenticated/admin/index.tsx)
adds a Tags tile between Webhooks and Settings.

## DoD — spec §9.1 Tags row

| Requirement | Status |
|---|---|
| Tenant-level tag CRUD | ✅ create + list + delete |
| Per-doc tagging already works | ✅ pre-existing |

Rename is intentionally not shipped — tag renames would need a
scrub-and-reapply across the `documents.tags` array (the array
stores tag names, not IDs, so renames are expensive and surprising).
Logged out-of-scope.

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ `tsc --noEmit` |
| 2 | ≥75% coverage on new files | 🟡 UI-only; backend already covered |
| 3 | Integration test | 🟡 Wave 13.1 |
| 4 | OpenAPI | ✅ routes come from the proto — already in the generated bundle |
| 5 | Prom metrics | 🟡 Wave 13.6 |
| 9 | RLS | ✅ backend unchanged — `tags_catalog` RLS-wrapped |
| 12 | Rollback | revert 2 TS files + the 2-line index.tsx diff |
| 13 | Runbook | n/a — trivial CRUD |

## Deferred (logged in out-of-scope.md)

- **Rename tag** — requires `documents.tags` array scrub; surprising
  semantics if a user has unsaved edits.
- **Bulk delete / color edit** — follow-up.
- **Color picker beyond 12 presets** — custom hex input.
- **`tags_*_total` Prom counters** — Wave 13.6.

## Wave 10 scorecard

| Page | Status |
|---|---|
| Groups | ✅ |
| Permission matrix | pending |
| SSO wizard | pending |
| Retention policies | pending (needs backend first) |
| Legal holds | ✅ Wave 8.2 |
| Webhooks | ✅ |
| Metadata schema | pending |
| **Tags admin** | ✅ this doc |
| Share-link admin | pending |

## Next prompt

Share-link admin remaining among "existing backend" targets. Beyond
that: permission matrix (needs OPA introspection), SSO wizard
(multi-step), retention policies (needs backend), metadata schema
(monaco editor).
