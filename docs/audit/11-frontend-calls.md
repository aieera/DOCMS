# Phase 2 — Frontend call-site inventory

**Scope:** every outbound HTTP / WebSocket / raw fetch from `web/src/`.
Grepped adapters under `web/src/api/`, every `useQuery` / `useMutation`
in `web/src/hooks/`, direct `fetch()` / `axios()` calls elsewhere, and
`new WebSocket()` / `new EventSource()` sites.

Row count: **47 HTTP calls + 1 WebSocket + 1 raw fetch = 49**. All paths
are relative to the axios client's `baseURL` of `/api/v1`
([api/client.ts:9](../../web/src/api/client.ts#L9)).

Every row resolves through the axios instance with `withCredentials:true`;
the HttpOnly `dms_session` cookie is sent automatically, plus
`X-Tenant-ID` when `authStore.tenantId` is set.

The `Feature` column is the user-facing route / component that triggers
the call — found by following the hook → component → route import chain.

---

## Auth flows (`api/auth.ts` + `hooks/useAuth.ts`)

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 1 | POST `/auth/login` | [api/auth.ts:5](../../web/src/api/auth.ts#L5) | `useLogin` mutation ([hooks/useAuth.ts:11](../../web/src/hooks/useAuth.ts#L11)) + [routes/login.tsx:21](../../web/src/routes/login.tsx#L21) | Login page |
| 2 | POST `/auth/register` | [api/auth.ts:13](../../web/src/api/auth.ts#L13) | [routes/register.tsx:18](../../web/src/routes/register.tsx#L18) | Register page |
| 3 | GET `/auth/me` | [api/auth.ts:18](../../web/src/api/auth.ts#L18) | `getCurrentUser()` — **zero callers** (dead code) | n/a |
| 4 | POST `/auth/logout` | [api/auth.ts:23](../../web/src/api/auth.ts#L23) | `useLogout` ([hooks/useAuth.ts:23](../../web/src/hooks/useAuth.ts#L23)) | Header logout button |
| 5 | POST `/auth/mfa/verify` | [api/auth.ts:27](../../web/src/api/auth.ts#L27) | `verifyMFA()` — **zero callers** (dead) | n/a |

## Admin (`api/admin.ts`)

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 6 | GET `/admin/users?…` | [api/admin.ts:8](../../web/src/api/admin.ts#L8) | [routes/…/admin/users.tsx:3](../../web/src/routes/_authenticated/admin/users.tsx#L3) | Admin → Users list |
| 7 | POST `/admin/users/invite` | [api/admin.ts:17](../../web/src/api/admin.ts#L17) | UserTable component (invite modal) | Admin → Users → Invite |
| 8 | POST `/admin/users/{id}/suspend` | [api/admin.ts:27](../../web/src/api/admin.ts#L27) | UserTable (row action) | Admin → Users row menu |
| 9 | POST `/admin/users/{id}/reset-mfa` | [api/admin.ts:31](../../web/src/api/admin.ts#L31) | UserTable (row action) | Admin → Users row menu |
| 10 | GET `/audit/events?…` | [api/admin.ts:37](../../web/src/api/admin.ts#L37) | [routes/…/admin/audit-log.tsx:3](../../web/src/routes/_authenticated/admin/audit-log.tsx#L3) | Admin → Audit log |
| 11 | GET `/admin/settings` | [api/admin.ts:42](../../web/src/api/admin.ts#L42) | `getTenantSettings()` — no route import found | Admin → Settings (not yet wired) |
| 12 | PUT `/admin/settings` | [api/admin.ts:47](../../web/src/api/admin.ts#L47) | `updateTenantSettings()` — no caller | Admin → Settings (not yet wired) |

## Documents (`api/documents.ts` + `hooks/useDocuments.ts`)

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 13 | GET `/documents?…` | [api/documents.ts:5](../../web/src/api/documents.ts#L5) | `useDocuments` hook | Workspace → document list grid/table |
| 14 | GET `/documents/{id}` | [api/documents.ts:10](../../web/src/api/documents.ts#L10) | `useDocument` hook | Document viewer page |
| 15 | PATCH `/documents/{id}` | [api/documents.ts:15](../../web/src/api/documents.ts#L15) | `useUpdateDocument` (TagEditor) | Tag / metadata editing |
| 16 | DELETE `/documents/{id}` | [api/documents.ts:20](../../web/src/api/documents.ts#L20) | `useDeleteDocument` | Document row → Delete |
| 17 | GET `/documents/{id}/versions` | [api/documents.ts:24](../../web/src/api/documents.ts#L24) | VersionHistory component | Document viewer → Versions |
| 18 | POST `/documents/{id}/versions/{verId}/restore` | [api/documents.ts:29](../../web/src/api/documents.ts#L29) | `restoreVersion()` — no direct caller surfaced in grep | Version history → Restore |

## Folders (`api/folders.ts`) — **duplicates `api/workspaces.ts`**

`hooks/useFolders.ts` actually imports from `api/workspaces`, not `api/folders`. The four functions below are dead-code duplicates.

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 19 | GET `/workspaces/{id}/folders?…` | [api/folders.ts:6](../../web/src/api/folders.ts#L6) | **no imports** | dead |
| 20 | POST `/workspaces/{id}/folders` | [api/folders.ts:11](../../web/src/api/folders.ts#L11) | **no imports** | dead |
| 21 | PATCH `/workspaces/{id}/folders/{fid}` | [api/folders.ts:16](../../web/src/api/folders.ts#L16) | **no imports** | dead |
| 22 | DELETE `/workspaces/{id}/folders/{fid}` | [api/folders.ts:21](../../web/src/api/folders.ts#L21) | **no imports** | dead |

## Intelligence (`api/intelligence.ts`)

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 23 | POST `/intelligence/ask` | [api/intelligence.ts:5](../../web/src/api/intelligence.ts#L5) | AIChatPanel component | Document viewer → AI chat |
| 24 | POST `/intelligence/summarize` | [api/intelligence.ts:10](../../web/src/api/intelligence.ts#L10) | SummaryButton component | Document viewer → Summarize |
| 25 | POST `/intelligence/redact/detect` | [api/intelligence.ts:15](../../web/src/api/intelligence.ts#L15) | RedactionTool component | Document viewer → Redact |

## Notifications (`api/notifications.ts` + `hooks/useNotifications.ts`)

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 26 | GET `/notifications?…` | [api/notifications.ts:5](../../web/src/api/notifications.ts#L5) | `useNotifications` | Header bell → notification list |
| 27 | PATCH `/notifications/{id}/read` | [api/notifications.ts:10](../../web/src/api/notifications.ts#L10) | `useMarkRead` | Notification row click |
| 28 | POST `/notifications/read-all` | [api/notifications.ts:14](../../web/src/api/notifications.ts#L14) | `useMarkAllRead` | "Mark all read" button |
| 29 | GET `/notifications/unread-count` | [api/notifications.ts:18](../../web/src/api/notifications.ts#L18) | `useUnreadCount` | Header bell badge |

## Permissions (`api/permissions.ts`) — all from 04b

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 30 | GET `/permissions/{type}/{id}` | [api/permissions.ts:16](../../web/src/api/permissions.ts#L16) | ShareDialog component (list existing grants) | Document viewer → Share dialog |
| 31 | POST `/permissions/check` | [api/permissions.ts:21](../../web/src/api/permissions.ts#L21) | client-side gate checks | any permission-gated button |
| 32 | POST `/permissions/{type}/{id}` | [api/permissions.ts:36](../../web/src/api/permissions.ts#L36) | ShareDialog (grant flow) | Document viewer → Share |
| 33 | DELETE `/permissions/{type}/{id}/{principal_id}` | [api/permissions.ts:46](../../web/src/api/permissions.ts#L46) | ShareDialog (revoke) | Document viewer → Share |

## Search (`api/search.ts` + `hooks/useSearch.ts`)

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 34 | POST `/search` | [api/search.ts:5](../../web/src/api/search.ts#L5) | `useSearch` | Search page submit |
| 35 | GET `/search/autocomplete?q=…` | [api/search.ts:10](../../web/src/api/search.ts#L10) | `useAutocomplete` | Command palette + search bar typeahead |

## Upload (`api/upload.ts` + `hooks/useUpload.ts`)

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 36 | POST `/storage/uploads/initiate` | [api/upload.ts:9](../../web/src/api/upload.ts#L9) | `useUpload` → DocumentUpload | Drag-drop upload |
| 37 | PUT `<presigned S3 URL>` (raw axios) | [api/upload.ts:16](../../web/src/api/upload.ts#L16) | DocumentUpload | Drag-drop upload — bypasses our API |
| 38 | POST `/storage/uploads/{id}/complete` | [api/upload.ts:25](../../web/src/api/upload.ts#L25) | DocumentUpload | Drag-drop upload (commit) |

## Workflows (`api/workflows.ts`)

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 39 | GET `/workflows/tasks/mine` | [api/workflows.ts:4](../../web/src/api/workflows.ts#L4) | Tasks page | Sidebar → Tasks |
| 40 | POST `/workflows/tasks/{id}/complete` | [api/workflows.ts:9](../../web/src/api/workflows.ts#L9) | Tasks page row action | Tasks → Approve/Reject |
| 41 | POST `/workflows/instances` | [api/workflows.ts:14](../../web/src/api/workflows.ts#L14) | Document viewer → Start workflow | Document viewer |

## Workspaces (`api/workspaces.ts` + `hooks/useWorkspaces.ts` / `useFolders.ts`)

| # | Method + path | Adapter call | Consumer | Feature |
|---|---|---|---|---|
| 42 | GET `/workspaces` | [api/workspaces.ts:5](../../web/src/api/workspaces.ts#L5) | WorkspaceSelector + `useWorkspaces` + routes/…/workspaces/index.tsx | Sidebar workspace picker + workspace list page |
| 43 | POST `/workspaces` | [api/workspaces.ts:10](../../web/src/api/workspaces.ts#L10) | `useCreateWorkspace` | "New workspace" modal |
| 44 | GET `/workspaces/{id}/folders` | [api/workspaces.ts:15](../../web/src/api/workspaces.ts#L15) | FolderTree + `useFolders` | Sidebar folder tree |
| 45 | POST `/workspaces/{id}/folders` | [api/workspaces.ts:22](../../web/src/api/workspaces.ts#L22) | `useCreateFolder` | Folder tree → New folder |

## WebSocket

| # | URL | Site | Consumer | Feature |
|---|---|---|---|---|
| 46 | `(ws|wss)://<origin>/ws` | [hooks/useWebSocket.ts:15](../../web/src/hooks/useWebSocket.ts#L15) | **zero callers** — hook is unused at the moment | (would be: collaboration real-time events) |

## Raw fetch

| # | URL | Site | Feature |
|---|---|---|---|
| 47 | fetch(url) — text viewer blob | [components/viewer/TextViewer.tsx:5](../../web/src/components/viewer/TextViewer.tsx#L5) | Document viewer renders plain-text blobs via a presigned GET URL that `/storage/download-url` would return |

---

## Dead or orphaned adapters (for Phase 3 bucket B3)

- `getCurrentUser()` — imports `/auth/me` but has no component that calls it. The auth store is populated solely from the login response; there's no "rehydrate on app mount" path. This is why a hard reload bounces you to `/login` (observed during 04b live run).
- `verifyMFA()` — imports `/auth/mfa/verify` but has no UI. MFA challenge flow is not implemented in the UI.
- `restoreVersion()` — exported, no caller surfaced in grep.
- `api/folders.ts` — entire file dead (superseded by equivalent functions in `api/workspaces.ts`).
- `useWebSocket` hook + its `/ws` connect — no component mounts it, so collaboration WS is never opened from the frontend today.

## Direct-route adapters (skip hooks)

The five callers below import adapter functions directly instead of going through a hook — fine by itself but worth noting so Phase 3 can spot any that drift away from the hook's caching:

- `routes/login.tsx` → `login()` (intentional — login is one-shot)
- `routes/register.tsx` → `register()` (same)
- `routes/…/admin/users.tsx` → `getUsers()` inside its own `useQuery`
- `routes/…/admin/audit-log.tsx` → `getAuditLog()` inside its own `useQuery`
- `routes/…/workspaces/index.tsx` → `getWorkspaces()` inside its own `useQuery`

The `useWorkspaces` + `useFolders` hooks exist but aren't consumed by any route component — Phase 3 will flag this as "hook exists, component calls adapter directly". Not a bug, but not the pattern.

---

## Shape expectations — to cross-check in Phase 3 Section D

Key type contracts the frontend assumes. Drift from what the backend returns surfaces as Section-D mismatches:

- **PaginatedResponse<T>** (`web/src/types/api.ts:116`): `{ items: T[]; total_count: number; page_token?: string }`. Backend admins return `{ users, next_cursor }` (04b explicitly adapts this). Verify every other paginated endpoint does the same dance.
- **User.role** (widened in remediation 07): `'owner' | 'admin' | 'member' | 'guest' | 'viewer'`. Backend allows `owner | admin | member | guest` only. `viewer` is frontend-only; if anyone sends it back, the backend CHECK constraint rejects.
- **login response**: frontend types `session_token` as optional, backend emits it; frontend types `tenant_id` at top-level but backend emits it nested at `user.tenant_id` (api-smoke.sh bug from remediation 10).
- **Permission grant** body: frontend sends `{ principal_type, principal_id, capability, expires_at? }` (04b aligned). Backend accepts same.
- **Search request**: frontend posts `{ query, …body }` as-is to `/search`. Payload shape lives in `web/src/types/api.ts` under `SearchQuery` / `SearchResult` — verify enum values match what OpenSearch indexer understands.

Phase 3 will generate a per-path diff against the backend model struct or proto message.

---

## Totals

- 47 REST/HTTP calls
- 1 WebSocket URL (currently unused at runtime)
- 1 raw `fetch()` (text-viewer blob fetch — not an API call)
- **5 adapter exports with no caller** (dead or orphaned)
- **Entire `api/folders.ts` file dead** — duplicated in `api/workspaces.ts`
