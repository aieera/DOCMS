# How-to: Switch eSign from mock to real DocuSign / Adobe Sign

Mock mode is the dev default (ADR 0071, set via
`VAULTDMS_ESIGN_MOCK_OK=true` in `docker-compose.yml`). To connect a
real DocuSign or Adobe Sign tenant, you need OAuth credentials from
the vendor portal and four env vars per provider.

## 1. DocuSign

1. Sign in to https://developers.docusign.com.
2. **Apps and Keys** → **Add App and Integration Key**.
3. Copy the **Integration Key** — this is the OAuth `client_id`.
4. Under **Authentication**, click **Add Secret Key** and copy the
   value — this is `client_secret`.
5. Under **Additional settings** → **Redirect URIs**, add the URL
   that matches your deploy:
   - Dev: `http://localhost:3000/api/v1/signatures/esign/oauth/callback`
   - Prod: `https://your-domain.com/api/v1/signatures/esign/oauth/callback`
6. Note the OAuth endpoints (sandbox vs production are different
   hostnames):
   - Sandbox: `https://account-d.docusign.com/oauth/auth` + `/token`
   - Prod: `https://account.docusign.com/oauth/auth` + `/token`

## 2. Adobe Sign (Acrobat Sign)

1. Sign in to https://acrobat.adobe.com.
2. **Account** → **API and Adobe Sign** → **Adobe Sign API** →
   **Create Application**. Check **OAuth for Adobe Sign**.
3. Copy the **Client ID** and **Client Secret**.
4. Set the **OAuth Redirect URI** to the same callback URL as DocuSign
   (one callback handler covers both providers).
5. Pick the regional OAuth host matching your Acrobat Sign account:
   - NA: `https://secure.na1.adobesign.com/public/oauth/v2` + `.../oauth/v2/token`
   - EU: `https://secure.eu1.adobesign.com/...` (same shape)
   - JP: `https://secure.jp1.adobesign.com/...`

## 3. Put the creds in `.env`

```bash
# Turn mock off (real providers take over)
VAULTDMS_ESIGN_MOCK_OK=false

# DocuSign — fill ALL six to enable; leave them empty to disable
VAULTDMS_ESIGN_DOCUSIGN_CLIENT_ID=your-integration-key
VAULTDMS_ESIGN_DOCUSIGN_CLIENT_SECRET=your-secret-key
VAULTDMS_ESIGN_DOCUSIGN_AUTHORIZE_URL=https://account-d.docusign.com/oauth/auth
VAULTDMS_ESIGN_DOCUSIGN_TOKEN_URL=https://account-d.docusign.com/oauth/token
VAULTDMS_ESIGN_DOCUSIGN_REDIRECT_URI=http://localhost:3000/api/v1/signatures/esign/oauth/callback

# Adobe Sign — same pattern
VAULTDMS_ESIGN_ADOBE_SIGN_CLIENT_ID=your-client-id
VAULTDMS_ESIGN_ADOBE_SIGN_CLIENT_SECRET=your-client-secret
VAULTDMS_ESIGN_ADOBE_SIGN_AUTHORIZE_URL=https://secure.na1.adobesign.com/public/oauth/v2
VAULTDMS_ESIGN_ADOBE_SIGN_TOKEN_URL=https://secure.na1.adobesign.com/oauth/v2/token
VAULTDMS_ESIGN_ADOBE_SIGN_REDIRECT_URI=http://localhost:3000/api/v1/signatures/esign/oauth/callback

# Required for state-HMAC signing. Random 32 bytes hex. Rotate yearly.
VAULTDMS_ESIGN_STATE_HMAC=$(openssl rand -hex 32)
```

You only fill the providers you actually use — the empty one stays
disabled and its "Authorize" button stays dimmed in the admin UI.

## 4. Restart the signature service

```bash
docker compose up -d --force-recreate signature
docker logs vaultdms-signature 2>&1 | grep esign
```

You should see:

```
esign connectors wired  providers=2  mock=false
```

(or `providers=1` if you filled just one). `mock=false` is the
acceptance line.

## 5. Connect from the admin UI

`/admin/integrations` → click **Authorize DocuSign** (or **Authorize
Adobe Sign**). You'll be redirected to the vendor's consent page,
log in, approve scopes, and the vendor redirects back to
`/api/v1/signatures/esign/oauth/callback` with `?code=...&state=...`.
The signature service:

1. Verifies the HMAC-signed `state` matches the requesting tenant.
2. Exchanges the auth code for `access_token` + `refresh_token`.
3. Seals both with the per-deploy SealingKey (derived from
   `VAULTDMS_LOCAL_KEK`).
4. Inserts/upserts a row in `esign_oauth_tokens`.
5. 303s back to `/admin/integrations?connected=docusign` so the UI
   updates immediately.

`GET /api/v1/signatures/esign/connections` then returns the
connection row (without the tokens — only `account_id`, `base_uri`,
`scope`, `connected_at`, `expires_at`).

## Localhost-callback gotcha (and the fix)

The signature service runs every route through
`pkg/middleware.RequireGatewaySignature`. When **you** click
Authorize, your browser hits Vite (port 3000), Vite proxy injects
`X-Gateway-Signature`, the middleware accepts. Good.

But when **the vendor** redirects your browser back to
`/api/v1/signatures/esign/oauth/callback?...`, the request bypasses
the gateway entirely — no `X-Gateway-Signature` header → 401.

The middleware (as of this commit) whitelists two prefixes for
external traffic:

```
/api/v1/signatures/esign/oauth/callback
/api/v1/signatures/esign/webhook/
```

Each authenticates itself with a different mechanism that doesn't
depend on the gateway:

- **OAuth callback** — the `state` parameter is HMAC-signed with
  `VAULTDMS_ESIGN_STATE_HMAC`. Tampered state fails the verify and
  the callback returns 400 before any token exchange.
- **Vendor webhooks** — DocuSign's HMAC header + Adobe Sign's
  webhook signature, both checked inside the webhook handler
  before any side effect.

If you tunnel through ngrok / Cloudflare and want the callback to
flow through Kong instead, the whitelist doesn't hurt — Kong is
still upstream of the middleware and signs the request anyway.

## Production checklist

- [ ] `VAULTDMS_ESIGN_MOCK_OK=false`
- [ ] Production OAuth URLs (`account.docusign.com`, not `-d.`)
- [ ] Redirect URI matches the public deploy URL exactly, including
      protocol and port
- [ ] `VAULTDMS_ESIGN_STATE_HMAC` set to a 32-byte random value
- [ ] `VAULTDMS_LOCAL_KEK` set (sealing key for tokens at rest)
- [ ] Webhook URL registered in vendor portal:
      `https://your-domain.com/api/v1/signatures/esign/webhook/{provider}/{tenant}`
- [ ] `signature` service deployment env carries the variables
      above (Helm chart `values.yaml`)
- [ ] Cron or Temporal schedule running the
      `SignatureReconcile` workflow (5-minute period by default —
      it polls vendor envelope status for cases the webhook misses)

## Where the code lives (for the next time)

| Path | Purpose |
|---|---|
| `services/signature/cmd/server/main.go:188-230` | Reads creds from env, builds `OAuthByProvider` map, calls `svc.AddESign` |
| `pkg/esign/oauth.go` | `OAuthConfig.AuthorizeURLBuilder` + `VerifyState` (state HMAC) |
| `services/signature/internal/service/esign.go` | `StartOAuth`, `HandleOAuthCallback`, `Disconnect` |
| `services/signature/internal/handler/esign.go` | HTTP routes |
| `services/signature/internal/repository/esign_tokens.go` | `esign_oauth_tokens` table CRUD |
| `pkg/esign/docusign.go` + `adobesign.go` | Provider-specific send/list/cancel + webhook HMAC verify |
| `pkg/middleware/gatewaysig.go` | The whitelist that lets vendor traffic through |
| `web/src/api/signatures.ts:228` | FE call to `/oauth/start` |
| `web/src/routes/_authenticated/admin/integrations.tsx` | Admin Connections UI |
