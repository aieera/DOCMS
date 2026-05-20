# Outlook add-in — deployment runbook (ADR 0112)

Audience: SRE shipping the "Save to VaultDMS" Outlook add-in into a
production tenant. Pre-reqs: a VaultDMS backend already running
with the `/api/v1/auth/m365/exchange` + `/api/v1/integrations/m365/ingest-email`
endpoints (these land in the same release as this add-in), and an
HTTPS host you can point at static content.

Companion doc: [outlook-addin-sideload.md](./outlook-addin-sideload.md)
covers the per-user install once this deploy is live.

---

## 1. What you're deploying

Three artifacts:

| Artifact     | Lives at                                        | Notes                                                                 |
|--------------|-------------------------------------------------|-----------------------------------------------------------------------|
| `manifest.xml` | served from the add-in host                   | Outlook fetches it on every host startup; cache for 24h is fine.      |
| Static bundle  | served from the same host (`taskpane.html` + `commands.html` + `*.js`) | Webpack output of `npm run build` under `addins/outlook`.             |
| Entra ID app  | Microsoft Entra ID                              | Already created if you followed [connectors-m365.md](./connectors-m365.md); same app or a sibling for the add-in surface. |

---

## 2. Build the bundle

```sh
cd addins/outlook
npm install
npm run build         # → dist/
npm run validate      # → office-addin-manifest validate manifest.xml
```

Validate is mandatory before deploy. Office's manifest parser is
strict; an XML error here means every user's Outlook silently
fails to install the add-in.

The `dist/` directory contains:
- `taskpane.html` + bundled `taskpane.*.js`
- `commands.html` + bundled `commands.*.js`
- `manifest.xml` copied verbatim
- `assets/` icons

---

## 3. Edit the manifest for the environment

Three placeholders in `manifest.xml` MUST be replaced before going
live. Use `sed`, or commit a per-environment manifest under
`deploy/outlook-addin/manifest.<env>.xml` and let CI substitute at
deploy time.

| Placeholder                                | Replace with                                                                |
|--------------------------------------------|------------------------------------------------------------------------------|
| `<Id>00000000-0000-0000-0000-000000000111</Id>` | `uuidgen` — one per environment (dev / staging / prod).             |
| `https://addin.vaultdms.example.com`       | The HTTPS host that serves `dist/`. Must be HTTPS — Office refuses HTTP.    |
| `<Id>00000000-0000-0000-0000-000000000222</Id>` in `<WebApplicationInfo>` | The Application (client) ID of the Entra ID app used for SSO. |

The `<Resource>` line under `<WebApplicationInfo>` follows the
`api://<host>/<client-id>` shape and must match the **Application ID URI**
configured in the Entra app's "Expose an API" blade.

---

## 4. Configure the Entra app for SSO

Office's `OfficeRuntime.auth.getAccessToken` returns a token scoped
to the app registration named in `<WebApplicationInfo>`. That app
MUST be set up to:

1. **Expose an API** with a scope (e.g. `access_as_user`) and an
   Application ID URI matching `<Resource>` in the manifest.
2. **Pre-authorize** Office clients:
   - `d3590ed6-52b3-4102-aeff-aad2292ab01c` (Outlook for Microsoft 365)
   - `bc59ab01-8403-45c6-8796-ac3ef710b3e3` (Outlook on the web)
   - `27922004-5251-4030-b22d-91ecd9a37ea4` (Outlook for iOS / Android)
   Add each as a "Authorized client application" with the
   `access_as_user` scope. Without this, Office can't issue tokens
   for your add-in.
3. **API permissions**: `openid`, `profile`, `email`, `User.Read`
   (Microsoft Graph delegated). Grant admin consent.

The connector-side app registration (the one in
[connectors-m365.md](./connectors-m365.md)) is a DIFFERENT app —
that one talks to Graph as the user; this one is just the audience
for the add-in's SSO token. You can use the same app for both with
the right scope mix, but separating them lets you rotate one
secret without touching the other.

---

## 5. Host the static bundle

Three options, pick the one that matches your infra:

### Option A — Frontend CDN (the simplest)

If the VaultDMS frontend is served from a CDN, drop `dist/` into a
sibling subdirectory (`/outlook-addin/`) and you're done. The
`Access-Control-Allow-Origin: *` already-set on the frontend bucket
is what the Office host needs.

### Option B — Dedicated S3 + CloudFront

```sh
aws s3 sync dist/ s3://vaultdms-outlook-addin-prod/ \
  --delete \
  --cache-control "public, max-age=300"
aws cloudfront create-invalidation \
  --distribution-id ABCDEF \
  --paths "/*"
```

The CloudFront distribution MUST:
- Enforce HTTPS (redirect HTTP → HTTPS).
- Forward `Origin` headers + return `Access-Control-Allow-Origin: *`.
- Return `manifest.xml` with `Content-Type: text/xml`.

### Option C — Azure Storage Static Website

If the tenant prefers everything in Azure, the standard "static
website" feature on a Storage Account works the same way. Bind a
custom domain + Microsoft-managed cert; same headers as above.

---

## 6. Validate end-to-end before publishing

From a workstation:

```sh
# Reachability:
curl -I https://addin.vaultdms.example.com/manifest.xml
# Expect 200, Content-Type: text/xml

curl https://addin.vaultdms.example.com/taskpane.html | head -20
# Expect the HTML with the office.js <script> tag

# Manifest validity (one more pass against the LIVE URL):
npx office-addin-manifest validate https://addin.vaultdms.example.com/manifest.xml
```

Then sideload into your own Outlook (see the sideload howto) and
do a real save against a test workspace. The audit row in
`outbox` table should show `dms.m365.outlook.email.saved.v1` with
the correct doc_id + attachment_count.

---

## 7. Publish to the Microsoft 365 admin center

For tenant-wide rollout (vs per-user sideload), Microsoft 365
admins centrally deploy via:

```
Microsoft 365 Admin Center → Settings → Integrated apps → Upload custom apps
```

Upload either the manifest XML or the URL pointing at the
hosted manifest. Choose the assignment audience:
- **Just me** — admin smoke-test first.
- **Specific users / groups** — phased rollout.
- **Entire organization** — flip the switch after the phased
  rollout proves clean.

Add-in propagation can take **up to 24 hours** to reach every
Outlook client. The official advice is to expect six hours; in
practice we've seen up to 18.

---

## 8. Versioning + updates

Outlook caches the manifest aggressively. To force a refresh:
1. Bump `<Version>` in the manifest (use the four-part scheme:
   `1.0.0.0` → `1.0.0.1` for patches; major surfaces a "this
   add-in needs updates" toast).
2. Re-deploy the static bundle (`dist/`).
3. Re-upload the manifest in the M365 admin center.

The `<Id>` MUST stay the same across updates — that's the
identity Outlook uses to recognize this is an UPDATE not a NEW
add-in.

---

## 9. Rollback

The cleanest rollback is "remove the add-in" from the M365 admin
center → Integrated apps. The add-in disappears from every user's
ribbon within ~1 hour. The frontend bundle can stay; users see no
ribbon button until the manifest is re-uploaded.

A botched FE deploy (e.g. taskpane bundle won't parse) can be
rolled back by re-uploading the previous `dist/` to the host. The
manifest doesn't change; Office picks up the new bundle on next
load because the SHA in the filename has changed.

---

## 10. What's NOT done (deferrals)

- **Compose-mode support.** Today the add-in is read-only:
  `<Form xsi:type="ItemRead">` — only available when reading an
  open message. A compose-mode counterpart (`ItemEdit`) lets the
  user attach a VaultDMS document from inside a new email; that's
  its own manifest + UI surface and lands when there's a customer
  ask.
- **Blob upload + OCR + classification.** The ingest endpoint
  creates documents but doesn't persist the actual bytes to
  MinIO yet — same deferral as the existing connector-email
  materialise path (see file header in
  `services/document/internal/handler/m365_ingest.go`). The audit
  event still fires; OCR + classification will pick up when the
  version-upload sweep lands.
- **Unified JSON manifest.** Microsoft is migrating from XML to a
  unified JSON format. We're on XML because mobile + macOS
  Outlook still require it as of mid-2026. Migration when those
  clients catch up.
