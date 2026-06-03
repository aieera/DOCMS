# Deploy the COMPLETE VaultDMS stack on AWS EC2

Stand up **all of VaultDMS** — every service, the AI/OCR pipeline, search, workflows,
e-sign, collaboration, and the web UI — on a single EC2 instance using prebuilt images
(`ghcr.io/aieera/docms/*`), then point a domain at it with HTTPS.

> **Two postures, pick one up front:**
> - **A — Full-stack on one box (this guide's default).** Fastest way to run *everything*.
>   Self-hosts Postgres/OpenSearch/MinIO/etc. in containers. Good for a demo, pilot, UAT, or
>   single-tenant install. ~32 GB instance, ~$250–300/mo.
> - **B — Production-hardened (see §11).** Managed RDS + OpenSearch Service + ElastiCache +
>   real S3 + the `dms_app` `NOBYPASSRLS` role, behind an ALB. Use the **Helm chart**
>   (`deploy/helm/vaultdms`) on EKS for HA. This single-EC2 guide is the on-ramp, not the
>   end state.
>
> The companion **minimal API-core** guide (`docs/deploy/ec2-api-check.md`, 9 containers,
> ~$30/mo) is for ERP/API testing only — use this one when you want the *whole product*.

---

## 0. Current status (what you're deploying)

- **Build:** Waves 5–14 structurally complete; Intelligence features 01–10 shipped. The
  product is at the *Production-Ready SaaS → Feature-Complete* gates.
- **Images:** every service publishes to `ghcr.io/aieera/docms/<svc>` on merge to `main`
  (tags: `main`, `sha-<short>`, release tags). No source build needed on the box.
- **Known caveats to deploy around** (none block bring-up; see §12 Troubleshooting):
  - graphql-gateway uses a startup-only gRPC dial and a stale `collaboration:9090` address
    (Yjs is on `:8083`) — restart-to-recover, or leave graphql-gateway out if unused.
  - intelligence-worker can drop OCR jobs silently under load — watch the DLQ + the
    processing-failure surface.
  - On Docker restarts, host ports can cross-wire — batch-restart the API-fronting
    containers (see §12).
  - License enforcement, native connectors (beyond Google), and iPaaS tiles are partial —
    they don't affect core bring-up.

---

## 1. What runs (full inventory)

| Tier | Containers |
|---|---|
| **Data plane** | `postgres`, `redis`, `nats`, `minio` (+ `minio-init` one-shot) |
| **Search & vectors** | `opensearch`, `qdrant` |
| **Workflow** | `temporal`, `temporal-ui` |
| **Security scan** | `clamav` |
| **Directory (optional)** | `slapd` (LDAP/AD test directory) |
| **Gateway** | `gateway` (Kong, public `:8080`), `autoheal` (restarts wedged gateway) |
| **Go services (13)** | `auth`, `policy`, `document`, `storage`, `search`, `audit`, `workflow`, `notification`, `signature`, `billing`, `connector`, `mcp-server`, `graphql-gateway` |
| **Workers (non-Go)** | `collaboration` (Node/Yjs), `intelligence` (Python API), `intelligence-worker`, `intelligence-worker-misc`, `preview-worker` |
| **Office co-authoring (opt-in profiles)** | `onlyoffice`, `collabora` (`--profile collabora`) |
| **Web UI** | built from `web/` and served by Caddy (not a compose service — see §8) |

That's ~28 containers. OnlyOffice/Collabora are the heaviest and **optional** — leave them
off unless you're testing in-browser document editing.

---

## 2. Provision the EC2 instance

| Setting | Value |
|---|---|
| AMI | Ubuntu Server 24.04 LTS (x86_64) |
| **Instance type** | see sizing table below |
| Storage | **100 GB gp3** (OpenSearch + Postgres + MinIO blobs + AI model cache grow fast) |
| Key pair | your SSH key |

**Sizing — the full stack is RAM-bound (OpenSearch, ClamAV, Temporal, AI workers):**

| Profile | Instance | vCPU / RAM | ~$/mo | Notes |
|---|---|---|---|---|
| Comfortable (recommended) | `m6i.2xlarge` | 8 / 32 GB | ~$280 | Whole stack + headroom |
| Tight (no OnlyOffice/Collabora, single intel worker) | `t3.xlarge` | 4 / 16 GB | ~$120 | Trim heavy bits; watch OOM |
| Heavy AI / many users | `r6i.2xlarge` | 8 / 64 GB | ~$370 | OCR + RAG at volume |

> Don't go below 16 GB for the full set — OpenSearch alone reserves ~1.5 GB and ClamAV ~1.5 GB.

**Security group (inbound):**

| Port | Source | Purpose |
|---|---|---|
| 22 | **your IP only** | SSH |
| 80 | 0.0.0.0/0 | Caddy ACME (Let's Encrypt) |
| 443 | 0.0.0.0/0 | HTTPS (web UI + API via Caddy) |
| 8195 | your IP only (optional) | OnlyOffice, if you enable it |

Everything else stays internal to the Docker network — **do not** expose `:8080`, `:9000`,
`:5432`, `:9200`, Temporal, etc. publicly. Caddy on 443 is the only front door.

Allocate an **Elastic IP** and point a DNS A-record (`dms.yourdomain.com`) at it — required
for free TLS and correct presigned-URL hosts.

---

## 3. Install host dependencies

```bash
ssh ubuntu@<EC2_PUBLIC_IP>

sudo apt-get update
sudo apt-get install -y ca-certificates curl git postgresql-client

# Docker Engine + compose plugin
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker ubuntu && newgrp docker

# golang-migrate (seed.sh applies the shared schema)
curl -fsSL https://github.com/golang-migrate/migrate/releases/download/v4.17.1/migrate.linux-amd64.tar.gz \
  | sudo tar -xz -C /usr/local/bin migrate

# Go toolchain — scripts/seed.sh runs `go run ./scripts/seed` to insert the admin user
curl -fsSL https://go.dev/dl/go1.23.4.linux-amd64.tar.gz | sudo tar -xz -C /usr/local
sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go

# Node 20 — to build the web UI (§8)
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt-get install -y nodejs

docker --version && migrate -version && go version && node -v
```

> **`vm.max_map_count` for OpenSearch** — required or OpenSearch won't start:
> ```bash
> echo 'vm.max_map_count=262144' | sudo tee /etc/sysctl.d/99-opensearch.conf
> sudo sysctl --system
> ```

---

## 4. Get the code

```bash
git clone https://github.com/aieera/DOCMS.git && cd DOCMS
```

You build images on the box only for `--profile`-gated extras; everything ships prebuilt.

---

## 5. Arrange the environment (`.env`)

Start from the committed template, then layer in **fresh, unique** secrets. Never reuse dev
defaults on an internet-reachable box.

```bash
cp .env.example .env

PUBLIC_HOST=dms.yourdomain.com        # or the Elastic IP

cat >> .env <<EOF

# ===== generated per-deploy secrets =====
SEDOC_GATEWAY_SECRET=$(openssl rand -hex 32)
SEDOC_LOCAL_KEK=$(openssl rand -hex 32)
SEDOC_ESIGN_STATE_HMAC=$(openssl rand -hex 32)
SEDOC_ONLYOFFICE_JWT=$(openssl rand -hex 32)

# ===== seed admin (CHANGE from dev default) =====
SEED_ADMIN_EMAIL=admin@yourco.com
SEED_ADMIN_PASSWORD=$(openssl rand -base64 18)
SEED_TENANT_SLUG=yourco

# ===== presigned URLs must resolve from the browser/ERP =====
SEDOC_S3_PUBLIC_BASE=${PUBLIC_HOST}

# ===== image tag =====
SEDOC_IMAGE_TAG=main

# ===== posture =====
# Test posture only. For production, connect as the dms_app NOBYPASSRLS role
# and DELETE this line (see §11).
SEDOC_ALLOW_BYPASS_RLS=1
EOF

grep SEED_ADMIN_PASSWORD .env    # save this once
```

**Object storage choice:**
- **Self-hosted MinIO (default, in this stack)** — nothing else to set; blobs live in the
  `miniodata` volume. Back it up (§11).
- **Real AWS S3** — copy from `.env.aws.example` instead: set `SEDOC_S3_ENDPOINT`,
  `SEDOC_S3_REGION`, `S3_ADDRESSING_STYLE=virtual`, and either an IAM key pair or (better)
  an **instance role** (leave key/secret blank). Then you can skip the `minio` container.

**LLM provider (for Q&A / NER LLM / summarize / translation):** the intelligence worker
routes through **litellm**. Point it at OpenAI/Anthropic (needs an API key + egress) or run
**Ollama** for a fully self-contained box. Configure per
`docs/runbooks/llm-provider-ops.md` or in the UI under **Admin → AI**. Regex/SpaCy NER, OCR,
classification, embeddings, and search all work **without** an external LLM.

---

## 6. Bring up the full stack

Use the prebuilt overlay so nothing builds from source:

```bash
export SEDOC_IMAGE_TAG=main
COMPOSE="docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml"

# 6.1 Pull everything
$COMPOSE pull

# 6.2 Start the data + search + workflow plane first
$COMPOSE up -d postgres redis nats minio minio-init opensearch qdrant temporal clamav

# 6.3 Migrate the shared schema + seed the admin user
#     (document service's initial migration creates the shared 45-table schema)
DATABASE_URL="postgres://vaultdms:devpassword@localhost:5432/vaultdms?sslmode=disable" \
  ./scripts/seed.sh

# 6.4 Start all application services + workers + gateway
$COMPOSE up -d \
  auth policy document storage search audit workflow notification \
  signature billing connector mcp-server graphql-gateway \
  collaboration intelligence intelligence-worker intelligence-worker-misc preview-worker \
  temporal-ui gateway autoheal

# 6.5 (optional) in-browser co-authoring
# $COMPOSE --profile collabora up -d collabora
# $COMPOSE up -d onlyoffice

# 6.6 Watch health
watch -n3 '$COMPOSE ps'
```

When the Go services + `gateway` report `healthy` (give OpenSearch/ClamAV/Temporal a couple
of minutes for their start periods), the API is live on the internal `:8080`.

> Bringing the data plane up **before** migrate/seed matters — `seed.sh` waits for Postgres
> but the app services assume the schema already exists.

---

## 7. (Verify the API before adding the UI)

```bash
# auth is enforced (no creds → 401)
curl -s -o /dev/null -w "login=%{http_code}\n" -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' -d '{}'        # expect 401

# end-to-end credential + scope check (mints a key, exercises it, revokes)
DMS_BASE_URL=http://localhost:8080 \
  TENANT_ADMIN_PASS='<the SEED_ADMIN_PASSWORD>' ./dms-provision.sh
```

---

## 8. Build and serve the web UI

The frontend is a static SPA (axios `baseURL: '/api/v1'`, cookie auth) — it must be served
from the **same origin** that proxies `/api/*` to the gateway. Build it, then let Caddy serve
the files and reverse-proxy the API.

```bash
cd web
npm ci
# Production build talks to same-origin /api/v1 — no dev proxy secret needed
# (Kong injects the gateway signature). Just build:
npm run build          # → web/dist
cd ..
sudo mkdir -p /var/www/vaultdms && sudo cp -r web/dist/* /var/www/vaultdms/
```

---

## 9. TLS + single front door (Caddy)

```bash
sudo apt-get install -y debian-keyring debian-archive-keyring apt-transport-https
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt-get update && sudo apt-get install -y caddy

sudo tee /etc/caddy/Caddyfile <<'EOF'
dms.yourdomain.com {
    encode zstd gzip

    # API + auth + integrations → Kong gateway
    @api path /api/* /healthz
    handle @api {
        reverse_proxy localhost:8080
    }

    # Real-time collaboration (Yjs WebSocket)
    handle /collab/* {
        reverse_proxy localhost:8083
    }

    # Everything else → the SPA (client-side routing fallback)
    handle {
        root * /var/www/vaultdms
        try_files {path} /index.html
        file_server
    }
}
EOF

sudo systemctl restart caddy
```

Caddy fetches a Let's Encrypt cert automatically. Your product is now at
`https://dms.yourdomain.com`. Log in with `SEED_ADMIN_EMAIL` / `SEED_ADMIN_PASSWORD`.

> If you enabled OnlyOffice, also expose `:8195` (SG → your IP) and set
> `SEDOC_ONLYOFFICE_PUBLIC_URL` to a host the browser can reach.

---

## 10. Verify end-to-end (the whole pipeline)

1. **Log in** at `https://dms.yourdomain.com`.
2. **Upload** a scanned PDF → it should appear, then within ~30–60 s show OCR text, a
   classification badge, language, and (if configured) entities.
3. **Search** the document's text → confirms OCR → OpenSearch indexing fired.
4. **Ask** (`/ask`) a question about it → confirms embeddings → Qdrant → RAG.
5. **Workflow** — start an approval on the doc → confirms Temporal.
6. **Audit log** (`/admin/audit-log`) shows the upload/login events.

If OCR text never appears, check `intelligence-worker` logs and the DLQ (§12) — that's the
one known silent-drop path.

---

## 11. Production hardening (do these before real data)

This single-box test posture is **not** production. To harden:

| Area | Test posture (this guide) | Production |
|---|---|---|
| **RLS** | `SEDOC_ALLOW_BYPASS_RLS=1` | Remove it; connect as the `dms_app` `NOBYPASSRLS` role so a missing tenant predicate fails closed |
| **Database** | container Postgres | **RDS Postgres 16** (Multi-AZ, automated backups, PITR) |
| **Search** | container OpenSearch | **Amazon OpenSearch Service** |
| **Cache/sessions** | container Redis | **ElastiCache Redis** |
| **Blobs** | container MinIO | **S3** with versioning + bucket policy + SSE; instance role, no static keys |
| **Events** | single NATS | NATS cluster (3 replicas) or managed |
| **Keys** | `SEDOC_LOCAL_KEK` (HKDF) | **AWS KMS / Vault** per-tenant, per-region KEK; rotate every 90 days |
| **Secrets** | `.env` on disk | **AWS Secrets Manager / SSM Parameter Store**, loaded at boot |
| **Ingress** | Caddy on the box | **ALB + ACM cert + WAF**; private subnets for app/data |
| **Compute** | one EC2, compose | **EKS + Helm chart** (`deploy/helm/vaultdms`), HPA + PDB per service |
| **Backups** | none | RDS snapshots, S3 versioning, OpenSearch snapshots, Postgres `pg_dump` cron |
| **Observability** | `docker logs` | Prometheus (`/metrics` :8081) + Grafana (`deploy/monitoring/`) + OTEL → Tempo |
| **Residency** | single region | Per-region KEK masters + `region_pin` (ADR 0026/0110); `deploy/regions/` |

For multi-node HA the supported path is the **Helm chart** (`docs/runbooks/helm-install.md`)
or the **Ansible bundle** (`docs/runbooks/ansible-install.md`) for on-prem/air-gapped.

---

## 12. Operations & troubleshooting

**Logs / status**
```bash
COMPOSE="docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml"
$COMPOSE ps
$COMPOSE logs -f gateway document auth intelligence-worker
```

**Known issues to watch (from the engineering notes):**
- **Ports cross-wired after a Docker/host restart** (a service answers on another's port):
  batch-restart the API-fronting containers —
  `$COMPOSE restart gateway auth policy document storage search`.
- **Gateway wedged:** the `autoheal` sidecar restarts it automatically; manual is
  `$COMPOSE restart gateway`.
- **graphql-gateway** dials `collaboration:9090` (stale; Yjs is `:8083`) at startup only.
  If you don't use GraphQL, you can omit `graphql-gateway` from §6.4. Otherwise restart it
  after `collaboration` is healthy.
- **OCR not running:** `intelligence-worker` can drop jobs silently under load — check its
  logs and the DLQ subjects (`dms.dlq.intel_events.*`); the processing-failure surface
  (ADR 0115) exposes these in the admin UI.
- **OpenSearch won't start:** confirm `vm.max_map_count=262144` (§3).
- **Upload "completes" but client can't fetch the blob:** `SEDOC_S3_PUBLIC_BASE` must be
  the reachable public host (§5), not `localhost`.

**Update to a new build**
```bash
export SEDOC_IMAGE_TAG=main      # or a pinned release tag
$COMPOSE pull && $COMPOSE up -d
DATABASE_URL="postgres://vaultdms:devpassword@localhost:5432/vaultdms?sslmode=disable" ./scripts/seed.sh  # apply new migrations
cd web && npm ci && npm run build && cd .. && sudo cp -r web/dist/* /var/www/vaultdms/
```

**Teardown**
```bash
$COMPOSE down            # stop (keeps volumes/data)
$COMPOSE down -v         # DANGER: wipe all data + blobs
```

**Cost note:** an `m6i.2xlarge` + 100 GB gp3 runs ~$290/mo. Stop the instance when idle;
detach the Elastic IP if you stop for long (idle EIPs bill).

---

## Appendix — one-shot bootstrap

`deploy/ec2/bootstrap-full.sh` automates this entire guide (§3–§9) on a fresh Ubuntu VM:
host deps + `vm.max_map_count`, fresh-secret `.env`, full-stack pull/up, migrate + seed,
web build, and Caddy (auto-HTTPS for a domain, HTTP for a bare IP). Safe to re-run.

```bash
git clone https://github.com/aieera/DOCMS.git && cd DOCMS

# with a domain (recommended — gets a Let's Encrypt cert automatically):
sudo PUBLIC_HOST=dms.yourdomain.com bash deploy/ec2/bootstrap-full.sh

# IP-only quick run (HTTP, no TLS):
sudo bash deploy/ec2/bootstrap-full.sh

# also enable in-browser DOCX editing:
sudo PUBLIC_HOST=dms.yourdomain.com ENABLE_ONLYOFFICE=1 bash deploy/ec2/bootstrap-full.sh
```

Knobs: `PUBLIC_HOST`, `SEDOC_IMAGE_TAG`, `ENABLE_ONLYOFFICE=1`, `SKIP_WEB=1`,
`SKIP_CADDY=1`. It prints the generated admin password once — save it.

> For the *minimal API-core* only (9 containers, ~$30/mo), use `deploy/ec2/bootstrap.sh`
> and `docs/deploy/ec2-api-check.md` instead.
