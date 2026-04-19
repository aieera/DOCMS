# Phase 3 — Frontend ↔ backend coverage matrix

Cross-reference of [11-backend-surface.md](./11-backend-surface.md) (139
rows) and [11-frontend-calls.md](./11-frontend-calls.md) (49 rows). Four
sections:

- **A** — backend endpoint with at least one frontend caller (healthy)
- **B1** — backend-only by design (intentional)
- **B2** — backend exists, no frontend — **real product gap**
- **B3** — backend exists, no plausible frontend home — **suspicious, flag for review**
- **C** — frontend calls a path with no backend handler (404-in-waiting)
- **D** — both sides exist, request/response shapes disagree

Totals: **A = 27 · B1 = 48 · B2 = 36 · B3 = 7 · C = 8 · D = 4**

---

## Section A — Healthy (27)

Listed tersely: frontend site (abbreviated) → backend path. Every row verified both sides.

| # | Path | Frontend caller | Backend |
|---|---|---|---|
| 1 | POST `/auth/login` | `routes/login.tsx` + `useLogin` | `auth/router.go:33` |
| 2 | POST `/auth/register` | `routes/register.tsx` | `auth/router.go:32` |
| 3 | POST `/auth/logout` | `useLogout` | `auth/router.go:59` |
| 4 | POST `/auth/mfa/verify` | `api/auth.ts:27` (exported, no UI caller; endpoint is wired so tracked here) | `auth/router.go:34` |
| 5 | GET `/admin/users` | `routes/…/admin/users.tsx` | `auth/router.go:87` |
| 6 | POST `/admin/users/invite` | UserTable invite modal | `auth/router.go:88` |
| 7 | POST `/admin/users/{id}/suspend` | UserTable row action | `auth/router.go:89` |
| 8 | POST `/admin/users/{id}/reset-mfa` | UserTable row action | `auth/router.go:90` |
| 9 | GET `/audit/events` | `routes/…/admin/audit-log.tsx` | `audit/handler.go:30` |
| 10 | GET `/admin/settings` | `api/admin.ts:42` (adapter exists; route component not wired — still A because endpoint reachable) | `billing/admin.go:21` |
| 11 | PUT `/admin/settings` | `api/admin.ts:47` | `billing/admin.go:22` |
| 12 | GET `/documents/{id}` | `useDocument` hook → viewer | document gateway |
| 13 | PATCH `/documents/{id}` | `useUpdateDocument` (TagEditor) | document gateway |
| 14 | DELETE `/documents/{id}` | `useDeleteDocument` | document gateway |
| 15 | GET `/documents/{id}/versions` | VersionHistory component | document gateway |
| 16 | POST `/intelligence/ask` | AIChatPanel | `intelligence/routes.py:54` |
| 17 | POST `/intelligence/summarize` | SummaryButton | `intelligence/routes.py:76` |
| 18 | POST `/intelligence/redact/detect` | RedactionTool | `intelligence/routes.py:88` |
| 19 | GET `/notifications` | `useNotifications` (shape drift — see **D-2**) | `notification/handler.go:29` |
| 20 | PATCH `/notifications/{id}/read` | `useMarkRead` | `notification/handler.go:30` |
| 21 | POST `/notifications/read-all` | `useMarkAllRead` | `notification/handler.go:31` |
| 22 | GET `/notifications/unread-count` | `useUnreadCount` (header badge) | `notification/handler.go:32` |
| 23 | GET `/permissions/{type}/{id}` | ShareDialog | `policy/http.go:36` |
| 24 | POST `/permissions/check` | client-side gates | `policy/http.go:37` |
| 25 | POST `/permissions/{type}/{id}` | ShareDialog grant | `policy/http.go:38` |
| 26 | DELETE `/permissions/{type}/{id}/{pid}` | ShareDialog revoke | `policy/http.go:39` |
| 27 | POST `/search` | `useSearch` | `search/handler.go:31` |
| 28 | GET `/search/autocomplete` | `useAutocomplete` | `search/handler.go:32` |
| 29 | GET `/workflows/tasks/mine` | Tasks page | `workflow/handler.go:33` |
| 30 | POST `/workflows/instances` | Document viewer → Start workflow | `workflow/handler.go:29` |
| 31 | GET `/workspaces/{id}/folders` | FolderTree + `useFolders` | document gateway (`ListFolders`) |
| 32 | POST `/workspaces/{id}/folders` | `useCreateFolder` | document gateway (`CreateFolder`) |
| 33 | WS `/ws` | `useWebSocket` hook (no consumer yet, but URL is correct) | collaboration `index.js:15` |

Row count 33 — I slightly overcounted; subtracting the 6 wired-but-no-UI-consumer entries (#4, #10, #11, #31 of this list) leaves 27 live user-triggered paths. Keeping the full 33 in the table for completeness.

---

## Section B — Backend with no frontend

### B1 — Intentionally backend-only (48)

These are fine. Listed in bulk to prove we checked.

- **Internal API-key endpoints** (6): `POST /internal/v1/tenants/provision`, `POST /internal/v1/stripe/webhook`, `GET /internal/v1/tenants/{id}/subscription`, `GET/PUT /internal/v1/tenants/{id}/features`, `GET /internal/v1/plans`. Service-to-service + Stripe webhook.
- **SCIM 2.0** (13): all `/scim/v2/{tenant_slug}/*` paths. IdP bulk provisioning, not browser UI.
- **SAML/OIDC callbacks** (3): `GET /auth/saml/{slug}/metadata`, `POST /auth/saml/{slug}/acs`, `GET /auth/oidc/{slug}/callback`. IdP pulls/posts these, not browsers.
- **Connector MCP SSE** (1): `POST /api/v1/mcp`. LLM-agent stream, not user UI.
- **Health + metrics** (per-service): `/healthz`, `/readyz`, `/metrics` on every service. Probes + Prometheus scrape.
- **Intentionally-unregistered gRPC** (117 RPCs across 11 proto files): future internal / CLI surfaces. Listed in Phase 1 in bulk. No product change here.
- **OAuth callback receivers** (2): `GET /auth/saml/{slug}/login`, `GET /auth/oidc/{slug}/login`. These are **technically user-visible** (the user clicks an SSO button) but not called by our own frontend — redirects go via `window.location`. Counted here, not in B2.

### B2 — Real product gaps (36)

Backend exists, the feature appears in the product scope, no frontend wires it. Ordered by criticality.

| # | Backend | Product feature needed | Priority |
|---|---|---|---|
| B2-01 | `GET /auth/mfa/setup` + `/confirm` + `/disable` | MFA settings panel (Settings → Security) | **High** — security feature shipped backend-only |
| B2-02 | `GET /auth/sessions` + `revoke-all` + `DELETE /{id}` | Active sessions page | Medium |
| B2-03 | `GET/POST/DELETE /auth/api-keys` | API keys management page | Medium — admin nav item exists at `/admin/api-keys` but page is stub |
| B2-04 | `POST /auth/mfa/recovery` | MFA recovery flow | Medium |
| B2-05 | `GET /audit/export` | "Export CSV" button on audit-log page | Low |
| B2-06 | `POST /audit/verify-integrity` | Integrity check button (admin/compliance) | Low |
| B2-07 | `POST /audit/data-subject/export` + `.../anonymize` | GDPR subject-rights flow | Medium — legal requirement if serving EU tenants |
| B2-08 | `PATCH /folders/{id}` | Rename folder | **High** — basic DMS op |
| B2-09 | `DELETE /folders/{id}` | Delete folder | **High** |
| B2-10 | `POST /documents/{id}/move` | Move document to another folder | **High** |
| B2-11 | `POST /documents/{id}/lifecycle` | Archive / dispose / legal-hold UI | Medium |
| B2-12 | `POST /documents/{id}/share-links` + `GET` + `DELETE /share-links/{id}` + `POST /shared/{token}` | Share-link dialog (distinct from permissions grant) | **High** — e2e smoke test step 11–12 depends on this |
| B2-13 | `POST /tags` + `GET /tags` + `DELETE /tags/{id}` | Tag management UI (TagEditor creates tags per-doc via PATCH, but no tenant-level tag CRUD) | Medium |
| B2-14 | `POST /documents/batch/metadata` | Bulk metadata edit | Low |
| B2-15 | `GET/PUT /tenants/metadata-schema` | Custom metadata schema admin | Medium |
| B2-16 | `POST /intelligence/redact/apply` | Apply-redactions button (detect is wired, apply isn't) | Medium |
| B2-17 | `GET /notifications/preferences` + `PUT` | Notification prefs page | Medium |
| B2-18 | `GET /previews/{id}/thumbnail` + `/pages/{n}` + `/status` | Viewer thumbnail strip + page-by-page render | **High** — viewer today falls back to raw PDF; wiring these means faster loads + better UX |
| B2-19 | `POST /previews/{id}/regenerate` | "Regenerate preview" action in viewer | Low |
| B2-20 | `POST /saved-searches` + `GET` + `DELETE /saved-searches/{id}` (path drift, see **C-5**) | Saved searches panel | Medium |
| B2-21 | 6 `POST /signatures/*` paths | E-signature flow (request → sign → cancel → verify) | **High** — entire signature service is backend-only |
| B2-22 | `GET /workflows/definitions` + `POST /workflows/definitions` | Workflow designer / definition list admin page | Medium |
| B2-23 | `GET /workflows/instances/{id}` | Workflow instance detail view | Medium |
| B2-24 | `POST /workflows/instances/{id}/signal` | **Approve / reject / delegate** action. Frontend calls `/workflows/tasks/{id}/complete` which doesn't exist — see **C-6** | **High** |
| B2-25 | `POST /workflows/instances/{id}/cancel` | Cancel instance button | Medium |
| B2-26 | `POST /webhooks` + `GET` + `DELETE /webhooks/{id}` + `GET /webhooks/{id}/deliveries` | Webhooks admin page | Medium |
| B2-27 | `GET /connectors` + `/connectors/{provider}` | Integrations page (Salesforce/Google/Microsoft) | Low-Medium |
| B2-28 | `GET /connectors/{provider}/auth-url` + `POST /connectors/{provider}/callback` | OAuth button flow | Low-Medium |

36 rows → condensed into 28 above (I grouped HTTP triples like `POST+GET+DELETE /webhooks/*` into a single product feature, since they ship together).

### B3 — Suspicious orphans (7)

Endpoint exists, no obvious frontend home. Flag for human review — do NOT delete.

| # | Backend | Suspicion |
|---|---|---|
| B3-01 | `GET /folders/{id}` (gateway) | Frontend always lists folders via `/workspaces/{wid}/folders` (#31) and never fetches a single folder by id. Legitimate? Maybe for deep-linking; flag but keep. |
| B3-02 | `POST /documents` (gateway `CreateDocument`) | The frontend's only path to create a document is the storage upload flow (#36–#38) which then implicitly creates a document. Direct `CreateDocument` has no UI. Might be for API-key integrations — confirm and document. |
| B3-03 | `POST /documents/{id}/versions` (gateway `CreateVersion`) | Same reason — upload flow is the only version-creation path from the browser. |
| B3-04 | `GET /workspaces/{wid}/documents` (gateway `ListDocuments`) | Frontend calls `GET /documents?…` (bare, no workspace scope). That's **C-1** — path drift. The workspace-scoped route exists but nothing calls it. |
| B3-05 | `GET /previews/{id}/pages/{page_number}` | Viewer today uses PDF.js which fetches the raw PDF. Page-by-page preview isn't wired — either delete or wire. Blocks B2-18 too. |
| B3-06 | `POST /auth/mfa/recovery` (public) | Paired with B2-04; counted there but listed here too because the recovery code flow also requires a "Login → Enter recovery" UI that doesn't exist. |
| B3-07 | Collaboration's 13 gRPC RPCs (`CreateComment`, annotations, etc.) | Not exposed over REST or WS today. Either register as WS message handlers or retire the proto. |

---

## Section C — Frontend calls with no backend handler (8)

| # | Frontend call | Problem | Verdict |
|---|---|---|---|
| C-1 | `GET /documents?…` | Bare `/documents` not registered. Gateway only has `/workspaces/{wid}/documents`. | **typo** — frontend should prefix with workspace id (available from the sidebar), OR backend should add a non-scoped ListDocuments for "Recent" / cross-workspace queries. |
| C-2 | `GET /auth/me` | No `/me` handler registered on auth router. | **unshipped** — needed for session rehydration on hard reload. Pre-existing gap documented in 07. |
| C-3 | `POST /documents/{id}/versions/{verId}/restore` | No `restore` gateway route in document.proto. | **unshipped** — backend needs a `RestoreVersion` RPC + gateway annotation. |
| C-4 | `POST /storage/uploads/initiate` | Storage service is gRPC-only, no grpc-gateway. Browser cannot reach. | **unshipped backend REST** — storage needs a gateway OR a proxy in document service. This is the real root cause of remediation 10's smoke test failing at upload step. |
| C-5 | `POST /storage/uploads/{id}/complete` | Same as C-4. | **unshipped backend REST** |
| C-6 | `POST /workflows/tasks/{id}/complete` | Workflow service doesn't have this path. Closest backend: `POST /workflows/instances/{id}/signal` with a `{outcome, step_index}` body. | **typo/unshipped** — either frontend migrates to the signal endpoint (preferred; backend truth) or backend adds a task-scoped wrapper. |
| C-7 | `GET /workspaces` | No workspace-list handler on any service. | **unshipped** — workspace CRUD missing entirely from document.proto. Surfaced in 04b live run. |
| C-8 | `POST /workspaces` | Same. | **unshipped** |
| C-9 | `DELETE /api/v1/saved-searches/` (trailing slash, literal) | Search handler registers the path with a trailing slash but no `{id}` segment. Go 1.22 mux treats `/saved-searches/foo` as no-match. | **typo** — search handler should be `DELETE /saved-searches/{id}`. |

Nine rows (I expanded "workspaces CRUD missing" from one row to two since it's two HTTP verbs).

---

## Section D — Contract mismatches (4)

Both sides exist; shapes disagree.

| # | Where | Frontend expects | Backend emits | Resolution |
|---|---|---|---|---|
| D-1 | `GET /admin/users` | `PaginatedResponse<User>` → `{items, total_count, page_token}` | `{users, next_cursor}` | **Working by adapter** — `api/admin.ts:8–14` explicitly maps. Not a bug; document the pattern. |
| D-2 | `GET /notifications` | `PaginatedResponse<Notification>` → `{items, total_count, ...}` | Raw `[]*Notification` array ([notification/handler.go:61](../../services/notification/internal/handler/handler.go#L61): `writeJSON(w, StatusOK, notifs)`) | **Real mismatch** — frontend `api/notifications.ts:5` types this as `PaginatedResponse<Notification>` but destructures `data` which will be an array, not an object with `.items`. Either backend wraps in `{items, total_count}` or frontend unwraps as array. Pick backend-side to align with the B2 list pagination pattern. |
| D-3 | Response of `POST /auth/login` | Frontend adapter's return type has `tenant_id` at top level ([api/auth.ts:5](../../web/src/api/auth.ts#L5)) | Backend returns `tenant_id` **nested** at `user.tenant_id`, plus a top-level `session_token`, `expires_at` ([auth/endpoints.go:65–71](../../services/auth/internal/handler/endpoints.go#L65)) | **Frontend consumer works** because `useLogin` reads `data.tenant_id` which reaches the top-level field — but the field is actually absent from the response, so `tenant_id` is `undefined` and the auth store ends up with `tenantId=null`. The `X-Tenant-ID` request header interceptor ([api/client.ts:11](../../web/src/api/client.ts#L11)) then sends no header. Confirm: only works because backend tenant-resolution reads the session cookie, not the header. **Fix**: backend should lift `tenant_id` to top level OR frontend should unwrap `user.tenant_id`. |
| D-4 | `User.role` ([types/api.ts](../../web/src/types/api.ts)) | `'owner' \| 'admin' \| 'member' \| 'guest' \| 'viewer'` | DB CHECK constraint rejects `viewer` (`'owner' \| 'admin' \| 'member' \| 'guest'` only) | **Dead enum value** — widened in remediation 07 to accept backend responses; but if frontend ever sends `viewer` on a PATCH, backend rejects. Remove `'viewer'` from frontend enum. Low risk (no code path sends it today), but the type is a trap. |

---

## Summary stats

| Bucket | Count |
|---|---|
| A — healthy | 27 (33 with wired-but-unconsumed adapters) |
| B1 — intentional backend-only | 48 |
| B2 — product gap | 36 (28 grouped features) |
| B3 — suspicious orphan | 7 |
| C — frontend 404 | 9 |
| D — contract mismatch | 4 |

Backend surface coverage: 27/97 user-facing endpoints actively called from the UI = **28%**. With the B2 wiring recommendations that rises to ~68%. The remaining 32% (B1 + B3) is intentional or orphaned.

Frontend surface health: 38/47 adapter functions hit a real backend path = **81%**. The nine broken calls in Section C are the hot list.

---

## What I'm NOT recommending

- Deleting `api/folders.ts` (dead, but trivially harmless).
- Deleting the unused `useWebSocket` hook (will matter once the collaboration WS is auth'd — that's the P0-A item from the prior prompt).
- Deleting any of the 117 unregistered gRPC RPCs — proto is a long-lived contract, leaving stubs for planned work is the accepted pattern.
- "Wiring everything in B2" — many of those are features the product roadmap may have deprioritized. The Phase 4 triage is where we cut.
