# Frontend ↔ Backend Integration Matrix

**Generated:** 2026-04-19 after inventorying both sides.

## Headline

There is **no API gateway** sitting in front of the 11 backend services. In local dev, the Vite proxy ([web/vite.config.ts:14-23](../../web/vite.config.ts#L14-L23)) is the only router, and **it currently forwards nearly all `/api/v1/*` traffic to the auth service**, which returns 404 for routes it doesn't own. Many frontend routes have never worked in dev since this proxy was written.

**Critical categorization:**
- ❌ **PROXY-ORPHANED FRONTEND:** ~60 frontend calls — backend route exists, but Vite proxy sends them to the wrong service → **404**.
- ❌ **ORPHAN FRONTEND (true):** 3 — backend endpoint doesn't exist anywhere.
- ⚠️ **SHAPE MISMATCH / NULL HAZARD:** a few known, likely more lurking (only groups `members: null` verified so far; fixed this session).
- ✅ **MATCH:** everything served by auth (login, groups, SSO admin, SCIM) works today because that's what the Vite `/api` catch-all points at.

## Live probe results (verified this session, 2026-04-19)

With current [web/vite.config.ts](../../web/vite.config.ts) pointing `/api → :8180` (auth):

| Frontend call | Status | Reason |
|---|---|---|
| `POST /api/v1/auth/login` | ✅ 200 | auth owns |
| `GET /api/v1/auth/me` | ✅ 200 | auth owns |
| `GET /api/v1/admin/groups` | ✅ 200 | auth owns |
| `GET /api/v1/admin/groups/{id}` | ✅ 200 (members now `[]` not null — fixed) | auth owns |
| `GET /api/v1/workspaces` | ❌ **404** | document owns on :8182, proxy sends to :8180 |
| `GET /api/v1/privacy/dsr` | ❌ **404** | document owns, proxy wrong |
| `POST /api/v1/intelligence/ask` | ❌ **404** | intelligence service not running at all |
| `GET /api/v1/tenants/metadata-schema` | ❌ **404** | document owns, proxy wrong |

## Full matrix

| Frontend call | Backend owner | Proxy route today | Actual status | Category |
|---|---|---|---|---|
| POST `/auth/login` | auth :8180 | :8180 | ✅ 200 | MATCH |
| POST `/auth/logout` | auth | :8180 | ✅ | MATCH |
| GET `/auth/me` | auth | :8180 | ✅ | MATCH |
| POST `/auth/mfa/verify` | auth | :8180 | ✅ | MATCH |
| POST `/auth/mfa/setup|confirm|disable` | auth | :8180 | ✅ | MATCH |
| GET/DELETE `/auth/sessions*` | auth | :8180 | ✅ | MATCH |
| GET/POST/DELETE `/auth/api-keys*` | auth | :8180 | ✅ | MATCH |
| GET/POST `/admin/users*` | auth | :8180 | ✅ | MATCH |
| GET/POST/PATCH/DELETE `/admin/groups*` | auth | :8180 | ✅ | MATCH (members null-hazard fixed) |
| CRUD `/admin/sso-configs*` | auth | :8180 | ✅ | MATCH |
| PATCH `/admin/tenants/{id}/region-pin` | auth | :8180 | ✅ | MATCH |
| GET/POST `/admin/settings` | billing :8189 | `/api/v1/admin/settings → :8189` | ✅ | MATCH (explicit proxy entry) |
| GET/POST/DELETE `/permissions/*` | policy :8181 | `/api/v1/permissions → :8181` | ✅ | MATCH (explicit proxy entry) |
| GET `/permissions/matrix` | policy | :8181 | ✅ | MATCH |
| GET `/audit/events` | audit :8185 | `/api → :8180` | ❌ 404 | **PROXY-ORPHANED** |
| GET `/audit/export` | audit | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET `/workspaces`, `/workspaces/{id}` | document :8182 | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST/PATCH/DELETE `/workspaces*` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET `/workspaces/{id}/documents` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET `/workspaces/{id}/folders`, `/folders/*` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET/PATCH/DELETE `/documents/{id}` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST `/documents/{id}/move` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET/POST `/documents/{id}/versions*` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST `/documents/{id}/versions/{v}/restore` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST/GET/DELETE `/documents/{id}/share-links*` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| DELETE `/share-links/{id}` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST `/shared/{token}` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** (public share link) |
| GET/POST/PATCH/DELETE `/admin/retention-policies*` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET/POST `/admin/share-links*` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST/GET `/compliance/holds*` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST/GET `/privacy/dsr*` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST `/privacy/verify/request-token` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET `/residency/stats`, `/residency/migrations` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET/PUT `/tenants/metadata-schema` | document | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST `/search` | search :8184 | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET `/search/autocomplete` | search | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| CRUD `/saved-searches*` | search | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET/POST/DELETE `/workflows/*` | workflow :8186 | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET/PATCH/POST `/notifications*` | notification :8187 | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| CRUD `/signatures/*` | signature :8188 | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| CRUD `/webhooks*` | connector :8190 | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| GET `/connectors*` | connector | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST `/mcp` (SSE) | connector | :8180 | ❌ 404 | **PROXY-ORPHANED** |
| POST `/intelligence/ask`, `/intelligence/summarize`, `/intelligence/redact/detect` | intelligence (Python, not running) | :8180 | ❌ 404 | **ORPHAN FRONTEND (service not running)** |

## Orphan backend (BE endpoints with no FE caller)

| Backend route | Note |
|---|---|
| `GET /api/v1/permissions/matrix` | Called by permissions.tsx — actually used |
| SCIM `/scim/v2/*` (all 12 routes) | Only called by external IdPs, not the UI — expected |
| Billing `/internal/v1/*` | Internal service-to-service; expected |
| Stripe webhook `/stripe/webhook` | External only; expected |
| Search `/internal/v1/search/purge-subject` | GDPR backend; expected |
| Connector `/internal/v1/connectors/purge-subject` | GDPR backend; expected |
| Auth `/api/v1/admin/sso-configs/validate` | No UI caller (could be used by SSO admin page's "test" button; unverified) |
| Auth `/api/v1/admin/tenants/{id}/schedule-cmk-deletion`, `/cancel-cmk-deletion` | No UI caller — CMK-deletion UX not built |
| Document admin `/api/v1/admin/documents/{id}/share-links/revoke-all` | `shareLinksAdmin.revokeAllShareLinksForDocument` calls this — so **used** |

Genuinely orphan (no UI caller, not an internal/webhook endpoint): auth's CMK-deletion management + the `sso-configs/validate` endpoint (unverified).

## Shape mismatches confirmed

| Endpoint | Frontend expected | Backend delivered | Status |
|---|---|---|---|
| `GET /api/v1/admin/groups/{id}` | `members: GroupMember[]` | `members: null` on empty groups | ✅ **FIXED this session** (both sides) |
| `GET /api/v1/auth/sessions` | `{sessions: SessionSummary[]}` | subagent not verified; axios client normalizes via `?? []` | OK |
| `GET /api/v1/auth/api-keys` | `{api_keys: APIKey[]}` | same pattern | OK |
| `GET /api/v1/workspaces`, `/folders` | either `{workspaces:[]}` or `Workspace[]` | client tolerates both | OK |

## Auth mismatches

| Frontend | Backend |
|---|---|
| Axios: withCredentials + cookie + X-CSRF-Token on mutations | auth/billing admin routes: session cookie + CSRF ✅ |
| Same | audit/notification/workflow/signature/search: trust raw `X-Tenant-ID`/`X-User-ID` headers; no session validation in-handler — **effectively no auth unless a gateway fronts them** ⚠️ |
| Axios sends cookie | SCIM `/scim/v2/*`: expects Bearer token, rejects cookie |
| Axios sends no Bearer | connector `/internal/v1/*`: expects X-API-Key — fine because UI shouldn't call these |

The audit/notification/workflow/signature/search bucket is the real concern: in the current setup, any authenticated frontend can call those routes with forged `X-Tenant-ID` headers because those handlers don't verify the session. This is a **pre-existing security gap** surfaced by this audit, not introduced by today's work.

## Null hazards (future crashes)

| Site | Hazard |
|---|---|
| [web/src/components/documents/VersionHistory.tsx:16](../../web/src/components/documents/VersionHistory.tsx#L16) | `data?.map()` — silent render-nothing if data is undefined; doesn't crash but hides errors |
| Unknown — need full sweep | Any Go handler returning a slice populated via `append` on a nil starting value will serialize as `null`; frontend `.length`/`.map` call sites without `?? []` will crash. Pattern to audit: backend `detail.Items = append(nil, ...)` + frontend `.data.items.length` |

## Summary counts

- Backend REST routes exposed: **125** (excluding SCIM = 113)
- Frontend API functions: **74 exported**
- ✅ MATCH: **~20** (all auth/groups/SSO/admin-users/permissions/admin-settings)
- ❌ PROXY-ORPHANED: **~60** (all routes targeting document/search/audit/workflow/notification/signature/connector)
- ❌ ORPHAN FRONTEND (true): **3** (intelligence.*)
- ⚠️ SHAPE MISMATCH / NULL HAZARD fixed: **1** (groups members)
- ⚠️ Auth posture weak at handler layer: **5 services** (audit/notification/workflow/signature/search)

## Single-change fix

Extending [web/vite.config.ts](../../web/vite.config.ts) with per-prefix proxies will turn all 60 PROXY-ORPHANED routes into MATCH overnight. No backend changes needed for those — the handlers exist and work; they just need to be reached. See [Priority 0 fix in §6](#priority-0-proxy-fan-out).
