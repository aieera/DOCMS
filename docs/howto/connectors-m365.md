# Microsoft 365 connector setup (ADR 0111)

Audience: tenant admin installing the M365 connector on SeDoc.
Pre-reqs: an Azure tenant with permission to register apps in
Microsoft Entra ID (formerly Azure AD). Global Administrator OR
Application Administrator role on the directory.

If you're an SRE wiring the connector framework itself (not just
configuring a tenant), read ADR 0111 first.

---

## 1. Pick the Entra topology

Two paths, both supported. The default is multi-tenant.

| Mode          | When to use it                                             | What you paste into the modal |
|---------------|------------------------------------------------------------|-------------------------------|
| Multi-tenant  | SaaS deployment — one Entra app shared across customers    | leave the Directory (tenant) ID field BLANK; the connector uses `common` |
| Single-tenant | Customer insists on hosting their own Entra app            | paste the Directory (tenant) ID GUID into the modal |

**Multi-tenant is the default.** Pick single-tenant only when the
customer's information-security review requires it — it means they
manage rotation, audit, and revocation on their side.

---

## 2. Register the Entra app

```
Azure Portal → Microsoft Entra ID → App registrations → New registration
```

| Field                           | Value                                                                          |
|---------------------------------|--------------------------------------------------------------------------------|
| Name                            | `SeDoc Connector` (any human-readable name)                                 |
| Supported account types         | Multi-tenant: "Accounts in any organizational directory" — Single-tenant: "Accounts in this organizational directory only" |
| Redirect URI                    | Skip on this screen; we add it in step 3 once we know the platform deployment URL |

Click **Register**. Copy the **Application (client) ID** — this is
the `client_id` you'll paste into the modal.

For single-tenant mode also copy the **Directory (tenant) ID** —
this is the GUID you'll paste into the optional Directory ID field.

---

## 3. Add the redirect URI

```
App registration → Authentication → Add a platform → Web
```

Paste the redirect URI from the SeDoc modal's "Authorized
redirect URI" copy-field. Format:

```
https://<your-vaultdms-host>/api/v1/connectors/oauth/callback
```

The path is shared across all native connectors (Google, M365,
future Salesforce, etc.) — the provider is identified by an HMAC-
signed state parameter, not by the URL.

Confirm **Implicit grant and hybrid flows** stays unchecked. We use
the authorization-code flow exclusively.

Save.

---

## 4. Grant the Graph API permissions

```
App registration → API permissions → Add a permission → Microsoft Graph → Delegated permissions
```

Add the following delegated scopes. The selector groups them by
resource type — search each name to find them.

| Scope                  | Why we need it                                  |
|------------------------|-------------------------------------------------|
| `offline_access`       | Issue refresh tokens (without this, the access token expires every hour and the connector breaks). |
| `User.Read`            | Read the connected user's profile (display name, email) for the "Connected as" indicator. |
| `Files.ReadWrite`      | Read AND write files in the user's OneDrive + on accessible SharePoint sites. Used by SharePoint import. |
| `Mail.ReadWrite`       | Read Outlook mail for email-ingestion connector (ADR 0087). |
| `Mail.Send`            | Send mail on behalf of the connected user (workflow notifications). |
| `Sites.ReadWrite.All`  | Enumerate SharePoint sites + read/write their drives. |
| `Group.Read.All`       | List Microsoft 365 Groups (needed for Teams integration). |
| `ChannelMessage.Send`  | Post messages to Teams channels (workflow notifications). |

After adding all eight, click **Grant admin consent for <your tenant>**
at the top of the table. Without admin consent, users get a per-
user consent prompt on first authorize, which most tenant policies
forbid for production apps.

If your tenant DOES want calendar integration, add the optional
`Calendars.ReadWrite` separately — the SeDoc connector treats it
as an opt-in surface.

---

## 5. Create a client secret

```
App registration → Certificates & secrets → New client secret
```

| Field        | Value                                                      |
|--------------|------------------------------------------------------------|
| Description  | `SeDoc connector secret`                                |
| Expires      | 24 months (or your org's max secret lifetime)              |

After clicking **Add**, copy the secret's **Value** column (not the
Secret ID column — the Secret ID is useless on its own). Entra
shows the value only once at creation. If you lose it, you'll have
to create a new secret.

Set a calendar reminder one month before the expiry date — SeDoc
will start logging refresh failures the day the secret expires.

---

## 6. Connect in SeDoc

```
SeDoc → Admin → Connectors → Microsoft 365 → Install
```

Paste:

- **Application (client) ID**     ← from step 2
- **Client secret VALUE**         ← from step 5
- **Directory (tenant) ID**       ← only for single-tenant mode (step 1)

Click **Save & Authorize**. You'll be redirected to Microsoft's
consent screen. After consenting, you'll land back on the
connectors page with the M365 tile showing **Installed**.

---

## 7. Verify the connection

After the redirect lands you should see:

- The M365 tile shows a green "Installed" badge.
- `GET /api/v1/connectors/m365/sites` returns a non-empty list of
  SharePoint sites (your SeDoc team can curl this from a dev
  shell to verify).

If the connect button bounces you back without a redirect to
Microsoft, the most common causes are:

1. **The redirect URI in Entra doesn't match SeDoc's.** Double-
   check the protocol (http vs https) and the trailing slash. The
   modal's copy-field is canonical.
2. **Admin consent was skipped.** Without admin consent, the
   consent screen shows once per user. Open the consent URL
   manually:
   `https://login.microsoftonline.com/<tenant>/adminconsent?client_id=<app-id>`
3. **The wrong secret value was pasted.** Entra's Secret ID column
   is NOT what to paste — only the Value column from the moment of
   creation.

---

## 8. Disconnecting

```
SeDoc → Admin → Connectors → Microsoft 365 → Disconnect
```

Disconnect clears the tenant's OAuth tokens. The Entra app
registration + saved client credentials stay so the admin can
re-authorize without re-pasting client_id / secret. To remove the
credentials too, contact SeDoc support (no admin UI for this
yet — the workflow is rare enough that we'd rather make it manual).

---

## 9. What's NOT supported in this phase

- **Files > 4 MB on upload** — Graph requires an upload-session +
  chunked PUT for files over 4 MB. Phase 2 (ADR 0111).
- **App-only (daemon) tokens** — every connection today is
  delegated-user; an app-only path needs separate Entra config and
  is out of scope.
- **Bookings / Forms / Power Automate triggers** — none of these
  Graph surfaces are wired. ADRs land as customers ask.
- **Conditional Access compliance** — the connector respects CA
  policies (it's just Graph; CA enforces at the token-grant step),
  but SeDoc has no UI for surfacing why a connection failed
  because of CA. Surfacing this is Phase 2.
