# ADR 0065 — Co-authoring via OnlyOffice + Collabora (WOPI)

Date: 2026-05-08
Status: Accepted

## Context

VaultDMS already has a partial OnlyOffice integration today
([services/document/internal/handler/onlyoffice_handler.go](../../services/document/internal/handler/onlyoffice_handler.go)):

- `GET /api/v1/documents/{id}/versions/{vid}/onlyoffice/config` —
  emits a signed editor config the frontend hands to OnlyOffice's
  proprietary `DocsAPI.DocEditor` JS SDK.
- `POST .../onlyoffice/callback` — accepts OnlyOffice's status
  events; skeletal (logs but doesn't commit a new version).

§10.3 widens this to:

1. **WOPI** — the open protocol both OnlyOffice and Collabora speak.
   Switching to WOPI lets one backend support both editors without
   duplicating glue. Microsoft Office Online uses WOPI as well, so
   future Office Web integration is the same code path.
2. **Lock semantics** — required so two editors don't trample each
   other when they share a host but use different sessions
   (OnlyOffice manages co-edit internally; Collabora delegates lock
   ownership to the host via WOPI X-WOPI-Lock).
3. **PutRelativeFile** — "Save As" in the editor creates a new
   document instead of overwriting; the host decides where it lands.
4. **Discovery.xml** — Collabora needs `discovery.xml` to know which
   action URLs the host supports for which mime types.
5. **Permissions** wired through the policy service so a viewer
   can't trigger a `PutFile`.
6. **Audit** — start + end of every edit session are recorded in the
   audit log with actor + version + duration.

## Decision

### Surface

```
GET    /wopi/files/{file_id}                         — CheckFileInfo (JSON metadata)
GET    /wopi/files/{file_id}/contents                — GetFile (binary)
POST   /wopi/files/{file_id}                         — Lock | Unlock | RefreshLock | PutRelativeFile
                                                       (multiplexed via X-WOPI-Override header)
POST   /wopi/files/{file_id}/contents                — PutFile (binary, with X-WOPI-Lock match check)
GET    /wopi/hosting/discovery                       — discovery.xml for Collabora
```

`{file_id}` is the **version_id** UUID, not the document_id. Editors
should pin to a version so concurrent saves can't shift the document
out from under them; the discovery URL the frontend constructs is
`/wopi/files/{version_id}/contents`.

The existing `/onlyoffice/config` and `/onlyoffice/callback` paths
stay for the OnlyOffice-native protocol. Tenants pick one editor at
deploy time; the frontend defaults to WOPI when a Collabora server is
configured, OnlyOffice-native otherwise.

### Auth model

WOPI uses a per-session `access_token` query parameter. The host
issues it in the iframe URL and the editor echoes it back on every
WOPI call. We HMAC-sign the token with `VAULTDMS_WOPI_SECRET`:

```
access_token = HMAC-SHA256(secret, tenant_id + "|" + user_id + "|" + version_id + "|" + expires_at)
```

Stored claims: tenant id, user id, version id, expiry (default 1h),
permissions snapshot at issue time. The handler reads `tenant_id`
and `user_id` straight from the token rather than from gateway
headers — Collabora is an independent service that hits the WOPI
endpoints directly without the gateway.

### Lock semantics

WOPI locks are short strings the editor passes via `X-WOPI-Lock`.
We persist them in Redis under `wopi:lock:{file_id}` with a 30-min
TTL (the WOPI spec). Lock value is opaque to the host:

- **Lock**: `SETNX wopi:lock:{file_id} {value} EX 1800`. Conflict →
  409 + return the existing lock value in `X-WOPI-Lock`.
- **Unlock**: `DEL` only when supplied value matches.
- **RefreshLock**: `EXPIRE 1800` only when value matches.
- **PutFile**: requires the request's `X-WOPI-Lock` to match the
  stored value; mismatch → 409.

Locks survive auth-service restarts because Redis is shared.

### Permission model

The policy service decides:

- **Read-only preview** — any user with `read` on the document.
  CheckFileInfo response: `UserCanWrite=false`, `ReadOnly=true`.
- **Edit** — users with `edit`; the iframe loader requests an
  edit-mode access_token. Lock acquisition is implicit — the editor
  takes the lock on first key-press.
- **Co-edit** — multiple simultaneous editors. OnlyOffice's CRDT
  layer handles operational transformation; the host is just the
  blob store + lock arbiter. Co-edit count is the number of
  outstanding access_tokens for the same `file_id` with `Write=true`.

### Audit

Every WOPI session emits at least:

- `dms.coauth.session.started.v1` on first non-`CheckFileInfo` call
  with a given `(user_id, file_id)` pair. We track that in Redis at
  `wopi:session:{user_id}:{file_id}` with a 1h TTL; first miss = new
  session.
- `dms.coauth.session.ended.v1` on Unlock OR session-key expiry.

Both carry `actor_id`, `delegator_id` (when the user is acting
through a tenant-wide delegation rule from ADR 0064), `tenant_id`,
`document_id`, `version_id`, `started_at`, `ended_at`, `duration_s`.

### Editor selection

Per-tenant in `tenant_settings`:

- `coauth_provider`: `"onlyoffice"` | `"collabora"` | `"disabled"`
- `coauth_url`: base URL of the editor (e.g.
  `http://onlyoffice:8080` or `https://collabora.tenant.com`)

Frontend Edit-in-browser button hides itself when `disabled` and
falls back to "Open in desktop app" (download link) when the
configured editor URL is unreachable on a quick health probe.

## Consequences

- WOPI is verbose — five endpoints, multiplexed via header — but
  it's the only protocol both major editors speak. Worth the LOC
  cost to avoid forking per editor.
- Collabora is AGPLv3. Customers self-hosting Collabora must
  acknowledge the license terms in the EULA at tenant onboarding;
  tenant_settings carries an `agpl_acknowledged_at` timestamp that
  the admin UI surfaces before exposing the Collabora option.
- OnlyOffice on-prem is also AGPLv3 with a commercial alternative;
  same EULA gate.
- The existing `/onlyoffice/*` handler stays. Tenants that prefer
  the proprietary protocol (faster co-edit on big files) keep it;
  tenants that need Collabora flip the per-tenant setting.
- WOPI access_tokens are NOT session cookies — they leak via URL.
  Mitigations: 1h expiry, single-use HMAC, audit ties tokens to a
  user. Token rotation on permission revoke is a future ADR.

## Out of scope

- Office Web (Microsoft) integration — same WOPI surface but
  requires Microsoft 365 partner enrollment. Trivially layers on
  once a customer asks.
- DOCX → PDF conversion via OnlyOffice ConversionApi. Existing
  preview path uses a Python worker (`services/preview/`); not
  changed by this ADR.
- Real-time presence outside the editor (e.g. avatars in the
  document tree). The WOPI host doesn't see presence — only the
  editor knows who's typing — so this is a frontend-only feature.
