# ADR 0089 — Native connectors (§12.4)

**Status:** Accepted (Google Workspace landed); other vendors pending.

**Date:** 2026-05-17

## Context

Blueprint §12.4 calls for native connectors to seven third-party systems
beyond the already-scaffolded Salesforce / Microsoft 365 / Google
provider classes:

- SAP ArchiveLink
- Google Workspace (Drive + Gmail import)
- ServiceNow (attach to incidents/cases)
- Workday (employee documents)
- SAP / NetSuite / QuickBooks / Xero (invoice + receipt matching)

The pre-existing scaffolding in `services/connector/internal/providers/`
had real OAuth code for Salesforce / M365 / Google but the surrounding
handler routes were stubs (`{"status":"stub"}`) and the repository's
SQL referenced columns that don't exist on the real
`connector_configs` table — meaning no connector could actually be
configured or used.

## Decision

Adopt the same per-tenant config model we built for eSign providers
(ADR 0071) and notification providers (Twilio / SMTP):

1. **One DB row per `(tenant_id, connector_type)`** in `connector_configs`.
   `config_encrypted` carries vendor `client_id` + `client_secret`
   sealed with a per-deployment key derived from `VAULTDMS_LOCAL_KEK`.
   `oauth_tokens_encrypted` carries the access + refresh tokens.
2. **HMAC-signed state** binds an OAuth round-trip to
   `(tenant_id, connector_type)`. Same format as the eSign callback:
   `<tenantID>.<connector>.<hmac>`. The state HMAC secret is derived
   from `VAULTDMS_LOCAL_KEK` with a different domain prefix so a leak
   of one key family cannot impersonate the other.
3. **One unified OAuth callback** at
   `GET /api/v1/connectors/oauth/callback` instead of one per provider.
   The handler recovers the connector from the state and dispatches
   to the right service method. This avoids registering a fresh
   redirect URI per provider in their respective developer consoles.
4. **Admin UI** lives at `/admin/integrations` → Connectors tab.
   Each vendor gets a card with Configure/Connect/Disconnect actions.
   Modal collects vendor credentials + shows the redirect URI to paste
   into the vendor portal.

## Today's slice

This ADR ships Google Workspace end-to-end through OAuth (config save →
authorize → token persisted with `account_id` + `base_uri` resolved
from userinfo). Drive/Gmail **import** is deferred to a follow-up — the
data path (DocumentClient.MaterialiseFile already exists from the email
ingestion work) needs a thin wrapper that lists files, downloads them,
and hands them off.

The other six vendors are placeholders on the Connectors tab.

## Migration sequence

- `000045_connector_configs_uniqueness.up.sql` adds the
  `(tenant_id, connector_type)` unique constraint the repo's
  `ON CONFLICT` clause requires.
- No new columns: the original `000001_initial_schema` already had
  `connector_type`, `display_name`, `config_encrypted`,
  `oauth_tokens_encrypted`, `is_active`, `last_sync_at`, `sync_status`.

## What's intentionally not in scope

- **Bi-directional sync.** Each connector pulls one-way until proven.
- **Rate-limit-aware** beyond per-call timeouts (no central token
  bucket yet).
- **Sync health dashboard** as a dedicated route. `sync_status` /
  `last_sync_at` exist on each row but are read inline on the
  Connectors tab today.
- **Playwright per connector.** Each vendor sandbox needs a
  pre-provisioned test account in CI — separate work.

## File map (Google slice)

| Path | Purpose |
|---|---|
| `services/document/migrations/000045_*` | Uniqueness constraint |
| `services/connector/internal/model/model.go` | `ConnectorConfig` aligned to real schema; `ProviderConfig` + `OAuthTokens` split |
| `services/connector/internal/repository/repository.go` | Repo SQL matches real columns; `wrapSealed` / `unwrapSealed` helpers |
| `services/connector/internal/service/google.go` | Save / Start / Callback / Disconnect |
| `services/connector/internal/handler/google.go` | REST handlers for Google + unified callback |
| `services/connector/internal/handler/handler.go` | Stub replacement: `getAuthURL` + `oauthCallback` dispatch |
| `services/connector/cmd/server/main.go` | `SetConnectorDeps` wiring (sealing key + HMAC + redirect URI) |
| `web/src/api/connectors.ts` | Typed client |
| `web/src/components/admin/GoogleWorkspaceModal.tsx` | Credentials modal |
| `web/src/routes/_authenticated/admin/integrations/index.tsx` | Connectors tab + Google card |
