# VaultDMS browser extension

Per ADR 0097 §17.7. Manifest V3, single bundle for Chrome / Edge / Firefox.

## Phase 1 features (shipped)

- DMS PKCE OAuth sign-in (requires backend Phase 1.5 — see below)
- Quick search popup against `/api/v1/search`
- Drag-drop / file-picker upload via the storage proxy chain
- "Clip this page" → PDF upload (Chromium native `tabs.printToPDF`)
- Options page for the DMS base URL

## Not yet shipped

- Gmail attachment save via Gmail API (Phase 2 — reuses connector
  service OAuth tokens)
- Outlook attachment save via Microsoft Graph (Phase 2)
- Firefox PDF clipping fallback (Firefox MV3 lacks `tabs.printToPDF`)
- Store submissions — needs developer credentials we don't have

## Backend dependency (Phase 1.5)

The extension's PKCE flow calls:

- `GET  /api/v1/oauth/authorize`
- `POST /api/v1/oauth/token`

Neither endpoint exists on the auth service yet. Until they land, the
extension's OAuth tab will 404. The shape both endpoints need to honour
is in ADR 0097 § OAuth flow. Until then, dev users can manually paste a
session cookie into `chrome.storage.local.vaultdms_session.access_token`
to exercise the rest of the extension surface.

## Local install (Chrome / Edge)

1. `chrome://extensions` → enable Developer mode (top right).
2. Click **Load unpacked** → choose this `extension/` folder.
3. Pin the VaultDMS icon to the toolbar.
4. Open the icon → set the DMS base URL via Options.
5. Click **Sign in** to start the OAuth flow (requires backend Phase 1.5).

## Local install (Firefox)

1. `about:debugging` → **This Firefox** → **Load Temporary Add-on**.
2. Choose `manifest.json` inside this folder.
3. Same steps from there.

Note: Firefox temp extensions reset on browser restart. For long-running
local testing, use `web-ext build` and install the signed `.xpi`.

## Build (per-browser zips)

```
node build.mjs
```

Produces `dist/chrome.files`, `dist/firefox.files`, `dist/edge.files` —
file manifests CI uses to drive `zip -r` over the sources. (We don't
ship a JS zip lib in this script to keep the build dep-free; CI calls
the system `zip` tool.)

## Store submission checklist

For each of Chrome Web Store, Firefox Add-ons, Edge Add-ons:

- [ ] Developer account ($5 one-time Chrome; free FF + Edge)
- [ ] Icons at 16×16, 48×48, 128×128 (placeholders in `icons/`; replace
      with branded assets before submission)
- [ ] Promo tiles (Chrome only — 440×280 small, 920×680 marquee, 1400×560
      hero)
- [ ] At least 1 screenshot at 1280×800 or 640×400
- [ ] Privacy policy URL — extension does not collect telemetry; the
      policy needs to declare that and explain why the host permissions
      list includes the DMS base URL
- [ ] Permission justifications:
  - `storage` — store OAuth tokens locally
  - `activeTab` + `scripting` — drag-drop overlay on user click
  - `notifications` — upload-success / failure notifications
  - `host_permissions` — talk to the user's DMS deployment
- [ ] Submit + wait for review (Chrome days, FF hours-days, Edge days)
- [ ] Set up auto-update channel per store

None of this is doable from a dev session — requires real credentials
and human review queues.

## File layout

```
extension/
├── manifest.json           — MV3, Chrome+FF+Edge
├── background.js           — service worker (OAuth, uploads, clipping)
├── callback.html           — OAuth redirect target
├── popup/                  — toolbar popup UI
│   ├── popup.html
│   ├── popup.css
│   └── popup.js
├── options/                — options page (DMS base URL)
│   └── options.html
├── content/                — content scripts (drag-drop overlay; Phase 2)
├── icons/                  — placeholder icons
├── build.mjs               — per-browser packager
└── README.md
```

## Security posture

- No client secret in the extension — PKCE only.
- Tokens live in `chrome.storage.local` (encrypted at rest on Chrome
  116+, sandboxed per-extension on all browsers).
- The DMS base URL is locked down per-install via Options + manifest
  `host_permissions`; the extension cannot egress to arbitrary origins.
- The drag-drop content script runs only on user-initiated activation
  via `activeTab` + `scripting` — no broad `content_scripts.matches`
  block.

ADR 0097 has the full design + the phased rollout for Gmail/Outlook
attachment save and store submissions.
