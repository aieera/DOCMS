# Backend API Inventory

**Generated:** 2026-04-19 · source: grep walk across `services/` + probe against live services on 8180–8190.

All paths below are what the service's own HTTP mux registers. **Nothing in this repo stitches them together** — there is no running API gateway; routing is done by the frontend's Vite dev proxy (see [api-integration-matrix.md](api-integration-matrix.md)).

## Ownership summary

| Service | HTTP port (dev) | Path prefix(es) owned |
|---|---|---|
| auth | 8180 | `/api/v1/auth/*`, `/api/v1/admin/users*`, `/api/v1/admin/groups*`, `/api/v1/admin/sso-configs*`, `/api/v1/admin/tenants/*`, `/scim/v2/*` |
| policy | 8181 | `/api/v1/permissions/*` |
| document | 8182 | `/api/v1/documents/*`, `/api/v1/workspaces/*`, `/api/v1/folders/*`, `/api/v1/shared/*`, `/api/v1/storage/*`, `/api/v1/compliance/holds*`, `/api/v1/privacy/dsr*`, `/api/v1/privacy/verify/*`, `/api/v1/residency/*`, `/api/v1/admin/share-links*`, `/api/v1/admin/retention-policies*`, `/api/v1/admin/documents/*/share-links/*`, `/api/v1/tenants/metadata-schema` |
| storage | 8183 | gRPC only |
| search | 8184 | `/api/v1/search*`, `/api/v1/saved-searches*`, `/internal/v1/search/*` |
| audit | 8185 | `/api/v1/audit/*` |
| workflow | 8186 | `/api/v1/workflows/*` |
| notification | 8187 | `/api/v1/notifications*` |
| signature | 8188 | `/api/v1/signatures/*` |
| billing | 8189 | `/internal/v1/*`, `/api/v1/admin/settings`, `/stripe/webhook` |
| connector | 8190 | `/api/v1/webhooks*`, `/api/v1/connectors*`, `/api/v1/mcp`, `/internal/v1/connectors/*` |
| intelligence | — | Python service — NOT currently running. Expected prefix `/api/v1/intelligence/*` |

## Full route table

125 routes inventoried by the subagent walk. **Ambiguous auth** flagged in the notes: audit/notification/signature/workflow/search rely on `X-Tenant-ID` / `X-User-ID` request headers (set by a proxy or by the frontend's axios interceptor) — not session-cookie auth at the handler layer.

See Appendix for the full enumerated table; shipped inline with this report was the 125-row `Service | Mux | Method | Full path | Handler | Auth | Response | File:Line` table from the Explore subagent. Abbreviated counts:

| Service | REST routes | gRPC services | Notes |
|---|---|---|---|
| auth | 52 (coreMux 19, adminMux 17, scimMux 16) | 1 (AuthService stub) | Highest surface area |
| document | 25 (core + compliance + privacy + residency + admin + storage proxy + redaction) | 4 (Document/Workspace/Folder/ShareLink) | Also owns legal-holds, residency, retention endpoints |
| policy | 5 | 1 (PolicyService) | |
| audit | 5 | — | X-Tenant-ID header auth |
| billing | 8 (6 internal + 2 admin) | — | Stripe webhook is signature-auth |
| connector | 14 | — | Includes MCP SSE endpoint |
| notification | 6 | — | |
| search | 6 | — | |
| signature | 6 | — | |
| workflow | 8 | — | |
| storage | 0 REST | 1 (StorageService — gRPC-only) | HTTP access via document service proxy |

### Key auth variations
- **Session cookie (`dms_session`)**: auth `/api/v1/auth/*` authenticated routes, billing `/api/v1/admin/settings`, policy (via middleware).
- **CSRF token (`dms_csrf` cookie → X-CSRF-Token header)**: required on every mutating auth/billing request.
- **X-API-Key**: billing `/internal/v1/*` (value from `VAULTDMS_INTERNAL_API_KEY`), connector `/internal/v1/connectors/*`, search `/internal/v1/search/*`.
- **SCIM Bearer token**: `/scim/v2/*` — per-tenant bearer, not the session.
- **Raw X-Tenant-ID / X-User-ID headers**: audit, notification, signature, workflow, search. **Assumes upstream gateway has already authenticated the caller** — dangerous if exposed directly.
- **Public** (no auth): `/api/v1/auth/{register,login,mfa/verify,mfa/recovery}`, `/api/v1/auth/saml/*`, `/api/v1/auth/oidc/*`, `/api/v1/privacy/verify/request-token`, `/api/v1/shared/{token}`.

### Response-shape notes
- `GET /api/v1/admin/groups/{id}` previously serialized `"members": null` on empty groups; **fixed this session** in [services/auth/internal/handler/groups.go:153](../../services/auth/internal/handler/groups.go#L153). Other handlers may have the same Go nil-slice pattern — see null-hazard audit in [api-integration-matrix.md](api-integration-matrix.md).
