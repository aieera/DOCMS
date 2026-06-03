# Outlook add-in — sideloading (ADR 0112)

Audience: tenant end-user or admin doing a one-off install of the
"Save to SeDoc" add-in into their own Outlook. Use this for
beta testing, demos, or before the tenant's M365 admin center
deploys it organization-wide.

For the SRE rollout-to-tenant path, see
[outlook-addin-deploy.md](./outlook-addin-deploy.md).

---

## 1. Prerequisites

- A SeDoc user account that matches the email on your work /
  school Microsoft account. The add-in's SSO step looks up
  SeDoc users by exact email match — your SeDoc admin
  invited you with the same address you use to sign into Outlook.
- Outlook on the web, OR Outlook for Microsoft 365 (Windows or
  Mac), OR new Outlook for Windows. Outlook 2019 + earlier are
  out of scope.
- The hosted manifest URL (your SeDoc admin gives you this;
  it looks like `https://addin.vaultdms.example.com/manifest.xml`)
  OR the manifest XML file itself.

---

## 2. Outlook on the web

This is the most common path.

```
Outlook on the web → Settings (gear icon, top-right) →
  General → Manage add-ins → My add-ins →
  Custom add-ins → Add a custom add-in →
    "Add from URL…"  OR  "Add from File…"
```

Paste the manifest URL (or upload the XML file). Click **Install**
on the consent prompt.

The "Save to SeDoc" button appears in the ribbon when you open
any email.

---

## 3. Outlook for Windows (classic)

```
Get Add-ins (icon in the ribbon) →
  My add-ins →
  Add a custom add-in →
    Add from URL…  OR  Add from File…
```

Same flow as web. The button shows up in the **Message** tab of
the ribbon, in a "SeDoc" group.

If the "Get Add-ins" button is missing from the ribbon, your IT
admin has blocked custom add-ins centrally. Ask them to allow
sideloading for your account, or to deploy the add-in
organization-wide via the [deploy howto](./outlook-addin-deploy.md).

---

## 4. New Outlook for Windows / Outlook for Mac

```
View → View settings → Mail → Customize actions →
  Get add-ins → My add-ins → Add a custom add-in
```

Same flow.

---

## 5. Outlook for iOS / Android

```
Settings (in the app) → Add-ins → Get add-ins → My add-ins → Add a custom add-in
```

Mobile add-ins inherit from your desktop / web installations,
so installing once via desktop or web typically also makes the
button appear on mobile within ~1 hour.

---

## 6. First-time consent

The first time you click "Save to SeDoc" after install, Outlook
shows a Microsoft consent screen listing the scopes your IT admin
configured (`openid`, `profile`, `email`, `User.Read`). Click
**Accept**. The taskpane then opens.

If the consent screen comes back as **"Need admin approval"**,
your IT admin hasn't granted admin consent on the Entra app yet.
Show them the [deploy howto](./outlook-addin-deploy.md) §4.

---

## 7. Using the add-in

1. Open any email in Outlook.
2. Click **Save to SeDoc** in the ribbon.
3. Pick a workspace + folder.
4. Optionally add tags. Leave "Include attachments" on (default).
5. Click **Save email**.

You'll get a "Saved to SeDoc" confirmation in the taskpane.
The email shows up in your workspace within a few seconds; OCR
+ classification run in the background and finish within minutes
for typical-size attachments.

---

## 8. Troubleshooting

| Symptom                                             | Likely cause + fix                                                                                            |
|-----------------------------------------------------|---------------------------------------------------------------------------------------------------------------|
| "Add a custom add-in" greyed out                    | IT admin has disabled custom add-ins. Ask them to deploy centrally instead.                                   |
| Consent screen says "Need admin approval"           | Entra admin consent not granted on the SSO app. See deploy howto §4.                                          |
| "no SeDoc user with this email" (404)            | The SeDoc admin hasn't invited your Microsoft email address yet. Ask them to invite you.                   |
| "email belongs to multiple SeDoc tenants" (409)  | Same email exists in 2+ tenants. The taskpane prompts which one to use; pick one.                             |
| 13003 / "Multi-factor authentication required"      | Sign out of Outlook + sign back in with MFA, then retry.                                                      |
| "Workspace dropdown empty"                          | Your SeDoc account doesn't have any workspaces yet, or the session token didn't get attached on the request — refresh the taskpane (close + reopen the email). |
| Save succeeds but the doc has no content yet        | Expected during Phase 1 — see the "What's NOT done" note in the deploy howto. The version-upload sweep will land the bytes shortly. |

---

## 9. Removing the add-in

```
Same Manage add-ins menu → My add-ins → click the trash icon
next to "Save to SeDoc"
```

This only removes it for your account. If you installed via your
M365 admin center (organization-wide deploy), only the admin can
uninstall.

Removing the add-in does NOT delete any documents that have
already been saved to SeDoc. To remove those, sign into
SeDoc and delete normally.
