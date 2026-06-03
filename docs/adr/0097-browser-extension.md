# ADR 0097 — Browser extension (Chrome / Firefox / Edge)

Status: Accepted (design + Phase 1 scaffold shipped; store submission deferred)
Date: 2026-05-19
Related: §17.7 of the blueprint; ADR 0067 (annotations), ADR 0091 (MCP),
ADR 0095 (license enforcement), ADR 0096 (Yjs collab).

## Context

Power users want SeDoc reachable from anywhere they're already working: a
quick search from any web page, drag-drop upload of files attached to emails,
"clip this page as a PDF into my workspace." The blueprint §17.7 calls for a
WebExtensions-API extension shipped to Chrome Web Store, Firefox Add-ons,
and Microsoft Edge Add-ons.

This ADR is the design plan, with **Phase 1 (scaffold + OAuth + quick search +
drag-drop + basic clipping)** shipped now in `extension/`. Gmail/Outlook
attachment scraping and store submissions are explicitly deferred — see
"Why phased" below.

## What is shipped now (Phase 1)

- `extension/` directory at repo root.
- `manifest.json` — Manifest V3, Chrome-first with `browser_specific_settings`
  shim for Firefox. Permissions limited to what each feature actually needs.
- `background.js` — service worker:
  - PKCE OAuth flow against the DMS auth service.
  - Token storage in `chrome.storage.local` (encrypted at rest by the
    browser's keychain on Chrome 116+).
  - Message bus between popup, content scripts, and background.
- `popup/` — quick-search popup (search-as-you-type against
  `/api/v1/search`), recent-docs list, "upload selection" action,
  "clip this page" action.
- `content/` — content script for the drag-drop overlay (shows on
  `dragenter`, hands the file off to the background via `chrome.runtime
  .sendMessage`).
- `clipping.js` — web clipping via `chrome.tabs.printToPDF` (Chromium-based
  browsers) with a fallback to `captureVisibleTab` + `<canvas>` → PDF for
  Firefox.
- `build/` scripts to produce a per-browser zip: `chrome.zip`,
  `firefox.zip`, `edge.zip`. Edge uses the Chrome bundle (Chromium-based).
- `README.md` covering local installation, dev workflow, and the
  not-yet-completed store submission checklist.

## What is **not** shipped now

- **Gmail attachment save.** Gmail's web UI is unstable enough that
  per-selector scraping breaks every few quarters when Google ships UI
  changes. Phase 2 will do this via the Gmail API (OAuth, list
  attachments by message ID) rather than DOM scraping, which is what
  the playbook entry's "save Gmail attachments" line really requires.
- **Outlook web attachment save.** Same shape: Phase 2 via Microsoft Graph
  API. The connector service (ADR 0089) already speaks Graph for M365 —
  the extension reuses that credential model.
- **Cross-browser test matrix.** Phase 1 was tested manually in Chrome.
  Firefox + Edge are MV3-compatible but need their own smoke runs.
- **Store submissions.** Chrome Web Store, Firefox Add-ons, and Edge
  Add-ons each require:
  - Developer account ($5 one-time for Chrome, free elsewhere)
  - Marketing assets (icons in 16/48/128, screenshots, promo tiles)
  - Privacy policy page hosted on a public URL
  - Permission justifications passed to each store's review
  - Review queue (Chrome: days, Firefox: hours-days, Edge: days)
  None of this can be done from a dev session — it's a release-management
  task with credentials and human review gates. Checklist is in the
  extension's README so the work is captured.

## OAuth flow (Phase 1, shipped)

Standard PKCE for the WebExtensions context, because the extension can't
hold a client secret:

```
  ┌─────────────────────────────┐         ┌─────────────────────────┐
  │ extension (background.js)   │         │ DMS auth service        │
  │                             │         │                         │
  │ 1. generate code_verifier   │         │                         │
  │ 2. SHA-256 → code_challenge │         │                         │
  │ 3. open auth URL in a tab:  │         │                         │
  │    /oauth/authorize?        │────────►│ 4. user logs in, grants │
  │      client_id=vaultdms-ext │         │    redirect with        │
  │      &code_challenge=...    │         │    ?code=<auth_code>    │
  │      &redirect_uri=         │◄────────│                         │
  │        chrome-extension:    │         │                         │
  │        //<id>/callback.html │         │                         │
  │                             │         │                         │
  │ 5. POST /oauth/token        │────────►│ 6. verify code +        │
  │    with code + verifier     │         │    code_verifier match  │
  │                             │◄────────│    → return tokens      │
  │ 7. chrome.storage.local     │         │                         │
  │    .set({ access_token,     │         │                         │
  │           refresh_token })  │         │                         │
  └─────────────────────────────┘         └─────────────────────────┘
```

**DMS auth service additions required for Phase 1.5:**
- `GET /api/v1/oauth/authorize` — interactive consent page.
- `POST /api/v1/oauth/token` — token exchange + PKCE verification.
- `oauth_clients` table with `client_id = vaultdms-ext` and
  redirect_uri allow-list (the dynamic `chrome-extension://<id>/`
  prefix per browser ID).

These don't exist yet. Phase 1 ships the extension-side flow with TODOs
in `background.js` and a mock callback that completes the dance against
the existing session cookie endpoint. Phase 1.5 wires the real OAuth
endpoints in the auth service.

## Quick search (Phase 1, shipped)

`popup.html` opens a search input wired to `/api/v1/search?q=...`. Hits
are listed with the document title, workspace, modified time, and a
deep link to the doc detail page (opens in the active tab). The search
runs through the same Bearer-token flow as every other authenticated
API call.

## Drag-drop upload (Phase 1, shipped)

`content/dragdrop.js` listens for `dragenter` on every tab, shows a
floating overlay when a file is dragged from the desktop OR from another
extension (e.g., Gmail download). On drop:

1. Content script reads file bytes via `FileReader`.
2. Posts to background via `chrome.runtime.sendMessage`.
3. Background calls `POST /api/v1/storage/uploads/initiate` with sha256,
   gets the upload session ID + presigned URL.
4. Streams the bytes to the presigned URL.
5. Calls `POST /api/v1/storage/uploads/{id}/complete`.
6. Calls `POST /api/v1/documents/{id}/versions` to create the version.
7. Shows a desktop notification on success with a "open in DMS" link.

Reuses the existing storage-proxy chain — no new backend code.

## Web clipping (Phase 1, shipped)

Two paths because Manifest V3's `chrome.tabs` API differs across vendors:

- **Chromium (Chrome + Edge):** `chrome.tabs.printToPDF()` returns a
  binary PDF blob directly. Upload via the same chain as drag-drop.
- **Firefox:** no `printToPDF` in MV3. Fallback: `captureVisibleTab` →
  scroll → capture → stitch via `<canvas>` → render via `jsPDF`. Quality
  is lower than native PDF but workable.

Both paths land the clipped PDF as a new document in the user's default
workspace. Default workspace is selectable via the popup settings.

## Gmail / Outlook attachment save (Phase 2, deferred)

The right design is **Gmail API + Microsoft Graph**, not DOM scraping:

```
  user clicks extension icon on a gmail.com tab
  → background.js reads tab URL, extracts thread/message ID
  → calls Gmail API /messages/{id}/attachments via existing M365
    OAuth tokens (provisioned by the connector service ADR 0089)
  → streams each attachment to DMS via the upload chain
```

DOM scraping the Gmail web UI is what the playbook entry literally
suggests, but Gmail's class names and DOM structure are obfuscated and
change with every web release. A 6-month half-life on a critical-path
feature isn't acceptable. The connector service already holds the
Google OAuth tokens for Drive/Gmail (ADR 0089 Phase 1 shipped); the
extension just needs to call those endpoints through a new connector
RPC.

Same shape for Outlook: the connector service speaks Microsoft Graph
already (M365 SharePoint pull — ADR roadmap entry); extending it for
Outlook attachments is a connector-side change, not extension-side.

## Manifest permissions (least privilege)

```jsonc
{
  "manifest_version": 3,
  "permissions": [
    "storage",          // chrome.storage.local for tokens
    "activeTab",        // tab content only when user clicks
    "scripting"         // inject content script on demand
  ],
  "host_permissions": [
    "https://<deployed-dms-host>/*"
  ],
  "optional_host_permissions": [
    "*://mail.google.com/*",     // Phase 2 Gmail
    "*://outlook.office.com/*"   // Phase 2 Outlook
  ]
}
```

`activeTab` + `scripting` instead of a broad content-script match means
the user has to actively trigger the extension on a page before it can
run code there. Cheaper review at the stores, smaller blast radius if
compromised.

## Token storage & refresh

`chrome.storage.local` is sandboxed per-extension and encrypted at rest
on Chrome 116+. Tokens never touch `localStorage` (no XSS exposure) or
`document.cookie` (no other extension can read it).

Refresh-token rotation: background.js maintains an `expires_at`
timestamp; ~60 s before expiry, calls `POST /api/v1/oauth/token` with
`grant_type=refresh_token`. Refresh token rotates each time; old one
revoked server-side.

## Store-submission checklist (Phase 3, deferred)

For each of Chrome Web Store, Firefox Add-ons, Edge Add-ons:

- [ ] Create developer account (Chrome: pay $5, others free)
- [ ] Icon assets: 16×16, 48×48, 128×128 (Chrome adds 16, 32, 48, 128)
- [ ] Promo tile: 440×280 small, 920×680 marquee, 1400×560 hero (Chrome only)
- [ ] At least 1 screenshot per store, 1280×800 or 640×400
- [ ] Privacy policy URL (publicly hosted)
- [ ] Permission justification text (each store has its own form)
- [ ] Submit + wait for review
- [ ] Plan for code-sign + auto-update flow per store

Cannot be done from a dev session — requires developer credentials and
human review queues.

## Open questions deferred to implementation

- Whether the extension should require a paid license (ADR 0095
  feature-flag) or be free with any tenant.
- Cross-tenant identity in the extension — if a user belongs to multiple
  tenants, popup needs a tenant switcher.
- Offline mode: should clipping queue locally when the DMS is unreachable
  and sync later?
- Telemetry: opt-in usage metrics for which features get used. Implies a
  privacy policy update.

## Why phased

A single-session "build the full thing including store submission" is
not deliverable:

1. Store submissions require credentials I don't have and human review I
   can't bypass.
2. Gmail/Outlook DOM scraping ages badly; the right design uses APIs that
   ride on existing OAuth tokens from the connector service.
3. Cross-browser smoke testing requires actually installing in Firefox
   and Edge — that's a workflow gate, not a code gate.

Phase 1 (this ADR) lands the scaffold + OAuth + quick search + drag-drop
+ clipping + per-browser build scripts. Subsequent phases land OAuth
backend, Gmail/Outlook via APIs, store assets, and submissions.
