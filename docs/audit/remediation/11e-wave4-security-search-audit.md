# Remediation 11e — Wave 4: security settings, API keys, saved searches, audit CSV

**Date:** 2026-04-17
**Source finding:** [11-coverage-matrix.md](../11-coverage-matrix.md) B2-01,
B2-02, B2-03, B2-05, B2-20. All UI-only — backends were already shipped;
the frontend just didn't call them.

## What shipped

### API adapters (new files)

- [web/src/api/security.ts](../../../web/src/api/security.ts) — MFA
  setup/confirm/disable, session list/revoke/revoke-all, API-key
  list/create/revoke. Types for `MFASetupResult`, `SessionSummary`,
  `APIKey`, `APIKeyIssued` mirror the backend payloads exactly.
- [web/src/api/savedSearches.ts](../../../web/src/api/savedSearches.ts)
  — list/create/delete; list tolerates both raw-array and
  `{saved_searches: […]}` wrappers.

### API adapter extension

- [web/src/api/admin.ts](../../../web/src/api/admin.ts) —
  `exportAuditCSV()` downloads `/audit/export` as a blob and triggers
  a browser save (creates `<a>`, revokes blob URL on return).

### Hooks

- [web/src/hooks/useSecurity.ts](../../../web/src/hooks/useSecurity.ts)
  — `useSetupMFA`, `useConfirmMFA`, `useDisableMFA`, `useSessions`,
  `useRevokeSession`, `useRevokeAllOtherSessions`, `useAPIKeys`,
  `useCreateAPIKey`, `useRevokeAPIKey`. Each mutation invalidates the
  relevant TanStack Query key.
- [web/src/hooks/useSavedSearches.ts](../../../web/src/hooks/useSavedSearches.ts)
  — `useSavedSearches`, `useCreateSavedSearch`,
  `useDeleteSavedSearch`.

### Pages

- [web/src/routes/_authenticated/admin/api-keys.tsx](../../../web/src/routes/_authenticated/admin/api-keys.tsx)
  — replaced the stub with a full list + create dialog. Create flow
  shows the plaintext token once with a copy button + "won't be shown
  again" warning; revoke has a confirm guard.
- [web/src/routes/_authenticated/admin/settings.tsx](../../../web/src/routes/_authenticated/admin/settings.tsx)
  — replaced the stub with two cards:
  - **MFA card**: enable flow shows the `otpauth://` URI (for pasting
    into an authenticator app), manual secret, recovery codes ("save
    now — shown once"), and a TOTP confirm step. Disable flow asks for
    a current TOTP code.
  - **Sessions card**: lists every active session with device/IP/last
    active; highlights the current device; provides per-session revoke
    and a "Sign out other devices" bulk action.
- [web/src/routes/_authenticated/admin/audit-log.tsx](../../../web/src/routes/_authenticated/admin/audit-log.tsx)
  — added an "Export CSV" button in the PageHeader actions slot.
  Calls `exportAuditCSV()` which streams the CSV and triggers a
  download.
- [web/src/routes/_authenticated/search.tsx](../../../web/src/routes/_authenticated/search.tsx)
  — added a **Save** button next to the search input and a chip row of
  saved searches below it. Clicking a chip reloads the query; clicking
  its X deletes the saved search.

## Backend endpoints actively called after this wave

These were 0% of the matrix's B2 list before; now wired:

- `GET /auth/sessions`, `POST /auth/sessions/revoke-all`,
  `DELETE /auth/sessions/{id}` (B2-02)
- `GET/POST/DELETE /auth/api-keys` (B2-03)
- `POST /auth/mfa/setup`, `POST /auth/mfa/confirm`,
  `POST /auth/mfa/disable` (B2-01)
- `GET /audit/export` (B2-05)
- `POST/GET/DELETE /saved-searches[…]` (B2-20)

## What's explicitly NOT in this wave

- **MFA recovery-code login UI** (B2-04) — the `/auth/mfa/recovery`
  endpoint is already wired; the UI flow that lets a user enter a
  recovery code on the login page if they lost their authenticator is
  a separate login-route change. Leaving for a follow-up.
- **Saved-search notifications** — the backend model has `notify` +
  `notify_interval_minutes` fields; the UI saves with defaults only.
  The notify toggle belongs to a richer saved-search editor.
- **API-key scopes UI** — the create dialog hardcodes
  `['documents:read', 'documents:write']`. Real scope selection is a
  follow-up; the backend accepts an arbitrary string slice today.
- **Date-range filter for audit export** — the CSV endpoint accepts
  `start_at` / `end_at` params but the current button exports the full
  visible range. Add a date picker when you redesign the audit page.

## Verification

Each page was built from the backend's documented response shape
without re-testing against a live stack. Manual smoke path once the
services are up:

```bash
# 1. Settings: enable MFA
#    → expect setup dialog with qr_code_uri, secret, 8 recovery codes
#    → confirm with code, dialog closes, user.mfa_enabled flips true

# 2. Settings: sessions
#    → expect current device flagged, other devices listed

# 3. API keys
#    → create "ci-test" → modal shows plaintext key once
#    → reopens page → key shows only key_prefix

# 4. Search → Save → chip appears → click chip reloads query
#    → click X removes

# 5. Admin → Audit log → Export CSV → file downloads as audit_log.csv
```

## Wave 4 scorecard

- B2-01 MFA panel ✅
- B2-02 Sessions page ✅
- B2-03 API keys page ✅
- B2-05 Audit CSV export ✅
- B2-20 Saved searches UI ✅

Five B2 features closed. Six+ remain (signature, preview thumbnails,
workflow designer, connectors/SSO config UI, tenant metadata schema,
notification preferences, retention/legal-hold UI, tags CRUD,
webhooks admin, redact-apply, GDPR data-subject export, audit
integrity verify, bulk metadata edit, MFA recovery login flow) — each
needs a scope discussion before building.
