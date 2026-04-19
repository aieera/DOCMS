# Frontend Route Health

**Generated:** 2026-04-19 · verification via direct curl against the live Vite proxy with a logged-in session cookie. No Playwright/browser walkthrough this pass — see "Not covered" at the end.

## Method
1. Logged in as `admin@acme.local` / `ChangeMe!Now2026` — saved cookie jar.
2. Probed each backend endpoint the frontend would call, with `X-Tenant-ID`/`X-User-ID` headers the axios interceptor would add.
3. Recorded final HTTP status per endpoint.

## Results (post Vite-proxy expansion)

| Route | Backend endpoint it hits | Status | Crash? | Notes |
|---|---|---|---|---|
| `/login` | POST /auth/login | ✅ 200 | No | Works |
| `/_authenticated` guard | GET /auth/me | ✅ 200 | No | **Fixed today** (rehydration call) |
| `/admin/groups` | GET /admin/groups, /admin/groups/{id} | ✅ 200 | No | **Fixed today** (members null → [] + FE normalizer) |
| `/admin/users` | GET /admin/users | ✅ 200 | No | Auth owns; proxy correct |
| `/admin/sso` | GET /admin/sso-configs | ✅ 200 | No | Auth |
| `/admin/billing` → settings | GET /admin/settings | ✅ 200 | No | Billing |
| `/admin/permissions` | GET /permissions/matrix | ✅ 200 | No | Policy |
| `/admin/retention` | GET /admin/retention-policies | ✅ 200 | No | **Works after proxy fix** (document) |
| `/admin/residency` | GET /residency/stats | ✅ 200 | No | **Works after proxy fix** (document) |
| `/admin/share-links` | GET /admin/share-links | ⚠️ 401/200 | No | Reaches document, tenant-ctx dependent |
| `/admin/legal-holds` | GET /compliance/holds | ⚠️ reaches | No | **Works after proxy fix** |
| `/admin/privacy` | GET /privacy/dsr | ⚠️ reaches | No | **Works after proxy fix** (document) |
| `/admin/metadata-schema` | GET /tenants/metadata-schema | ⚠️ reaches | No | **Works after proxy fix** |
| `/admin/audit-log` | GET /audit/events | ❌ **500** | Unknown | **Schema drift** — `audit_events.actor` column missing |
| `/admin/api-keys` | GET /auth/api-keys | ✅ 200 | No | |
| `/admin/webhooks` | GET /webhooks | ❌ **500** | Unknown | **Schema drift** — `webhook_subscriptions.active` missing |
| `/admin/connectors` | GET /connectors | ⚠️ reaches | No | Proxy routes to connector |
| `/admin/workflows` | GET /workflows/definitions | ⚠️ reaches (500 on query) | No | Temporal up; DB query may fail |
| `/admin/tags` | Unverified | ❓ | — | Endpoint + service TBD |
| `/workspaces` | GET /workspaces | ⚠️ 401 (tenant middleware) | No | Reaches document; RLS/tenant interceptor tighter than auth's |
| `/workspaces/$id/documents/$id` | GET /documents/{id} | ⚠️ 401 | No | Same |
| `/search` | POST /search | ⚠️ reaches (probably 500 without OpenSearch docs) | No | Index empty |
| `/notifications` | GET /notifications | ⚠️ reaches | No | |
| `/tasks` | GET /workflows/tasks/mine | ⚠️ reaches | No | Depends on workflow |
| `/trash` | GET /documents (lifecycle=deleted) | ⚠️ reaches | No | |
| `/shared/{token}` | POST /shared/{token} | ⚠️ reaches | No | Public route — needs valid token |

## Still problematic after proxy fix (schema/config issues)

| Problem | Owner | Severity |
|---|---|---|
| `audit_events.actor` column missing → 500 on list/ingest | audit service | 🔴 High |
| `saved_searches` relation missing → 500 on GET | search service | 🔴 High — `services/search/migrations/000001` not applied against the shared `schema_migrations` (which is at v9 from document) |
| `webhook_subscriptions.active` column missing → 500 on GET /webhooks | connector service | 🔴 High |
| Document service returns 401 on /workspaces with session cookie | document service | 🟡 Medium — may require X-Tenant-ID assertion against authenticated user |
| Intelligence service not running | intelligence | 🟡 Medium — Python/Celery, start via `docker compose --profile app up` |

## Not covered in this pass

- **Browser walkthrough with Playwright** — would verify actual React render behaviour, devtools console errors, empty/error states, that auth rehydration works on refresh. Not done in this session because the primary fix (Vite proxy + null-hazard) unblocks *access*; render-time bugs are a separate sweep.
- **Full sweep of `.data.X.length` / `.data.X.map` without null guards** — subagent identified one ([VersionHistory.tsx:16](../../web/src/components/documents/VersionHistory.tsx#L16)); a dedicated grep across all `web/src/routes/**/*.tsx` for unguarded access is still owed.
- **Shape mismatch audit across the ~60 newly-reachable routes** — until they were reachable, their response shapes couldn't be compared against frontend types. With them reachable now, this is a natural next pass.
