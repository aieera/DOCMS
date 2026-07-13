# SeDoc client add-ins — build & deployment config

Three add-ins share the "Save to / Open from SeDoc" surface:

| Add-in | Host | Manifest | Build |
|---|---|---|---|
| `outlook/` | Outlook (mail read) | `manifest.xml` (XML) | webpack → `dist/` |
| `word/` | Word (task pane) | `manifest.xml` (XML) | webpack → `dist/` |
| `google/` | Gmail + Docs (Apps Script) | `appsscript.json` | `clasp push` |

## What is baked vs. per-deployment

The **add-in identity** GUIDs are baked (stable, canonical) — they identify
the add-in package, not a deployment:

| Add-in | Canonical `<Id>` |
|---|---|
| Outlook | `2bcd772e-ec09-4254-bbb5-2919adab8523` |
| Word | `c244db56-3f2c-4da1-a797-ccb752981b0c` |

Everything genuinely **per-deployment** (an operator's Entra tenant / CDN /
Apps Script project) is a token, never committed as a literal:

| Token / field | What it is | Where |
|---|---|---|
| `${SEDOC_ADDIN_HOST}` | HTTPS host serving the static bundle (CDN in prod; `localhost:3001`/`3002` HTTPS in dev) | Office manifests |
| `${SEDOC_AAD_CLIENT_ID}` | Microsoft Entra (Azure AD) application (client) ID of the SeDoc API app registration, used for SSO | Office manifests |
| `${SEDOC_ADDIN_ID}` (optional) | override the canonical `<Id>` to sideload multiple envs on one machine | Office manifests |
| `${SEDOC_DOCS_HOST}` (optional) | docs host for the SupportUrl | Office manifests |
| `scriptId` | Apps Script project id | `google/.clasp.json` |
| `logoUrl`, `urlFetchWhitelist`, `openLink` host | the operator's SeDoc web host | `google/appsscript.json` |

### Office (Outlook / Word) — build-time substitution

`webpack.config.js` runs a `copy-webpack-plugin` transform
(`substituteManifestTokens`) that replaces `${VAR}` / `${VAR:-default}` from
the environment when copying `manifest.xml` into `dist/`. Tokens inside XML
comments are left verbatim (they document the tokens). In **production mode**
a token with no env value **and** no inline default fails the build — so a
store submission can never ship a literal `${SEDOC_AAD_CLIENT_ID}`.

```bash
cd addins/outlook   # or addins/word
export SEDOC_ADDIN_HOST=addin.acme.example.com
export SEDOC_AAD_CLIENT_ID=<your-entra-app-client-id>
npm ci
npm run build       # dist/manifest.xml has the real values; sideload it
```

### Google (Gmail / Docs) — Apps Script has no build step

`clasp push` uploads `appsscript.json` and the `.gs` files verbatim, so edit
these **before pushing** (or script it in your deploy):

- `.clasp.json` → `scriptId`: your Apps Script project id.
- `appsscript.json` → `logoUrl` + `urlFetchWhitelist` + the card `openLink`
  host: your SeDoc web host.

## Icons

Real brand icons live in `outlook/assets/` and `word/assets/`:
`icon.svg` (source) + `icon-{16,32,64,80,128}.png` (what the manifests
reference). Regenerate the PNGs from the SVG design with:

```bash
go run addins/tools/genicons.go addins/outlook/assets addins/word/assets
```

The Google add-on references a remotely-hosted logo
(`${SEDOC_ADDIN_HOST}/google/logo.png`); `google/assets/logo.svg` is the
source to publish there.

## Verification note (live host)

The add-in **UI paths** (the ribbon buttons, task panes, and CardService
cards) must be exercised in a real Office / Google host — sideload the
Outlook/Word manifests into Outlook/Word and deploy the Apps Script project
to Gmail/Docs. That step cannot run in CI (no Office/Google host), so it is a
manual pre-release check. What CI *does* cover: the bundles type-check and
build, the manifests validate + substitute cleanly, and the server side of
the "Save to SeDoc" flow — the m365 ingest now persisting real blobs +
versions — is proven by `TestM365Ingest_PersistsBlobsAndVersions`
(`services/document/internal/handler`, `-tags integration`).
