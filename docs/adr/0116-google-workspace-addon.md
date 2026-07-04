# ADR 0116 — Google Workspace Add-on (Gmail + Docs)

- **Status**: Accepted
- **Date**: 2026-07-02
- **Relates to**: ADR 0112 (Outlook add-in), ADR 0113 (Word add-in), ADR 0087 (email ingestion), §8.1 (no auth token in browser storage / archtest C2)

## Context

ADR 0112/0113 shipped Office add-ins that file emails and open/save Word
documents against SeDoc's existing REST surface. Customers on Google
Workspace need the equivalent: file a Gmail message (+ attachments) into a
workspace/folder, open a SeDoc document into Google Docs, save it back as a
new version without silently clobbering concurrent edits, and insert
links/references.

Constraints carried over from 0112/0113:
- **No new backend document APIs.** The storage upload chain
  (`/storage/uploads/initiate` → PUT → `/complete`), `POST
  /documents/{id}/versions` (with `base_version_id` optimistic
  concurrency), the workspace/folder list routes, the share-link routes,
  and the email-ingest route all already exist.
- **No SeDoc token in client-side storage** (§8.1, archtest C2).
- **No auto-provisioning** — unknown users get a 404 and an "ask your
  admin" message, same as the M365 exchange.

## Decision

### 1. Apps Script + CardService, not an HTML sidebar or HTTP add-on

The add-on is a single Apps Script project (`addins/google/`) using
CardService JSON cards, deployed with `clasp`. Cards render natively in
Gmail and Docs on web and mobile, need no hosted static bundle (unlike the
Office add-ins' webpack bundles), and keep the entire client inside
Google's server-side sandbox — there is no browser-resident code to leak a
token from. An "alternate runtimes" HTTP add-on would let us reuse the
React task panes but requires hosting + more OAuth plumbing for no
functional gain at this scope.

### 2. Auth: Google OIDC ID token exchanged server-side for a SeDoc session

Mirror of the 0112 exchange, with a Google front-end:

```
ScriptApp.getIdentityToken()                    (Apps Script, per-user)
  → POST /api/v1/auth/google/exchange { google_id_token }
      auth service verifies: signature vs Google JWKS (go-oidc),
      iss == accounts.google.com, aud == SEDOC_GOOGLE_AUDIENCE,
      exp/nbf, email_verified == true,
      hd ∈ SEDOC_GOOGLE_ALLOWED_HDS   ← the tenant-trust boundary
  → user lookup by verified email (shared with m365: 404 / 409-candidates)
  → { vdms_session_token, user_id, tenant_id, email }
```

The M365 verifier's per-`tid` allow-list has no direct Google analogue
(Google is a single issuer), so the **hosted-domain (`hd`) allow-list** is
the equivalent trust boundary: it rejects personal Gmail accounts and any
Workspace domain the operator hasn't explicitly trusted. Like m365, the
feature is **fail-closed**: with `SEDOC_GOOGLE_AUDIENCE` or
`SEDOC_GOOGLE_ALLOWED_HDS` unset, the exchange refuses to run.

Identity mapping is by verified email, same as 0112, with the same known
limitation: a future link-table migration should replace email mapping
with `(hd, sub)` — the JWT's stable subject — alongside 0112's `(tid, oid)`
plan. The verified `sub`/`hd` are logged for that migration.

The session token lives in `CacheService.getUserCache()` (Google server-
side, per-user, 25-minute TTL, below the server session TTL) and is
re-exchanged on 401. It never reaches client-side storage — C2-compliant
by construction.

### 3. Reuse the m365 ingest route for Gmail filing

`POST /api/v1/integrations/m365/ingest-email` takes a provider-agnostic
body (subject/from/to/body/attachments/workspace/folder). The Gmail card
posts the same shape. We deliberately did NOT clone a `/google/ingest-email`
route: one filing pipeline means the ADR 0087 semantics (markdown body +
attachments-as-documents, audit event, dedup by `message_id`) stay in one
place. If the "m365" name grates, a later rename to
`/integrations/email/ingest` with an alias is cheap; a fork is not.

### 4. Docs open/save = Drive import/export + the standard version chain

- **Open**: download the newest SeDoc version's `.docx` (presigned URL) →
  Drive `files` multipart upload with conversion to a Google Doc →
  remember `(document_id, version_id)` for that Doc in
  `PropertiesService.getUserProperties()` — the analogue of the Word
  add-in's CustomProperties stamp (0113 §CustomProperties).
- **Save**: Drive `files/{id}/export?mimeType=docx` → storage
  initiate/PUT/complete → `POST /documents/{id}/versions` with
  `base_version_id` = the remembered version. A 409 (head moved) surfaces
  as "open the latest version, reapply your edits" — never a silent
  clobber. After a successful save the remembered version advances to the
  newly created one.
- **Scopes**: `drive.file` (only files the add-on created or was granted
  per-file access to via the Docs file-scope prompt) +
  `documents.currentonly` (cursor access for insert-link) +
  `gmail.addons.current.message.readonly`. No broad `drive`/`gmail.readonly`
  scope — same minimal-scope posture as 0112/0113 ("the bytes flow through
  the host's own APIs + SeDoc's APIs").

### 5. Insert link/reference

Internal reference = the canonical `/documents/{id}` URL (requires a SeDoc
session). Shareable reference = `POST /documents/{id}/share-links` and
insert the returned tokenised `url`. In Docs the link is inserted at the
cursor via `DocumentApp`; in Gmail read-mode (like Outlook read-mode,
0112) in-body insertion isn't possible, so the reference affordance is a
copy/open link on the result card.

## Consequences

- Two exchange endpoints now mint sessions from IdP tokens
  (`/auth/m365/exchange`, `/auth/google/exchange`) sharing the user-lookup
  + session-mint core (`findUsersByEmailAcrossTenants`,
  `createSessionInTx`). A third provider should extract a common
  `provider → VerifiedIdentity` interface first.
- The add-on requires a Workspace domain (`hd`); personal-Gmail users are
  out of scope by design.
- Multi-tenant email collisions (409) are surfaced but not resolvable
  in-card yet — single-tenant deployments (the norm) never hit this;
  a tenant-chooser card is a follow-up.
- Runtime verification requires a Google Workspace domain + Apps Script
  deployment; CI covers the Go exchange (unit tests) only. The `.gs` files
  are plain V8 JavaScript reviewed against the CardService/GmailApp/
  DocumentApp/UrlFetchApp documented surfaces.
