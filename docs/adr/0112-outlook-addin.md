# ADR 0112 — Outlook "Save to VaultDMS" add-in

Status: Accepted (Phase 1 — UI + SSO + ingest endpoint shipped;
blob upload + OCR + classification deferred to the version-upload
sweep that already gates the connector-email ingestion path).
Date: 2026-05-19
Depends on: ADR 0111 (M365 / Graph connector framework).

## Context

The highest-leverage M365 surface for VaultDMS adoption is "save
this email to VaultDMS from inside Outlook." It beats every other
M365 feature for two reasons:

1. **Daily-use frequency.** Office workers open Outlook all day,
   every day. A button that says "Save to VaultDMS" right next to
   "Reply" puts VaultDMS in front of the user constantly.
2. **No context switch.** The user never leaves Outlook. They
   don't open VaultDMS, navigate to a workspace, choose
   "Upload", drag-and-drop an `.eml` export. One click, two
   dropdowns, done.

ADR 0111's M365 connector solves the OAuth + Graph plumbing for
backend-driven flows. This ADR is the user-driven counterpart.

## What ships now (Phase 1)

- **`addins/outlook/`** — a self-contained React+TypeScript
  Office Web Add-in:
  - `manifest.xml` declaring a `MessageReadCommandSurface` button
    + a taskpane with SSO (`WebApplicationInfo`).
  - `src/taskpane/TaskPane.tsx` — Fluent UI dropdowns (workspace,
    folder), tag input, "Include attachments" switch, Save button.
  - `src/auth.ts` — exchanges the Entra ID token from
    `OfficeRuntime.auth.getAccessToken` for a VaultDMS session.
  - `src/api.ts` — thin REST client (`listWorkspaces`,
    `listFolders`, `ingestEmail`). 401 clears the session cache
    so the next attempt re-exchanges.
  - `webpack.config.js` — dev (HTTPS on :3001) + prod (static
    bundle to `dist/`).
- **`services/auth/internal/handler/m365_exchange.go`** +
  **`services/auth/internal/service/m365_exchange.go`** — the
  exchange endpoint. POST `/api/v1/auth/m365/exchange` validates
  the Entra token via Graph `/me`, looks up VaultDMS users by
  email, and issues a VaultDMS session token. Returns 404 when
  no user matches and 409 + a tenant-candidate list when the
  email exists in multiple tenants.
- **`services/document/internal/handler/m365_ingest.go`** — POST
  `/api/v1/integrations/m365/ingest-email`. Auth: VaultDMS
  session. Creates one parent document for the email + one child
  document per file attachment. Emits a
  `dms.m365.outlook.email.saved.v1` audit event via the outbox
  in the same tx that creates the documents.
- **Howtos**:
  - [docs/howto/outlook-addin-deploy.md](../howto/outlook-addin-deploy.md)
    — SRE-facing: build pipeline, manifest customization, Entra
    SSO setup, hosting options, M365 admin center publish.
  - [docs/howto/outlook-addin-sideload.md](../howto/outlook-addin-sideload.md)
    — end-user: per-platform install instructions + troubleshooting.

## Architecture decisions worth defending

### 1. Validate the Entra token via Graph `/me`, not by parsing JWKS

The exchange endpoint takes the Entra ID token and calls
`https://graph.microsoft.com/v1.0/me` with it as a bearer. The
read serves two purposes:

- **Validates** the token. Microsoft 401s if it's expired or
  revoked; we surface that 401 to the add-in so it re-acquires.
- **Resolves the user's email**, which is our lookup key into
  VaultDMS's users table.

The alternative — parse + verify the JWT in-process — would mean
plumbing JWKS endpoints + key rotation + per-Entra-tenant
audience handling into the auth service. The Graph call costs
~150 ms once per session; we cache the resulting VaultDMS
session for the taskpane's lifetime. Worth it.

### 2. Look up VaultDMS users by email, not by Entra tenant ID

Two reasons:

- **Customer reality**: many VaultDMS tenants invite users from
  email addresses that aren't in the customer's own Entra
  tenant (contractors, partners, etc.). Mapping Entra tenant ID
  to VaultDMS tenant ID would block these users.
- **The same email may exist in multiple VaultDMS tenants** —
  a contractor working at two customers, the same human's email
  in two unrelated VaultDMS instances. The exchange returns 409
  + a candidate list so the add-in can prompt the user.

The 0-match case is "ask your VaultDMS admin to invite you" —
we deliberately don't auto-provision. SCIM (ADR 0062) is the
right path for org-wide auto-provision.

### 3. Phase 1 doesn't persist bytes to MinIO

Matches the existing connector-email ingestion path
(`services/connector/internal/email/document_client.go`'s
deferred version-upload sweep). The handler accepts the
base64-encoded attachment payload on the wire — so the contract
is forward-compatible — but writes only the document rows + the
custom-metadata. OCR + classification will fire when the
version-upload sweep lands; until then the ingest is
"saved-but-pending" and the response carries `pending: true` so
the add-in can show that state.

This is the most controversial choice in this ADR. The honest
alternative — wiring StorageClient into the document service's
handler — needed work that exceeded this ADR's budget. The
sweep is one focused commit and unblocks both this path AND the
existing connector-email path simultaneously.

### 4. SSO scope is `User.Read` + OpenID — Graph data access is
###    NOT through the add-in's token

The add-in's WebApplicationInfo declares only what's needed to
identify the user (`openid`, `profile`, `email`, `User.Read`).
The bytes the add-in moves to VaultDMS come from
`Office.context.mailbox.item.getAttachmentContentAsync` — an
Office.js surface that doesn't need Graph scopes because Outlook
already has the message open. We don't ask the user to consent
to broader Graph scopes when they don't need to.

If we ever add a "Save full mailbox" bulk feature, that does
need broader Graph access — but that goes through the M365
connector (ADR 0111) which already has the scopes.

### 5. XML manifest, not the unified JSON manifest

JSON is Outlook-web + new-Outlook-only as of mid-2026. Mobile +
macOS classic + the legacy Windows client still require XML. We
ship XML now; we migrate when mobile + macOS catch up.

## What is NOT done (deferrals)

- **Compose-mode counterpart**. The mirror surface that lets you
  attach a VaultDMS document to a new email. Requires a separate
  manifest extension point + a different UI flow (paste a
  document link vs save the message). Lands when there's a
  customer ask.
- **MinIO blob persistence + OCR**. See architecture decision 3.
- **Item-attachment support**. Office distinguishes file
  attachments from "item" attachments (embedded MIME, calendar
  invites, etc.). The add-in skips non-file attachments in v1 —
  surfacing them would require a different ingestion shape and
  isn't worth the per-attachment code path until a real customer
  asks.
- **Cloud attachments**. OneDrive / SharePoint cloud
  attachments come down as a stub + a URL. Pulling the actual
  bytes needs the M365 connector's Drive client (ADR 0111). The
  add-in skips these in v1; the user gets a clear "skipped N
  cloud attachments" log line they can see in dev tools.
- **Playwright e2e**. Office add-ins can't be exercised by
  Playwright directly (Outlook is not a browser). E2E for this
  surface is a manual sideload + a saved test recording — the
  acceptance criterion is a green sign-off on the test plan in
  the sideload howto, not an automated check.

## Verification

```sh
# Backend builds clean:
cd services/auth     && go build ./...
cd services/document && go build ./...

# Add-in builds:
cd addins/outlook
npm install
npm run lint        # tsc --noEmit
npm run validate    # office-addin-manifest validate manifest.xml
npm run build       # dist/ ready for deploy

# Manual end-to-end (see sideload howto):
# 1. Sideload the add-in into Outlook.
# 2. Open a test email.
# 3. Click Save to VaultDMS.
# 4. Pick workspace + folder, click Save.
# 5. Verify a row appears in `documents` with email metadata
#    in custom_metadata, and an outbox row with event_type
#    = 'dms.m365.outlook.email.saved.v1'.
```

## Open questions deferred

- **Drag-and-drop attachment from VaultDMS into a draft email.**
  The natural counterpart to "Save to VaultDMS." Office's compose
  surface exposes `Office.context.mailbox.item.addFileAttachmentAsync`;
  wiring it to a VaultDMS document picker is its own ADR.
- **Per-tenant manifest customization.** Branding
  (icons, display name, support URL) currently lives in the
  manifest. A tenant who wants their logo would need a separate
  build today. Phase 2: a manifest-templating step at deploy time
  reads tenant config and emits a per-tenant manifest URL.
- **Tracking which emails came from the add-in vs the email
  ingestion connector**. The audit event distinguishes them
  (`dms.m365.outlook.email.saved.v1` vs `dms.email.ingested.v1`)
  but the document row itself doesn't carry a hint beyond
  `custom_metadata.email.source`. A `source` column or tag would
  let analytics queries split cleanly; left as Phase 2.
