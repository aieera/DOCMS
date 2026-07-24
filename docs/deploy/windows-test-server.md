# Windows test server

How to stand up the full SeDoc stack on a Windows machine. Everything runs as
**Linux containers under Docker Desktop's WSL2 backend** — there is no native
Windows deployment: the Python workers depend on LibreOffice/poppler/ffmpeg/
libmagic and Celery's prefork pool, none of which work as host processes on
Windows. (The only Windows-native script, `restart-dev.ps1`, is a Go-services
dev loop, not a deployment path.)

## TL;DR

```bat
git clone https://github.com/aieera/sedoc.git
cd sedoc
install.bat
```

`install.bat` (→ `scripts/install/install.ps1`, PowerShell 5.1+) is idempotent
and needs **only Docker Desktop** — migrations and seeding run inside
containers, so Go, golang-migrate and openssl are not required on the host.
Add `-Prebuilt` to pull the published `ghcr.io/aieera/sedoc` images instead of
building everything (5 services — signature-signer, preview-api,
graphql-gateway, mcp-server, intelligence-worker-misc — still build from
source either way).

The same installer exists for Linux/WSL2 as `./install.sh` (`--prebuilt`).

## What the installer does (and why)

1. **Preflight** — Docker CLI, running daemon, compose v2, Linux-containers
   mode. Attempts `winget install Docker.DockerDesktop` when Docker is absent,
   then asks you to start it once and re-run (first-run setup is interactive).
2. **`vm.max_map_count=262144`** — OpenSearch's bootstrap check. The WSL2 VM
   defaults to 65530, so OpenSearch exits at boot on a fresh machine. The
   installer sets it in the running VM (via `wsl -d docker-desktop`, falling
   back to a privileged nsenter container) and persists it in
   `%USERPROFILE%\.wslconfig` (`kernelCommandLine = sysctl.vm.max_map_count=262144`).
3. **WSL2 memory** — the stack wants ~10–12 GB (ClamAV loads ~2 GB of virus
   signatures, OnlyOffice's vendor minimum is 4 GB, OpenSearch heap, 17 app
   services). WSL2's default cap (50 % of RAM, max 8 GB on recent builds) OOMs
   the stack, so the installer writes `[wsl2] memory=<¾ of RAM, 8–24 GB>` into
   `.wslconfig` if you haven't set one. `.wslconfig` changes apply after
   `wsl --shutdown` + Docker Desktop restart; the sysctl is also set at
   runtime so the **current** boot works immediately.
4. **`.env` + `web/.env`** — random secrets (`SEDOC_GATEWAY_SECRET` is
   compose-mandatory and must match between backend and web dev proxy). An
   existing `.env` is never overwritten.
5. **`docker compose up -d --build`** (+ prebuilt overlay with `-Prebuilt`).
6. **Health wait** — polls compose healthchecks, 15-min timeout (ClamAV's
   first signature download and the Temporal schema setup are slow on a cold
   WSL2 disk).
7. **Migrations + seed, dockerized** — replicates `scripts/migrate-all.sh`'s
   interleaved track order exactly (document `000001` → intelligence →
   document → search/audit/billing/connector/notification, per-service
   `x-migrations-table`), then seeds the default tenant/admin. Both idempotent.
8. **Smoke check** — gateway on `http://localhost:8080`, prints the admin
   login from `.env`.

## After install

- **Web UI**: the compose stack has no web container. On the test server run
  `cd web && npm install && npm run dev` (Node 20+) → `http://localhost:3000`,
  or front the API from elsewhere.
- **After a Docker Desktop / WSL restart**: run `make wake` from WSL or
  Git Bash — host ports frequently come back dead or cross-wired; `wake`
  batch-restarts the API-fronting containers.
- **ERP integration**: `SEDOC_INTEGRATION_BFF_URL` defaults to a dev-LAN IP
  (`http://192.168.70.22:18091`). Set it in `.env` to your ERP BFF, or ignore
  it — the proxy features fail gracefully when unreachable.
- **Change the seeded admin password** (`admin@acme.local` /
  `ChangeMe!Now2026` unless overridden via `SEED_*` in `.env`).

## Known Windows footguns

- **Don't bind-mount a Windows folder for connector watched-folder intake** —
  inotify events don't cross NTFS mounts; intake degrades to a 30 s poll. The
  default `intake_data` named volume is correct.
- **Don't use `docker-compose.prod.yml` on Windows** — it bind-mounts Linux
  absolute paths (`/var/lib/vaultdms/...`). Test servers use the dev stack.
- **Don't `pip install` the Python workers on the host** — Linux containers
  only.
- **Clone with git, not a zip download** — `.gitattributes` guarantees correct
  line endings (LF for shell scripts baked into images, CRLF for `.bat`).
- The bash tooling (`make setup`, `make wake`, `scripts/*.sh`) runs fine from
  **WSL2 or Git Bash**; it does not run in cmd/PowerShell. Git Bash users need
  `jq` and `golang-migrate` on PATH for the make flow — or just use the
  installer, which needs neither.
