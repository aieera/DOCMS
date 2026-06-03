# Deploy SeDoc (minimal API-core) on AWS EC2 — for API checking

Goal: stand up just enough of SeDoc on a single EC2 instance to exercise the
**REST API** (auth, documents, versions, folders, storage, integrations) and
webhooks — e.g. for ERP integration testing. Uses **prebuilt images**
(`ghcr.io/aieera/docms/*`) so there's no source build on the box.

> This is a **test deployment**, not production. It runs the dev RLS-bypass posture
> and a single-node compose stack. For production use the Helm chart
> (`deploy/helm/vaultdms`) and a hardened config.

---

## 1. What runs (minimal core)

| Service | Why it's needed |
|---|---|
| `postgres`, `redis`, `nats`, `minio` | data plane (DB, sessions/cache, events, blob store) |
| `auth` | login, API keys, sessions |
| `policy` | permission checks (document → policy on every read/write) |
| `storage` | upload initiate/complete, blob streaming |
| `document` | documents, versions, folders, `/integrations/triggers/*` |
| `gateway` (Kong) | the single public entrypoint (`:8080`) |

Left out (not needed for API checks): search/OpenSearch, qdrant, intelligence +
worker, preview, onlyoffice, collaboration, clamav, temporal, slapd, signature,
billing, audit, notification, mcp-server. (Add them later if you test those flows.)

Footprint of the core set: **~1 GB RAM** + OS/Docker overhead.

---

## 2. Provision the EC2 instance

| Setting | Value |
|---|---|
| AMI | Ubuntu Server 24.04 LTS (x86_64) |
| Instance type | **t3.medium** (2 vCPU / 4 GB) — comfortable for the core set. `t3.small` (2 GB) is too tight. |
| Storage | 30 GB gp3 |
| Key pair | your SSH key |

**Security group (inbound):**

| Port | Source | Purpose |
|---|---|---|
| 22 | **your IP only** | SSH |
| 443 | 0.0.0.0/0 | HTTPS (via Caddy, §6) |
| 80 | 0.0.0.0/0 | Caddy ACME challenge (Let's Encrypt) |
| 8080 | your IP / ERP IP only | direct gateway access (skip if using Caddy/443) |

Note the instance's **public IP** (and ideally point a domain's A-record at it — needed for free TLS in §6).

---

## 3. Install Docker (on the VM)

```bash
ssh ubuntu@<EC2_PUBLIC_IP>

sudo apt-get update && sudo apt-get install -y ca-certificates curl git postgresql-client
# Docker Engine + compose plugin (official convenience script)
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker ubuntu && newgrp docker     # run docker without sudo

# golang-migrate (seed.sh needs it to apply the document migrations)
curl -fsSL https://github.com/golang-migrate/migrate/releases/download/v4.17.1/migrate.linux-amd64.tar.gz \
  | sudo tar -xz -C /usr/local/bin migrate

# Go toolchain — scripts/seed.sh runs `go run ./scripts/seed` to insert
# the admin user. ~120 MB; only used during the one-shot seed.
curl -fsSL https://go.dev/dl/go1.23.4.linux-amd64.tar.gz | sudo tar -xz -C /usr/local
sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go

migrate -version && docker --version && go version
```

---

## 4. Get the code + configure secrets

```bash
git clone https://github.com/aieera/DOCMS.git && cd DOCMS

# Generate a base .env, then HARDEN it (see edits below)
cp web/.env.example .env 2>/dev/null || true
```

Create/edit `.env` with **fresh** secrets (do NOT reuse dev defaults):

```bash
cat >> .env <<EOF
# --- generated secrets (unique per deploy) ---
SEDOC_GATEWAY_SECRET=$(openssl rand -hex 32)
SEDOC_LOCAL_KEK=$(openssl rand -hex 32)
SEDOC_ESIGN_STATE_HMAC=$(openssl rand -hex 32)

# --- seed admin: CHANGE from the dev default ---
SEED_ADMIN_EMAIL=admin@acme.local
SEED_ADMIN_PASSWORD=$(openssl rand -base64 18)
SEED_TENANT_SLUG=acme

# --- presigned URLs must point at the PUBLIC host, not localhost ---
# Use your domain (preferred) or the EC2 public IP. Needed for the upload flow
# (initiate/complete hand back URLs the client must reach).
SEDOC_S3_PUBLIC_BASE=<your-domain-or-EC2_PUBLIC_IP>:9000

# Dev posture (quick test). For real prod, connect as the dms_app NOBYPASSRLS
# role and REMOVE this line.
SEDOC_ALLOW_BYPASS_RLS=1
EOF

# print the generated admin password ONCE and save it somewhere safe
grep SEED_ADMIN_PASSWORD .env
```

> If you expose MinIO (`:9000`) for the upload flow, add `9000` to the security
> group (your IP / ERP IP only) and keep `SEDOC_S3_PUBLIC_BASE` matching the
> reachable host. For auth/metadata/folder/trigger API checks you can skip this.

---

## 5. Bring up the core stack (prebuilt images)

```bash
export SEDOC_IMAGE_TAG=main   # or pin: sha-<short> / a release tag

CORE="postgres redis nats minio auth policy storage document gateway"

# Pull only what we need, then start infra first
docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml pull $CORE
docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml up -d postgres redis nats minio

# Migrate + seed (document migrations + admin user). Uses the host migrate + psql.
DATABASE_URL="postgres://vaultdms:devpassword@localhost:5432/vaultdms?sslmode=disable" \
  ./scripts/seed.sh

# Now start the services + gateway
docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml up -d auth policy storage document gateway

# Wait for health
watch -n3 'docker compose ps'
```

When `gateway`, `auth`, `policy`, `storage`, `document` are `healthy`, the API is up on `:8080`.

---

## 6. TLS (recommended) — Caddy auto-HTTPS

With a domain pointed at the instance, Caddy gets a Let's Encrypt cert automatically and proxies to the gateway:

```bash
sudo apt-get install -y debian-keyring debian-archive-keyring apt-transport-https
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt-get update && sudo apt-get install -y caddy

# /etc/caddy/Caddyfile
sudo tee /etc/caddy/Caddyfile <<'EOF'
dms.yourdomain.com {
    reverse_proxy localhost:8080
}
EOF
sudo systemctl restart caddy
```

Your API base URL is then `https://dms.yourdomain.com`. (No domain? Use
`http://<EC2_PUBLIC_IP>:8080` with the SG locked to your IP — fine for a quick check.)

---

## 7. Verify the API

```bash
BASE=https://dms.yourdomain.com    # or http://<EC2_PUBLIC_IP>:8080

# 1) gateway routes + auth enforced
curl -s -o /dev/null -w "login=%{http_code}\n" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' -d '{}'                       # expect 401
curl -s -o /dev/null -w "noauth=%{http_code}\n" \
  "$BASE/api/v1/integrations/triggers/documents"                    # expect 401

# 2) full credential flow — mint a scoped key, exercise it, revoke
DMS_BASE_URL=$BASE TENANT_ADMIN_PASS='<the SEED_ADMIN_PASSWORD>' \
  CALLBACK_URL=https://your-erp/api/webhooks/dms ./dms-provision.sh
# → prints dms_base_url, dms_workspace_id, dms_api_key to paste into the ERP
```

A `200` on the trigger feed with the minted key and a `400` (not `401/403`) on
`storage/uploads/initiate` confirm auth + scopes work end-to-end (the same checks
that passed locally).

---

## 8. Hand to the ERP

In the ERP's **External APIs → DMS** settings:
- **Base URL**: `https://dms.yourdomain.com` (no `/api/v1`)
- **API Key**: the `vdms_…` from `dms-provision.sh` (scopes: documents:read/write, upload, integrations:read)
- **Workspace ID**: the printed `dms_workspace_id`
- **Webhook Signing Secret**: create a subscription (`/admin/webhooks` or `POST /api/v1/webhooks`) → copy the `whsec_…`

---

## 9. Security & teardown

- **Change the seed password** (done in §4) and keep the security group locked to known IPs.
- This runs `SEDOC_ALLOW_BYPASS_RLS=1` (dev). For anything beyond a private test, switch to the `dms_app` NOBYPASSRLS role and remove that flag.
- **Cost**: t3.medium ≈ $30/mo + 30 GB gp3 ≈ $2.4/mo. Stop/terminate the instance when you're done testing.
- Logs: `docker compose logs -f gateway document auth`.
- Tear down: `docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml down` (add `-v` to wipe data).

---

## Appendix — one-shot bootstrap

`deploy/ec2/bootstrap.sh` automates §3–§5 on a fresh Ubuntu VM. Review it, set the
secrets, then run it. It does NOT set up TLS (do §6 separately).
