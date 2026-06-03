# License setup — operator runbook

How to mint, install, rotate, and verify SeDoc license JWTs.
Companion to [ADR 0095](../adr/0095-license-enforcement.md) which covers the design.

## TL;DR

```bash
# Mint a license
./cmd/license-gen/license-gen.exe \
  --key deploy/license/dev.priv.pem \
  --tenant-id aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa \
  --tenant-name "Acme Corp" \
  --seat-limit 250 \
  --expires 2027-04-19 \
  --features esign,mcp,intel_llm \
  --connectors google_workspace,salesforce,m365 \
  --regions us-east-1,eu-west-1 \
  --issued-to admin@acme.example \
  --out /tmp/license.jwt

# Activate
JWT=$(cat /tmp/license.jwt)
echo "SEDOC_LICENSE_JWT=$JWT" >> .env
docker compose up -d document

# Verify — refresh /admin/tenant/license in the browser. Should show
# the green "License active" banner with the claims you minted.
```

---

## Quick start

### 1. Verify the signing key is in place

```bash
ls -la deploy/license/
# Expected:
#   dev.priv.pem     (RSA-2048 private key — gitignored)
#   dev.pub.pem      (matching public key — public; also embedded in pkg/license/dev_pubkey.go)
```

If `dev.priv.pem` is missing (fresh checkout), regenerate the dev keypair and re-embed the public key:

```bash
openssl genrsa -out deploy/license/dev.priv.pem 2048
openssl rsa -in deploy/license/dev.priv.pem -pubout -out deploy/license/dev.pub.pem
# Then copy the contents of dev.pub.pem into pkg/license/dev_pubkey.go
# and rebuild every service so they verify against the new key.
```

### 2. Build the signer tool

Already built; binary at `cmd/license-gen/license-gen.exe`. Rebuild after edits:

```bash
cd cmd/license-gen && go build -o license-gen.exe .
```

### 3. Mint a license JWT

```bash
./cmd/license-gen/license-gen.exe \
  --key deploy/license/dev.priv.pem \
  --tenant-id <UUID> \
  --tenant-name "<name>" \
  --seat-limit <N> \
  --expires YYYY-MM-DD \
  --features <comma-separated boolean flags> \
  --connectors <comma-separated provider list> \
  --regions <comma-separated region list> \
  --issued-to <admin email> \
  --grace-days 30 \
  --out /path/to/license.jwt
```

**Flag reference:**

| Flag | Required | Meaning |
|---|---|---|
| `--key` | yes | RSA private key (PEM). Defaults to `deploy/license/dev.priv.pem`. |
| `--tenant-id` | **yes** | Tenant UUID; goes into JWT `sub`. |
| `--tenant-name` | **yes** | Display name; goes into JWT `tenant_name`. |
| `--seat-limit` | no | Max active users (informational today; Phase 4 enforces). Default 100. |
| `--expires` | **yes** | Expiry date `YYYY-MM-DD`. |
| `--features` | no | Boolean flags: `esign`, `mcp`, `ipaas`, `intel_llm`. Default `esign,mcp,intel_llm`. |
| `--connectors` | no | Allow-list for `/admin/connectors`. Default all built-ins. |
| `--regions` | no | Allow-list for upload region pinning. |
| `--issued-to` | no | Customer admin email. Defaults to `admin@example.com`. |
| `--issued-by` | no | BD ops email. Defaults to `bd-ops@vaultdms`. |
| `--grace-days` | no | Days of read-only grace after `--expires`. Default 30. |
| `--out` | no | Output file path. Default: stdout. |

### 4. Activate the license

Append to `.env` so docker-compose forwards it:

```bash
JWT=$(cat /path/to/license.jwt)
# remove any old entry first
grep -v "^SEDOC_LICENSE_JWT=" .env > .env.tmp && mv .env.tmp .env
echo "SEDOC_LICENSE_JWT=$JWT" >> .env
```

Alternative for production: drop the JWT at `/etc/vaultdms/license.jwt` inside the container (mounted via a Helm Secret).

### 5. Restart the document service

```bash
docker compose up -d document
```

At startup the container:
1. Reads `SEDOC_LICENSE_JWT` (or falls back to `/etc/vaultdms/license.jwt`).
2. Verifies the RS256 signature against the public key in `pkg/license/dev_pubkey.go`.
3. Caches the parsed claims for `license.Current()` callers.
4. Spawns a background goroutine that re-verifies every hour.

If the JWT is invalid or expired past the grace window, the container exits with `license init: ...`. Fail-closed by design.

### 6. Verify

- Navigate to **http://localhost:3000/admin/tenant/license**.
- The amber "Unlicensed (dev mode)" banner should flip to **green "License active"** with the tenant name, seat limit, expiry, issued-to, issued-by, and feature flags.
- Endpoint check: `curl -b cookies.txt http://localhost:3000/api/v1/admin/tenant/license` — should return `"status":"active"`.

---

## Production posture

For deployments where running unlicensed is a misconfiguration:

```bash
echo "SEDOC_REQUIRE_LICENSE=true" >> .env
```

With this flag, **no license loaded = service refuses to start.** This protects against a misconfigured prod deploy silently running unlicensed.

---

## Rotation

Mint a new license, swap the env var, restart:

```bash
./cmd/license-gen/license-gen.exe --expires 2028-04-19 ...other flags... --out new.jwt
JWT=$(cat new.jwt)
sed -i "s|^SEDOC_LICENSE_JWT=.*|SEDOC_LICENSE_JWT=$JWT|" .env
docker compose up -d document
```

The hourly re-validator catches the new claims without a restart **only if the JWT is loaded from `/etc/vaultdms/license.jwt`** (file path is re-read). Env-var-supplied JWTs are read once at startup, so an env swap requires the container restart.

---

## Key rotation (RSA keypair)

Heavy operation — requires rebuilding every service.

1. Generate a new keypair: `openssl genrsa -out new.priv.pem 2048; openssl rsa -in new.priv.pem -pubout -out new.pub.pem`.
2. Replace the constant in `pkg/license/dev_pubkey.go` with the contents of `new.pub.pem`.
3. Re-mint every active license with `--key new.priv.pem`.
4. Rebuild and roll all services: `docker compose up -d --build`.
5. Update each tenant's `SEDOC_LICENSE_JWT` to the newly-signed JWT.

Until step 5 completes, services run on the new key and reject old licenses — coordinate the rollout so customers aren't locked out.

In prod, the private key lives in BD ops' vault (HashiCorp Vault or 1Password), **never on developer machines**. `cmd/license-gen` runs only via a CI workflow with manual approval.

---

## Revocation

There is no online revocation list. If you need to invalidate a license before its `exp`:

1. Mint a new license for the same tenant with `--expires` set to today.
2. Swap the customer's `SEDOC_LICENSE_JWT` to the new one. They drop into the grace state immediately.
3. After 30 days (default grace), they're locked out.

For instant lockout, rotate the signing key (heavy — see above).

ADR 0095 § Open questions notes that adding a JWKS-style remote revocation endpoint is on the table for a future phase.

---

## Wiring license validation into other services

**Status today: only the document service validates the license.** The remaining 13 services start without invoking the validator — they don't currently care whether a license is loaded.

To wire a new service:

1. Add the pkg/license import to `services/<name>/cmd/server/main.go`:
   ```go
   import "github.com/aieera/sedoc/pkg/license"
   ```

2. After `logger.New()` and `signal.NotifyContext()`, add the 4-line block:
   ```go
   if err := license.Init(); err != nil {
       log.Fatal(ctx).Err(err).Msg("license init")
   }
   license.StartReloader(ctx)
   ```

3. Rebuild that service: `docker compose up -d --build <name>`.

That's the entire integration. The shared env block in `docker-compose.yml` already forwards `SEDOC_LICENSE_JWT` to every Go service, so no per-service env changes are needed.

The pattern is identical for all 13 remaining services. See [services/document/cmd/server/main.go](../../services/document/cmd/server/main.go) for the reference.

---

## Feature-flag gates

The `license.Current()` claims are available globally. To gate a feature at a handler boundary:

```go
import "github.com/aieera/sedoc/pkg/license"

func (h *MyHandler) Create(w http.ResponseWriter, r *http.Request) {
    if !license.Current().HasFeature("esign") {
        http.Error(w, `{"error":"license_feature_disabled","feature":"esign"}`, http.StatusForbidden)
        return
    }
    // ... rest of the handler
}
```

`HasFeature` returns `true` in unlicensed_dev_mode (today's default) so adopting the helper doesn't regress existing dev workflows.

Connector and region allow-lists have dedicated helpers:

```go
if !license.Current().AllowsConnector("salesforce") { ... }
if !license.Current().AllowsRegion("eu-west-1")    { ... }
```

**No gates are wired today** beyond the document service's startup load. The pattern above is the spec for the follow-up work in ADR 0095 § Feature-flag gates.

---

## What works today vs. what doesn't

| Behavior | Status |
|---|---|
| Mint a signed license JWT with `cmd/license-gen` | ✅ working |
| Document service validates license at startup | ✅ working |
| Document service re-validates hourly | ✅ working |
| `/admin/tenant/license` page shows real claims | ✅ working |
| Page shows "active" / "grace" / "expired" / "unlicensed_dev_mode" honestly | ✅ working |
| Wrong/tampered JWT → document refuses to start | ✅ working |
| `SEDOC_REQUIRE_LICENSE=true` blocks startup if absent | ✅ working |
| Expiry beyond grace window → fatal at startup | ✅ working |
| Auth, search, intel, signature, etc. validate license | ❌ not wired (see "Wiring" section above) |
| Grace mode blocks writes with HTTP 423 | ❌ not wired (validator returns `StatusGrace` correctly; write-gate middleware not built) |
| `seats_used` populated from auth users table | ❌ not wired |
| Per-feature gates at service handlers | ❌ not wired (helpers exist; no callers) |
| Frontend 60/30/7-day countdown banners | ❌ not wired (page just shows current status) |
| Online revocation | ❌ not wired (rotate key or set short `--expires`) |

These gaps are tracked as Phase 2-5 work in [ADR 0095 § Phased rollout](../adr/0095-license-enforcement.md).

---

## Troubleshooting

### Page still says "Unlicensed (dev mode)" after restart

```bash
# Confirm the JWT is in .env
grep SEDOC_LICENSE_JWT .env | head -c 80; echo "..."

# Confirm the JWT made it into the container
docker exec vaultdms-document env | grep SEDOC_LICENSE_JWT | head -c 80; echo "..."

# Check startup logs for the license confirmation
docker logs vaultdms-document 2>&1 | grep -i license
# Expected: license: loaded tenant=...sub=...expires=...status=active
```

### Document container won't start: `license init: ...`

The JWT is malformed, signed with the wrong key, or expired past grace. Mint a fresh one with `cmd/license-gen` and try again. The signature check is fail-closed.

### Browser shows 404 on `/admin/tenant/license`

Vite proxy hasn't picked up the route override. Restart the dev server: stop `npm run dev`, restart, hard-refresh the browser.

### Browser shows 401 with `authentication required`

You're not logged in. Sign in at `/login` first.
