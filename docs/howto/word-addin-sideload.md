# Word add-in — sideloading (ADR 0113)

Audience: a SeDoc user installing the "SeDoc for Word" ribbon
into their own Word. Use this for beta testing, demos, or before
the IT admin centrally deploys it.

For the SRE-side rollout, see
[word-addin-deploy.md](./word-addin-deploy.md).

If you've already installed the Outlook add-in
([outlook-addin-sideload.md](./outlook-addin-sideload.md)), the
process here is identical except for the Office host you do it
in.

---

## 1. Prerequisites

- A SeDoc user account that matches the email on your Microsoft
  account. The add-in looks up SeDoc users by exact email
  match — your SeDoc admin invited you with the same address
  you sign into Word with.
- Word for the web, OR Word for Microsoft 365 (desktop, Windows
  or Mac). Word 2019 and earlier are out of scope.
- The hosted manifest URL (your SeDoc admin gives you this;
  it looks like `https://addin.vaultdms.example.com/word/manifest.xml`)
  OR the manifest XML file itself.

---

## 2. Word for the web (Office.com)

```
Word for the web → Home tab → Add-ins drop-down →
  More Add-ins → MY ADD-INS →
  Manage My Add-ins → Upload My Add-in
```

Pick `Add from File…` and upload `manifest.xml`, or `Add from
URL…` and paste the manifest URL.

After a few seconds, a new **SeDoc** tab appears in the ribbon
with two buttons: **Open from SeDoc** and **Save back to SeDoc**.

---

## 3. Word for Windows (desktop)

```
Insert tab → Get Add-ins → MY ADD-INS →
  Manage My Add-ins → Upload My Add-in
```

The first install can take 30-60 seconds to surface the new
ribbon tab — Word caches the manifest aggressively, so closing
and reopening Word once after install is the cleanest signal.

If the "Get Add-ins" button is missing from the ribbon, your IT
admin has blocked custom add-ins centrally. Ask them to allow
sideloading for your account, or to deploy the add-in
organization-wide via [word-addin-deploy.md](./word-addin-deploy.md).

---

## 4. Word for Mac

```
Insert menu → Add-ins → My Add-ins →
  ⚙ icon → Upload My Add-in
```

---

## 5. First-time consent

The first time you click **Open from SeDoc** or **Save back to
SeDoc** after install, Microsoft shows a consent screen listing
the scopes (`openid`, `profile`, `email`, `User.Read`). Click
**Accept**. The task pane then opens.

If the consent screen comes back as **"Need admin approval"**,
your IT admin hasn't granted admin consent on the Entra app yet.
Show them §3 of the deploy howto.

---

## 6. Open a SeDoc document

1. Open Word (a blank doc or an existing one — either works).
2. Click the **SeDoc** tab in the ribbon → **Open from SeDoc**.
3. The task pane lists your recent SeDoc documents.
4. Type to search; press Enter or click Search.
5. Click any result. Word opens the document in a new window.

What's happening under the hood:
- The add-in asks the SeDoc API for the latest version's
  download URL (a presigned URL valid for ~15 minutes).
- It hands the URL to Word via the `ms-word:` protocol handler.
  Word desktop opens it as a new editable doc.
- The source `document_id` is stashed in Office's
  `CustomProperties` for the open document, so the Save panel
  defaults to saving back to the right doc.

---

## 7. Save back as a new version

1. Edit the document in Word.
2. When done, click the **SeDoc** tab → **Save back to SeDoc**.
3. The task pane lands on the Save panel with the source
   document pre-selected (see step 6 — `CustomProperties` keeps
   the link).
4. (Optional) type a change-summary line for the version history.
5. Click **Save as new version**.

What's happening under the hood:
- Word.js's `getFileAsync(Office.FileType.Compressed)` reads the
  full .docx bytes in 4 MB slices.
- The add-in initiates an upload, PUTs the bytes to MinIO, and
  POSTs a new-version row pointing at the resulting blob_id.
- The document's `versions` table grows by one; OCR +
  classification fire in the background.

For Word for Web with a doc opened via the add-in: live co-
authoring is hosted by Microsoft directly. Multiple users on the
web can edit simultaneously; when they close the doc the
"latest" version syncs back via the same Save path.

---

## 8. Troubleshooting

| Symptom                                            | Likely cause + fix                                                                                                |
|---------------------------------------------------|-------------------------------------------------------------------------------------------------------------------|
| "Upload My Add-in" greyed out                     | IT admin has disabled custom add-ins. Ask them to deploy centrally instead.                                       |
| Consent screen says "Need admin approval"         | Entra admin consent not granted. See deploy howto §3.                                                             |
| "no SeDoc user with this email" (404)          | The SeDoc admin hasn't invited your Microsoft email. Ask them to invite you.                                   |
| "email belongs to multiple SeDoc tenants" (409)| Same email exists in 2+ tenants. The taskpane prompts which one to use; pick one.                                 |
| **Open succeeds, but Word shows "read-only"**     | The presigned URL was downloaded into the browser instead of being handed to Word. Re-click — the protocol handler races sometimes. On Mac, Safari may block the protocol; try Chrome or Edge as the host. |
| Save fails with "getFileAsync failed"             | The open document is in a state Word can't snapshot (e.g. uncommitted track-changes review). Accept / reject all and retry. |
| Save uploads but "no content_blob_id"             | The storage service rejected the bytes mid-PUT (size limit, ClamAV scan failure, etc.). Check the storage service logs.   |
| 13003 / "Multi-factor authentication required"    | Sign out of Word + sign back in with MFA, then retry.                                                              |

---

## 9. Removing the add-in

Same Manage My Add-ins menu → click the trash icon next to
"SeDoc for Word".

This only removes it from your account. If your IT admin
deployed it centrally, only they can uninstall.

Removing the add-in does NOT delete any documents in SeDoc —
the documents stay with all their version history. Sign into
SeDoc to delete normally if you need to.
