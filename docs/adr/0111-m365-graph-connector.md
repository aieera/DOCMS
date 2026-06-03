# ADR 0111 — Microsoft 365 / Graph connector

Status: Accepted (Phase 1 — OAuth + SharePoint/Drive surface
shipped; uploads >4 MB, app-only auth, and the Playwright e2e
deferred).
Date: 2026-05-19
Depends on: ADR 0089 (native connectors framework).

## Context

The blueprint groups Outlook, Word, Excel, PowerPoint, Teams, and
SharePoint behind one OAuth surface: Microsoft Graph. ADR 0089's
connector framework already supports per-tenant OAuth via
`connector_configs.config_encrypted` + a sealed
`oauth_tokens_encrypted` blob. What was missing was the M365-
specific provider, the scope set, the Graph API client, and the
admin UI to drive the handshake.

An earlier package at `services/connector/internal/providers/microsoft/`
shipped a minimal Connector covering PollInbox + SharePointListFiles
+ SendTeamsNotification with a smaller scope set and no 401-retry.
It's left in place behind the email poller until we deprecate it.
This ADR's package is `services/connector/internal/providers/m365/`
— shorter name + the canonical surface.

## What ships now (Phase 1)

- **`services/connector/internal/providers/m365/`** — OAuth shell
  (`m365.go`), Graph client (`client.go`), wire types (`types.go`).
  Methods: `ListSites`, `ListDriveItems`, `GetDriveItemContent`,
  `UploadDriveItem` (≤4 MB), `ListMessages`, `SendMail`,
  `PostChannelMessage`. All take an optional `actingAs` user GUID
  for delegated impersonation.
- **401-on-`invalid_token` retry**. Client.do() refreshes once on
  RFC 6750 `WWW-Authenticate: Bearer error="invalid_token"` and
  re-issues the request. Concurrent refreshes are serialised by
  the per-client mutex so we never trade one refresh_token for two.
  A `WithRefreshCallback` option re-seals the new tokens into
  `connector_configs.oauth_tokens_encrypted` so other processes
  see the fresh refresh_token.
- **Scopes**: `offline_access`, `User.Read`, `Files.ReadWrite`,
  `Mail.ReadWrite`, `Mail.Send`, `Sites.ReadWrite.All`,
  `Group.Read.All`, `ChannelMessage.Send`. `Calendars.ReadWrite`
  is an `OptionalCalendarScopes` array the connector adds on
  per-tenant opt-in.
- **Service layer** at `services/connector/internal/service/m365.go`
  mirrors the Google variant: `SaveM365Config`, `GetM365ConfigPublic`,
  `StartM365OAuth`, `HandleM365OAuthCallback`, `DisconnectM365`,
  plus convenience wrappers (`ListM365Sites`, `ListM365DriveItems`,
  `GetM365DriveItemContent`) that hide the Client-per-request
  pattern.
- **HTTP handlers** at `services/connector/internal/handler/m365.go`:
  - `PUT /api/v1/connectors/m365/config`
  - `GET /api/v1/connectors/m365/auth-url`
  - `POST /api/v1/connectors/m365/disconnect`
  - `GET /api/v1/connectors/m365/sites`
  - `GET /api/v1/connectors/m365/drives/{drive_id}/items`
  The unified `/api/v1/connectors/oauth/callback` now dispatches
  on the provider name embedded in the HMAC-signed state — the
  Google handler refactored to share the dispatcher rather than
  hardcode `HandleGoogleOAuthCallback`.
- **Admin UI**: `web/src/components/admin/M365ConnectorModal.tsx`
  mirrors `GoogleWorkspaceModal`, plus a Directory (tenant) ID field
  for single-tenant Entra apps. The Microsoft 365 tile on
  `/admin/connectors` opens the modal instead of hitting the stub
  `/auth-url` endpoint.
- **Howto** at `docs/howto/connectors-m365.md` — Azure app
  registration, redirect URI configuration, admin-consent steps,
  client-secret creation, troubleshooting common connect failures.

## Architecture notes worth defending

### Client-per-request, not Client-per-Service

`m365Client(ctx, tenantID)` builds a fresh Client on every HTTP
handler call. That's deliberate:

- The token blob may have been refreshed by another pod between
  requests. Re-reading `connector_configs` is the simplest way
  to pick that up without cross-pod cache invalidation.
- A 30ms Postgres read against `connector_configs` is cheap
  relative to the 200-500ms Graph round-trip that follows.
- A long-lived Client would have to grow a sync.Map keyed on
  tenant_id with a TTL + invalidation hook — code we'd revisit
  the first time Microsoft revokes a token mid-cache.

When the read becomes a profile hotspot, the optimisation is a
process-wide LRU keyed on `(tenant_id, oauth_tokens.AccessToken_sha256)`
so the cache invalidates automatically on refresh.

### Why dispatch on state, not on URL

The unified callback `/api/v1/connectors/oauth/callback` is the
same endpoint Google uses. The provider name lives in the HMAC-
signed state. That means:

- One redirect URI to register at every vendor — saves the admin
  pasting eight different URLs into eight different consoles when
  more providers land.
- The dispatch decision (= "which `HandleXOAuthCallback` to call")
  is verified by the HMAC, so a tampered state can't route through
  the wrong provider.
- Adding Salesforce later is a one-case-in-the-switch change in
  `connectorOAuthCallback`.

### `entra_tenant` lives in `ProviderConfig.Extra`, not its own column

Adding a typed column for every provider's idiosyncrasies bloats
the model and the migration count. `Extra map[string]string` is
free-form for vendor-specific knobs that don't deserve a column;
M365's `entra_tenant` is the first user. Salesforce's
`instance_url` already has a typed field because the connector
framework predates `Extra`; ideally that'd move into `Extra` too
in a future cleanup but it's not worth the churn today.

## What is NOT shipped (deferrals)

- **Uploads >4 MB**. Graph's chunked upload-session protocol is a
  separate state machine; the small-file PUT is enough for the
  Phase-1 SharePoint import path most buyers actually demo.
  `UploadDriveItem` rejects bigger payloads with a clear error.
- **App-only (daemon) auth**. Every token in this Phase is
  delegated-user. App-only would need separate Entra app
  configuration and a different scope set; the Connector struct's
  signature accepts an `actingAs` user GUID anticipating this but
  the OAuth path is delegated-only today.
- **Calendars.ReadWrite** in the default scope set. Listed as
  optional per the prompt; opt-in via the Connector's `ExtraScopes`
  field.
- **`PostChannelMessage` `actingAs` semantics**. Graph's
  `/teams/{}/channels/{}/messages` is delegated-by-the-bearer-token;
  we accept the parameter for API symmetry but ignore it.
- **Playwright e2e**. Mocking the Microsoft OAuth handshake end-to-
  end is intricate (multiple redirects, hash-fragment state, the
  consent screen). The connector's contract is exercised by the
  Go unit tests around the OAuth state HMAC + the 401-retry path.
  E2E lands when we have a vendor-supplied OAuth mock server we
  can point Playwright at.
- **The old `providers/microsoft` package**. Left in place because
  the email poller's interface points at it (lazily, via a backend
  field that nobody constructs today). When the email poller wires
  to `m365.Client.ListMessages` instead, we'll delete the old
  package.

## Verification

```sh
# Build the whole connector service:
cd services/connector && go build ./...   # clean

# Build the frontend:
cd web && npx tsc --noEmit                # clean

# End-to-end: configure a tenant, click Install, follow the redirect.
# After consent, /admin/connectors should show "Installed" + the
# /api/v1/connectors/m365/sites curl returns a list.
```

## Open questions deferred

- **Microsoft Information Protection (MIP) sensitivity labels**.
  Graph exposes them via `/me/security/labels`; surfacing them in
  SeDoc's metadata is its own ADR.
- **Per-drive permission preservation**. Today, copying a file
  into SeDoc doesn't replicate SharePoint's share permissions.
  That's intentional for v1 — SeDoc has its own permission model
  — but a future "preserve source ACLs" toggle has a real customer
  ask behind it.
- **Webhook subscriptions**. Graph supports change notifications
  via `/subscriptions`. Wiring it would replace our planned polling
  for new SharePoint files with real-time updates. Phase 2.
