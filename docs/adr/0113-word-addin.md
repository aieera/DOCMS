# ADR 0113 — Word "Open / Save back" add-in

Status: Accepted (Phase 1 — Open + Save flows shipped; bespoke
m365.word.* audit events + Yjs-Word-desktop bridge deferred).
Date: 2026-05-19
Depends on: ADR 0112 (Outlook add-in, which shipped the SSO
exchange endpoint this ADR re-uses).

## Context

ADR 0112 shipped the Outlook surface — the highest-traffic M365
integration. Word is the second-highest: users open documents in
Word for review or editing all day. A "SeDoc" ribbon tab that
lets them open a doc straight from SeDoc — and save the
edited bytes back as a new version — is the natural follow-up.

The pattern is identical to the Outlook add-in: an Office Web
Add-in, manifest XML, React+TypeScript bundle, Fluent UI surface,
hosted as static files behind HTTPS, SSO via
`OfficeRuntime.auth.getAccessToken` → `/api/v1/auth/m365/exchange`.

The DIFFERENCES from Outlook are:
- Word host instead of Mailbox.
- A custom ribbon tab with TWO actions (Open + Save) instead of a
  single MessageRead button.
- The document-bytes path goes through Office.js's slice-based
  `getFileAsync(Office.FileType.Compressed, …)`, not the email
  body/attachment APIs.
- The Open flow uses the `ms-word:ofe|u|<url>` protocol handler
  to hand a presigned URL to Word for opening.

## What ships now (Phase 1)

- **`addins/word/`** — self-contained add-in tree:
  - `manifest.xml` with a `<CustomTab>` named "SeDoc" and two
    `<Button>` controls. SSO via `<WebApplicationInfo>` reusing
    the Outlook add-in's Entra app (or a sibling app — operator
    choice; see deploy howto §3).
  - `src/taskpane/TaskPane.tsx` — Fluent UI tab list switching
    between Open and Save panels. URL-driven default
    (`?action=open|save`) so the two ribbon buttons each land on
    their respective panel.
  - `src/wordIO.ts` — `readDocumentBytes()` does the slice +
    concat dance; `openWordURL()` triggers Word via the
    protocol handler.
  - `src/auth.ts`, `src/api.ts`, `src/config.ts` — same shape as
    the Outlook add-in. Duplicated rather than shared because
    extracting a workspace package wasn't worth ~100 LOC for two
    add-ins.
- **No backend changes**. The exchange endpoint, the existing
  storage upload chain (`/storage/uploads/initiate` → PUT →
  `/complete`), and the existing `/documents/{id}/versions` POST
  all already exist. The add-in is glue between Word and these
  endpoints.
- **Howtos**:
  - [docs/howto/word-addin-deploy.md](../howto/word-addin-deploy.md)
    — SRE-facing: build pipeline, manifest customization, Entra
    SSO (with a Word-specific list of Office client IDs to
    pre-authorize), hosting, M365 admin-center publish.
  - [docs/howto/word-addin-sideload.md](../howto/word-addin-sideload.md)
    — end-user: per-platform install + first-time consent +
    Open / Save walkthrough + troubleshooting.

## Architecture decisions worth defending

### 1. `getFileAsync(Compressed)` + slice concat, not `getOoxml()`

`Word.run(ctx => ctx.document.body.getOoxml())` returns the
WORDPROCESSINGML XML for the body of the document only. Round-
tripping that as a new version drops headers, footers, styles,
embedded media, and any document-level XML the body doesn't
reference. The result is a "saved version" that visually differs
from what the user just edited — a perfect way to torch buyer
trust.

`getFileAsync(Office.FileType.Compressed)` returns the full
`.docx` archive as a sliced binary stream. We concat the slices
(4 MB cap each) into a Uint8Array and PUT that to the storage
service. The .docx that lands in SeDoc is byte-identical to
what a "File → Save As" in Word would produce.

The slice path is more code, but it's the only path that produces
the right outcome.

### 2. `ms-word:ofe|u|<presigned-url>` for the Open flow

The Office protocol handler has been the canonical "open URL into
Word" surface since 2016. We construct a `ms-word:ofe|u|<url>`
URI and click an anchor with it; Word desktop intercepts, Word
for Web falls back to the regular href which the browser hands
off based on Content-Type.

The alternatives we rejected:
- **`window.open(url)`** — opens the .docx in the browser; if
  the user has Word desktop installed, the OS may prompt. Worse
  UX on every platform.
- **`Office.context.document.open()`** — does not exist in
  current Office.js; there's no in-doc surface for "swap the
  open document for this URL." The protocol handler is the
  documented way.

### 3. Save-target is explicit; no auto-create

The Save panel REQUIRES the user to pick a target document. We
don't offer a "save as a new document" path because:
- The "new document" flow is already covered by the regular
  upload UI in the SeDoc frontend.
- The add-in's most common use case is "I opened this doc → I
  saved edits to the same doc as a new version." That flow is
  one-click via the CustomProperties hint we stash on Open.
- Forcing the user to pick a target prevents the "I lost my
  edits because the add-in saved to a new doc I can't find"
  failure mode.

### 4. CustomProperties to link Open → Save

When a user clicks Open and we trigger Word via the protocol
handler, we stamp the source `document_id` into
`Office.context.document.settings`. Word persists these settings
inside the .docx file's CustomXMLPart (they survive Save → reopen).
The Save panel reads the property on mount; the result is
"the right target is pre-selected" without a second search.

Caveat: `settings` only saves when `saveAsync` succeeds, which
happens during Word's normal save sequence. The first Save AFTER
the Open will work; if the user closes Word without saving
locally, the CustomProperty isn't persisted and they'll have to
pick the target again. Acceptable for v1.

### 5. SSO scopes stay minimal — no Graph access through the add-in

The add-in's `<WebApplicationInfo>` asks only for `openid`,
`profile`, `email`, `User.Read`. The bytes flow through Office.js
+ SeDoc's own APIs; we never talk to Graph from the Word
add-in directly. The Outlook add-in took the same posture
(ADR 0112 § decision 4) — fewer consent prompts for the user,
fewer attack surfaces if a token leaks.

If a future Word feature needs Graph (e.g. read the document's
SharePoint metadata for auto-tagging), it goes through the M365
connector (ADR 0111), not the add-in's token.

### 6. Sharing the SSO endpoint with the Outlook add-in

`/api/v1/auth/m365/exchange` (ADR 0112) accepts an Entra token
and returns a SeDoc session — no add-in identifier in the
request. We reuse it as-is. Both add-ins look up SeDoc users
by email, both surface 409 + a tenant-candidate list when the
email exists in multiple tenants. One endpoint, two add-ins.

## Co-authoring story

The prompt asks for "live co-author when a user opens with Edit
in Word for Web." Here's what actually exists and what we ship:

- **Word for Web** opens .docx files at supported URLs in the
  SharePoint-style co-authoring engine, hosted by Microsoft.
  When SeDoc serves the presigned URL with the right
  Content-Type, Word for Web opens it AND can do live co-edit
  with other users opening the same URL within the URL's
  validity window. Microsoft owns the conflict resolution.
- **Word desktop** doesn't do live co-authoring against arbitrary
  URLs — it requires SharePoint Online / OneDrive Business. Our
  Save-back path creates a new version on each save; conflicting
  parallel edits surface as separate versions, and the user
  resolves them via the doc detail page's compare flow
  (ADR 0101).
- **The existing `services/collaboration/` Yjs CRDT path**
  (ADR 0096) runs against the SeDoc native viewer +
  OnlyOffice. It does NOT participate in the Word for Web
  co-auth — that channel is opaque to us, and Microsoft doesn't
  expose a way to intercept it.

**The honest framing**: "Live co-authoring on the web; version-
based collaboration on the desktop." Both paths land in the
same SeDoc document; the merge model differs by host.

A future Phase-2 ADR (0113.2 or 0114) wires Yjs into Word
desktop via a WOPI host or a custom protocol handler. That work
is substantial and out of scope here.

## What is NOT shipped (deferrals)

- **`dms.m365.word.opened.v1` / `dms.m365.word.saved.v1` audit
  events**. The prompt asks for these but ALSO says "no new
  backend endpoints." Bespoke event names need either a new
  audit-emit endpoint OR a code change inside the existing
  CreateVersion path. We do neither. Instead the add-in stamps
  `change_summary: "Saved from Microsoft Word add-in"` (or
  whatever the user typed prefixed with the same source tag) on
  every save, which makes the operation discoverable via the
  existing `dms.version.uploaded.v1` outbox event. A Phase-2
  change adds a `source` enum to the `versions` table so the
  audit query is exact.
- **Yjs-Word-desktop bridge**. See the co-authoring section
  above.
- **Per-tenant manifest branding**. The Outlook add-in's same
  deferral applies — Phase-2 templating step at deploy time
  generates a per-tenant manifest URL with the customer's logo +
  display name.
- **Playwright e2e**. Office add-ins can't be exercised by
  Playwright directly (Word desktop / web isn't a browser
  Playwright drives). The acceptance criterion is a green
  sign-off on the manual test plan at the bottom of the sideload
  howto.
- **Compose-mode Save (auto-save)**. Today Save is explicit —
  the user clicks the ribbon button. A future "auto-save every
  N seconds while the add-in is open" handler lives in
  `commands.ts` once the audit story for it is clear (every
  30s = N versions per session, which is a lot of OCR work).

## Verification

```sh
# Add-in build:
cd addins/word
npm install
npm run lint        # tsc --noEmit
npm run validate    # office-addin-manifest validate manifest.xml
npm run build       # dist/ ready for deploy

# Manual end-to-end (see sideload howto):
# 1. Sideload the add-in.
# 2. Click "Open from SeDoc" → search → pick a doc.
# 3. Edit, then click "Save back to SeDoc".
# 4. Verify a fresh row in `versions` with
#    change_summary LIKE 'Saved from Microsoft Word add-in%'.
# 5. Verify `dms.version.uploaded.v1` lands in the outbox.
```

## Open questions deferred

- **Cross-add-in code sharing**. Auth + API helpers are duplicated
  between Outlook and Word. Extracting a workspace package
  (`addins/_common/`) would dedup ~200 LOC, but Office.js typings
  diverge per host and the build glue cost is real. Revisit when
  Excel lands as a third add-in.
- **A unified Office Add-ins catalog page on the SeDoc frontend**
  that lists all add-ins with one-click sideload instructions.
  Today the howtos live in `docs/howto/`; a self-service page
  inside the product would lower the admin friction.
- **"Save as PDF" alongside "Save as new version"**. Word
  exports to PDF natively; capturing the PDF bytes via
  `getFileAsync(Office.FileType.Pdf)` would let us save a
  signed-ready PDF on demand. Out of scope here.
