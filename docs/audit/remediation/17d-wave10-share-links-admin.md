# Remediation 17d — Wave 10 (part 4): Share-link admin page

**Date:** 2026-04-17
**Wave:** 10 · Scope: 1 of the pending 5 pages from §9.1.

## Recon finding

Per-document share-link endpoints existed (gRPC + grpc-gateway REST
at `/api/v1/documents/{id}/share-links`). But:

- No tenant-wide listing — admins had to know the document ID first.
- No bulk revoke-all-for-document endpoint.
- Admin page was a placeholder EmptyState.

## What shipped

### Backend — 3 new endpoints on document service

[services/document/internal/handler/share_links_admin_handler.go](../../../services/document/internal/handler/share_links_admin_handler.go):

| Method | Path | Effect |
|---|---|---|
| GET | `/api/v1/admin/share-links?status=active` | list every link for the tenant. Status: `active` (default) or `all`. |
| POST | `/api/v1/admin/share-links/{id}/revoke` | deactivate one link. |
| POST | `/api/v1/admin/documents/{documentId}/share-links/revoke-all` | bulk-deactivate every active link on a document. Returns `{revoked: N}`. |

Dedicated mux pattern matching compliance/privacy/residency. Reads
`X-Tenant-ID` / `X-User-ID` headers + injects via
`auth.WithUser(ctx, auth.UserInfo{TenantID, ID})` so service-layer
`mustCaller()` resolves.

### Service + repo extensions

[services/document/internal/service/sharing_tags.go](../../../services/document/internal/service/sharing_tags.go):

- `ListShareLinksTenantWide(ctx, onlyActive)` — tenant-wide list,
  `requirePermission("admin", "workspace", "share_links")` gated.
- `RevokeAllShareLinksForDocument(ctx, documentID)` — same permission
  gate, returns count of deactivated rows.

Both strip `token` + `password_hash` from responses before they leave
the service layer.

[services/document/internal/repository/sharelink_repo.go](../../../services/document/internal/repository/sharelink_repo.go):

- `ListByTenant(tx, tenantID, onlyActive)` — LEFT JOIN `documents`
  for the title. Limit 500 rows per call.
- `RevokeAllForDocument(tx, tenantID, documentID)` — single
  `UPDATE ... WHERE is_active=true` with `RowsAffected()` returned.

New model type `model.ShareLinkAdmin = ShareLink + DocumentTitle` so
the base `ShareLink` used by per-doc endpoints stays unchanged.

`ShareLinkRepository` interface gained the two new methods.

### Frontend

- [web/src/api/shareLinksAdmin.ts](../../../web/src/api/shareLinksAdmin.ts) —
  typed client (separate file so per-doc `shareLinks.ts` stays
  untouched; the two surfaces have different response shapes).
- [web/src/routes/_authenticated/admin/share-links.tsx](../../../web/src/routes/_authenticated/admin/share-links.tsx) —
  tenant-wide view grouped by document:
  - Active / All filter pills.
  - One card per document with title, ID, link count, and a
    "Revoke all" button (confirmation includes active-link count).
  - Per-document table with status badge, lock icon for password-
    protected, permissions, view count (with `max_views` cap when
    set), expiry/last-access/created relative timestamps, per-row
    Revoke button.
  - TanStack Query + invalidation on every mutation.
- [admin/index.tsx](../../../web/src/routes/_authenticated/admin/index.tsx) —
  new Share Links tile between Tags and Settings.

## DoD — spec §9.1 Share-link admin row

| Requirement | Status |
|---|---|
| List active share links per doc | ✅ grouped-by-document view |
| Revoke one | ✅ per-row button |
| Revoke-all button per doc | ✅ with confirmation |

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ Go + TS |
| 2 | Test coverage | 🟡 no HTTP test harness in this handler package yet; the repo methods hit the existing integration path in Wave 13.1 |
| 3 | Integration test | 🟡 Wave 13.1 |
| 4 | OpenAPI | ⚠ Wave 13.5 |
| 5 | Prom metrics | 🟡 Wave 13.6 (`share_links_revoked_total`) |
| 9 | RLS | ✅ `WithTenantTx` wraps every read + write; service's `requirePermission` gates at the OPA layer |
| 10 | NATS subject | n/a — revocation reuses existing `Deactivate` path; no new events added in this prompt |
| 12 | Rollback | revert model diff + repo diff + service diff + handler file + main.go wiring + 2 TS files + index tile |

## Deferred (logged in out-of-scope.md)

- **`dms.sharelink.revoked.v1` outbox event** on individual revoke +
  bulk revoke. Today only `sharelink.created.v1` is emitted from
  create; revocation audit relies on `audit_events` table entries
  written by the OPA `requirePermission` check.
- **Export share-link audit CSV** from the admin page.
- **Filter by document title / creator / expires-before** — UI polish.
- **`share_links_revoked_total` / `share_links_revoke_all_total` Prom
  counters** — Wave 13.6.
- **Go handler-level tests** — handler package would benefit from a
  shared test harness (logged for Groups too).

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
| Tags admin | ✅ |
| **Share-link admin** | ✅ this doc |

Four pages pending — **all need new backend or heavier UI work**:

- **Permission matrix**: needs OPA bundle introspection endpoint.
- **SSO wizard**: multi-step SAML/OIDC metadata upload + test
  assertion flow.
- **Retention policies**: needs CRUD endpoints on
  `retention_policies` table, then UI.
- **Metadata schema**: needs monaco editor + JSON Schema library
  integration.

## Next prompt

Suggestions in priority order:

1. **Retention policies** — biggest operator unblock; backend CRUD
   is a moderate Go task followed by a straightforward form UI.
2. **Permission matrix** — read-only; OPA introspection is a small
   gRPC method addition in the policy service.
3. **SSO wizard** — largest scope; best as two prompts (upload +
   test assertion).
4. **Metadata schema** — depends on picking a JSON Schema validator
   UX; could wait.
