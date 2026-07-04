# SeDoc Google Workspace Add-on

Apps Script add-on (Gmail + Docs) — the Google equivalent of the Office
add-ins in `addins/outlook` and `addins/word`. Governed by ADR 0116.

| Surface | Capability |
|---|---|
| Gmail (message context) | File the open email + attachments into a SeDoc workspace/folder (same ingest endpoint as the Outlook add-in) |
| Docs | Open a SeDoc document as a converted Google Doc; save it back as a new **conflict-checked** version (`base_version_id`); insert an internal or shareable link at the cursor |
| Everywhere | Browse/pick workspace + folder; "Open in SeDoc" deep links |

## Architecture

- **UI**: CardService JSON cards (no HTML sidebar) — renders natively in
  Gmail/Docs on web and mobile.
- **Auth**: `ScriptApp.getIdentityToken()` → `POST /api/v1/auth/google/exchange`
  → SeDoc session bearer. The backend verifies the OIDC ID token against
  Google's JWKS, requires a verified email, and enforces the
  `SEDOC_GOOGLE_ALLOWED_HDS` hosted-domain allow-list. The session token is
  cached in `CacheService.getUserCache()` (Google-server-side, per-user,
  25 min) — never in any client-side storage (§8.1 / archtest C2).
- **Docs↔SeDoc link**: `PropertiesService.getUserProperties()` maps a
  Google Doc id to the SeDoc `(document_id, version_id)` it was opened
  from — the analogue of the Word add-in's CustomProperties stamp — so
  Save is version-aware and 409s instead of clobbering.

## Deploy

1. `npm i -g @google/clasp && clasp login`
2. Create an Apps Script project (`clasp create --type standalone`) and put
   its script id into `.clasp.json`, then `clasp push`.
3. In the GCP project backing the script: configure the OAuth consent
   screen and note the **OAuth client ID** — that is the ID-token `aud`.
4. Script Properties: set `SEDOC_API_BASE` to the SeDoc gateway origin.
   **Also add that origin (with a trailing `/`) to `openLinkUrlPrefixes` in
   `appsscript.json`** — Workspace add-ons refuse to open links whose URL
   isn't on that whitelist, so a base-URL change without a matching
   manifest entry breaks every "Open in SeDoc" button.
5. SeDoc auth service env:
   - `SEDOC_GOOGLE_AUDIENCE` = the OAuth client ID from step 3
   - `SEDOC_GOOGLE_ALLOWED_HDS` = comma-separated Workspace domains to trust
6. Deploy → Test deployments → install for your account, or publish to the
   Workspace Marketplace (internal) for the org.

Personal `@gmail.com` accounts are rejected by design (no `hd` claim).
Users must already exist in SeDoc — the exchange never auto-provisions.

## Files

| File | Role |
|---|---|
| `appsscript.json` | Manifest: scopes, Gmail contextual trigger, Docs homepage + file-scope triggers |
| `Config.gs` | `SEDOC_API_BASE` script-property config |
| `Auth.gs` | Identity-token → SeDoc session exchange + cache |
| `Api.gs` | REST client (same endpoints as the Office add-ins) |
| `Cards.gs` | Shared card widgets: workspace/folder pickers, notifications |
| `Gmail.gs` | File-email card + save action |
| `Docs.gs` | Open / version-aware save / insert-link cards |
| `Common.gs` | Non-contextual homepage |
