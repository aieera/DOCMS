# Word add-in — deployment runbook (ADR 0113)

Audience: SRE shipping the "Open / Save back" Word add-in into a
production tenant. Pre-reqs: the VaultDMS backend's
`/api/v1/auth/m365/exchange` + `/api/v1/documents/{id}/versions`
endpoints already live (they shipped with ADRs 0112 + the
foundational document service), and an HTTPS host you can point
at static content.

Companion: [word-addin-sideload.md](./word-addin-sideload.md) for
the per-user install once this deploy is live.

If you've already deployed the Outlook add-in
([outlook-addin-deploy.md](./outlook-addin-deploy.md)) most of
this will look familiar — the two add-ins share the SSO app
registration, the build pipeline shape, and the M365 admin-center
publish flow. Differences worth flagging:

- **No Mail scopes**. The Word add-in's `<WebApplicationInfo>`
  asks only for `openid`, `profile`, `email`, `User.Read`. The
  bytes the add-in moves to/from VaultDMS come from Office.js's
  document-API, not Graph.
- **Different dev port**. Outlook uses :3001; Word uses :3002 so
  you can run both add-ins simultaneously on the same dev box.
- **`<CustomTab>` instead of MessageRead**. Word doesn't have a
  read-vs-compose split; a custom tab on the main ribbon is
  where the two buttons live.

---

## 1. Build the bundle

```sh
cd addins/word
npm install
npm run build         # → dist/
npm run validate      # → office-addin-manifest validate manifest.xml
```

`npm run validate` is mandatory before deploy. Office's manifest
parser is strict; a validation error here means every user's
Word silently fails to install the add-in.

The `dist/` directory after a successful build contains:
- `taskpane.html`  + bundled `taskpane.*.js`
- `commands.html`  + bundled `commands.*.js`
- `manifest.xml`   copied verbatim
- `assets/`        icons (16/32/64/80/128 PNG; see assets/README.md)

---

## 2. Edit the manifest for the environment

Three placeholders in `manifest.xml` MUST be replaced:

| Placeholder                                    | Replace with                                                                          |
|-----------------------------------------------|---------------------------------------------------------------------------------------|
| `<Id>00000000-0000-0000-0000-000000000113</Id>` | `uuidgen` — one per environment (dev / staging / prod).                       |
| `https://addin.vaultdms.example.com/word`      | The HTTPS host + path serving `dist/`. Must be HTTPS — Office refuses HTTP.   |
| `<Id>00000000-0000-0000-0000-000000000222</Id>` (in `<WebApplicationInfo>`) | The Application (client) ID of the Entra ID app used for SSO. |

The `<Resource>` line follows the `api://<host>/<client-id>` shape
and must match the **Application ID URI** configured in the Entra
app's "Expose an API" blade — the same one the Outlook add-in
uses, if you went with one shared app.

---

## 3. Entra SSO setup

If you've already done [outlook-addin-deploy.md §4](./outlook-addin-deploy.md#4-configure-the-entra-app-for-sso),
you're done — the same app registration covers Word.

If you're starting fresh OR want a sibling app so secrets rotate
independently:

1. Register the app (or use the existing one).
2. Under "Expose an API", set the Application ID URI to
   `api://<your-addin-host>/<client-id>` and add a scope
   `access_as_user`.
3. Pre-authorize the Office clients that ship Word add-ins:
   - `d3590ed6-52b3-4102-aeff-aad2292ab01c` — Microsoft 365 apps
     (Word desktop on Windows / Mac, Outlook desktop).
   - `bc59ab01-8403-45c6-8796-ac3ef710b3e3` — Outlook Web / OWA.
   - `08e18876-6177-487e-b8b5-cf950c1e598c` — Word for Web /
     Office.com.
   - `f8d98a96-0999-43f5-8af3-69971c7bb423` — Office mobile
     (iOS / Android).
4. Under "API permissions" add `openid`, `profile`, `email`,
   `User.Read` (delegated Microsoft Graph). Grant admin consent.

---

## 4. Host the static bundle

Same three patterns as the Outlook add-in:

### Option A — Frontend CDN

Drop `dist/` into a sibling subdirectory of the VaultDMS frontend
bucket (`/word/`) and you're done. The frontend's
`Access-Control-Allow-Origin: *` is what Office hosts expect.

### Option B — Dedicated S3 + CloudFront

```sh
aws s3 sync dist/ s3://vaultdms-word-addin-prod/ \
  --delete \
  --cache-control "public, max-age=300"
aws cloudfront create-invalidation \
  --distribution-id ABCDEF \
  --paths "/*"
```

Distribution requirements:
- HTTPS-only (HTTP → HTTPS redirect).
- `manifest.xml` served with `Content-Type: text/xml`.
- `Access-Control-Allow-Origin: *` on every response.

### Option C — Azure Static Website

If the customer prefers Azure-native, the Storage Account "Static
website" feature works the same way. Custom-domain + Microsoft-
managed cert; same headers.

---

## 5. Validate end-to-end before publishing

From a workstation:

```sh
# Reachability:
curl -I https://addin.vaultdms.example.com/word/manifest.xml
# 200, Content-Type: text/xml

curl https://addin.vaultdms.example.com/word/taskpane.html | head -20
# HTML with the office.js <script> tag

# Manifest validity against the LIVE URL:
npx office-addin-manifest validate https://addin.vaultdms.example.com/word/manifest.xml
```

Then sideload into your own Word + do a real Open + Save round-trip
(see the sideload howto for steps). After Save:

```sh
psql -c "SELECT id, document_id, version_number, change_summary, created_at
           FROM versions
          WHERE change_summary LIKE 'Saved from Microsoft Word add-in%'
          ORDER BY created_at DESC LIMIT 3;"
```

You should see a fresh row with the canonical change-summary text.
The audit trail comes from the existing `dms.version.uploaded.v1`
outbox event the document service publishes — bespoke
`dms.m365.word.*` events are deferred (see ADR 0113 §
"What is NOT shipped").

---

## 6. Publish to the M365 admin center

Identical flow to the Outlook add-in. From the M365 Admin Center:

```
Settings → Integrated apps → Upload custom apps
```

Upload the manifest XML or the hosted URL. Pick the deployment
audience (Just me → Specific users / groups → Entire org). Tenant-
wide propagation can take up to 24 hours; six is typical.

---

## 7. Co-authoring story (read this carefully)

The prompt asks for a "live co-author" hybrid path. Here's what
actually exists:

- **Word for Web** opens .docx files hosted at supported URLs in
  the SharePoint-style co-authoring engine, hosted by Microsoft.
  When VaultDMS serves the presigned URL with the right
  Content-Type, Word for Web opens it and lets multiple users
  edit simultaneously. Microsoft owns the conflict resolution.
- **Word desktop** does NOT do live co-authoring against arbitrary
  URLs — it requires SharePoint Online / OneDrive Business with
  the document already living there. For desktop opens via our
  add-in, the flow is "open → edit → click Save back to VaultDMS"
  which creates a new version. Conflicts (two users editing in
  parallel) surface as separate versions; the user picks the
  winner via the doc detail page's compare flow (ADR 0101).
- **The existing collaboration service (`services/collaboration/`)
  runs the Yjs CRDT path for the VaultDMS native viewer + the
  OnlyOffice integration (ADR 0096 + ADR 0065).** It does NOT
  participate in the Word for Web SharePoint co-auth — that
  channel is opaque to us.

The honest framing for buyers: **"Live co-authoring on the web,
version-based collaboration on the desktop."** Both paths land
in the same VaultDMS document; the merge model differs.

A future ADR (0113-Phase-2) wires Yjs into Word desktop via a
Wopi or a custom protocol handler. Not in scope here.

---

## 8. Versioning + updates

Outlook add-in's §8 applies verbatim — bump `<Version>`, redeploy
the static bundle, re-upload the manifest. The `<Id>` stays
constant; that's how Office recognizes the new bundle as an
UPDATE rather than a NEW add-in.

---

## 9. Rollback

`Remove the add-in` from the M365 admin center → Integrated apps.
The button disappears from every user's ribbon within ~1 hour.
A botched FE deploy can be rolled back by re-uploading the
previous `dist/` to the host; the manifest doesn't change and
Office picks up the new bundle on next load.

---

## 10. What's NOT done (deferrals)

- **`dms.m365.word.opened.v1` / `dms.m365.word.saved.v1` events**.
  The prompt asks for these, but it also says "no new backend
  endpoints beyond existing document API." Bespoke event names
  need either a new audit-emit endpoint or a code change inside
  the existing CreateVersion path. We do neither — instead the
  add-in stamps `change_summary: "Saved from Microsoft Word
  add-in"` on every save, which makes the operation discoverable
  via the existing `dms.version.uploaded.v1` outbox stream. A
  Phase-2 change adds a `source` enum column on `versions` so
  the audit query is exact.
- **Word for Web → Yjs co-auth bridge**. See §7.
- **OOXML diff** of the new version vs the previous. ADR 0101's
  compare endpoint handles this on-demand; the Word add-in just
  uploads bytes.
- **Per-tenant manifest branding**. Same Phase-2 templating idea
  as the Outlook add-in.
- **Recent-documents endpoint** that doesn't go through search.
  Today the add-in's "recent documents" list is "search with an
  empty query", which the search service treats as a recency
  sort. A dedicated `/api/v1/documents/recent` would be one
  query against `documents ORDER BY updated_at DESC` but the
  search path is good enough until profile data says otherwise.
