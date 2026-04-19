# Frontend API Inventory

**Generated:** 2026-04-19 · source: grep walk of `web/src/api/` + callsite scan.

## API client config ([web/src/api/client.ts](../../web/src/api/client.ts))

- **baseURL:** `/api/v1` (relative — resolved via the Vite dev proxy at :3000).
- **withCredentials:** `true` — sends the HttpOnly `dms_session` cookie.
- **Request interceptor:** adds `X-Tenant-ID`, `X-User-ID`, `X-User-Role` from the Zustand auth store; adds `X-CSRF-Token` on all mutating verbs (reads `dms_csrf` cookie — double-submit pattern).
- **Response interceptor:** 401 → `logout()` + redirect to `/login`; 403 → toast "Access denied"; 429 → toast "Rate limited"; 5xx → toast "Server error".

## 20 API modules in [web/src/api/](../../web/src/api/)

| Module | Functions | Notes |
|---|---|---|
| `auth.ts` | login, register, logout, getCurrentUser, verifyMFA | Token via cookie; no Bearer header |
| `admin.ts` | getUsers, inviteUser, suspendUser, resetMFA, getAuditLog, exportAuditCSV, getTenantSettings, updateTenantSettings | `getAuditLog` shape-massaged in client |
| `documents.ts` | getDocuments, getDocument, updateDocument, deleteDocument, moveDocument, getVersions, restoreVersion | |
| `groups.ts` | listGroups, getGroup, createGroup, updateGroup, deleteGroup, addGroupMember, removeGroupMember | **members normalized to [] this session** |
| `holds.ts` | listHolds, getHold, createHold, releaseHold | |
| `intelligence.ts` | askQuestion, summarizeDocument, detectRedactions | Target service not running |
| `notifications.ts` | getNotifications, markAsRead, markAllRead, getUnreadCount | |
| `permissions.ts` | getPermissions, checkPermission, grantPermission, revokePermission | |
| `permissionsMatrix.ts` | getPermissionMatrix | |
| `privacy.ts` | listDSR, getDSR, submitDSR, requestDSRToken | |
| `residency.ts` | getResidencyStats, listResidencyMigrations, createResidencyMigration | |
| `retention.ts` | listRetentionPolicies, getRetentionPolicy, createRetentionPolicy, updateRetentionPolicy, deleteRetentionPolicy | |
| `savedSearches.ts` | listSavedSearches, createSavedSearch, deleteSavedSearch | |
| `search.ts` | search, autocomplete | |
| `security.ts` | setupMFA, confirmMFA, disableMFA, listSessions, revokeSession, revokeAllOtherSessions, listAPIKeys, createAPIKey, revokeAPIKey | |
| `shareLinks.ts` | createShareLink, listShareLinks, deleteShareLink, accessShareLink | |
| `shareLinksAdmin.ts` | listAdminShareLinks, revokeAdminShareLink, revokeAllShareLinksForDocument | |
| `sso.ts` | listSSOConfigs, createSSOConfig, updateSSOConfig, deleteSSOConfig, validateSSOConfig | |
| `metadataSchema.ts` | getMetadataSchema, updateMetadataSchema | |
| `tags.ts` | (not read — likely CRUD for tags catalog) | |
| `upload.ts` | (not read — likely presigned-URL flow) | |
| `webhooks.ts` | (not read — CRUD for connector webhooks) | |
| `workspaces.ts` | getWorkspaces, getWorkspace, createWorkspace, updateWorkspace, deleteWorkspace, getFolders, createFolder, updateFolder, deleteFolder | |
| `workflows.ts` | (not read) | |
| `sso.ts` | (see above) | |

**~74 exported API functions across 20+ modules.**

## Direct `fetch()` outside the client layer
- [web/src/components/viewer/TextViewer.tsx:5](../../web/src/components/viewer/TextViewer.tsx#L5) — raw `fetch(url)` to an arbitrary URL (presigned preview). Bypasses CSRF, tenant headers, and the 401 interceptor.

## Null-guard audit at call sites

Defensively normalized at the API layer (safe): `listGroups`, `getGroup` (this session), `listHolds`, `listRetentionPolicies`, `listDSR`, `getResidencyStats`, `listResidencyMigrations`, `listAdminShareLinks`, `listSSOConfigs`, `listSavedSearches`, `getWorkspaces`, `getFolders`, `listSessions` (via `data.sessions ?? []`), `listAPIKeys` (via `data.api_keys ?? []`).

**Identified NULL HAZARD at a call site:**
- [web/src/components/documents/VersionHistory.tsx:16](../../web/src/components/documents/VersionHistory.tsx#L16) — `data?.map(...)` without guarding that `data` could be `undefined` during React-Query's pre-first-data render. Silent failure (renders nothing), not a crash.

**Other components worth future audit:** any useQuery consumer that `.map()`s `.data` directly without `??` — subagent flagged only the one above, but a follow-up grep over all `.data.` accesses in route files is warranted.
