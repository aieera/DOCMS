# SeDoc Local Setup

This guide gets a fresh clone running with a working login in one command.

## Prerequisites

- **Docker Desktop 24+** (or Docker Engine + compose plugin).
  On Windows, Docker Desktop must use the **WSL2 backend with Linux
  containers** — every SeDoc image is a Linux image.
- **Go 1.25+** (`go.work` pins `toolchain go1.26.2`; with the default
  `GOTOOLCHAIN=auto` Go downloads it on first build, which needs internet)
- **Node.js 20+** with `npm`
- **Python 3.12+** (only for the intelligence + preview services)
- **golang-migrate** — `brew install golang-migrate` or `go install github.com/golang-migrate/migrate/v4/cmd/migrate@latest`
- **openssl** (optional — secret generation falls back to /dev/urandom)

Optional for Python services (Linux/macOS hosts only — on Windows these
services run in their Docker containers; the native wheels for libmagic,
poppler, WeasyPrint/GTK and ffmpeg are impractical to install on a Windows
host):
- `pip install -r services/intelligence/requirements.txt`
- `pip install -r services/preview/requirements.txt`

## First-time setup

```bash
git clone https://github.com/aieera/sedoc.git
cd vaultdms
make setup
```

### Alternative: self-contained installer (test servers, Windows)

`./install.sh` (Linux/WSL2) and `install.bat` (Windows, wraps
`scripts/install/install.ps1`) stand up the same stack but need **only
Docker on the host** — migrations and seeding run in containers, so Go,
golang-migrate and openssl are not required. They also handle the
OpenSearch `vm.max_map_count` kernel requirement that `make setup` assumes
you've done yourself. Windows specifics: `docs/deploy/windows-test-server.md`.

```bash
./install.sh              # Linux / WSL2   (add --prebuilt to pull ghcr images)
install.bat               # Windows        (add -Prebuilt for the same)
```

`make setup` runs four steps in sequence:

1. **`make gen-env`** — generates `.env` from `.env.example` with fresh random secrets
   (`SEDOC_LOCAL_KEK`, `SEDOC_INTERNAL_API_KEY`, `SESSION_COOKIE_SECRET`).
   Refuses to overwrite an existing `.env`.
2. **`make up`** — `docker compose up -d` + waits for every container's healthcheck to pass.
3. **`make migrate`** — runs `migrate up` against the document service migrations
   (which contain the full 45-table schema) and any other service migrations that exist.
4. **`make seed`** — inserts the default organization + admin user + default workspace
   + root folder. Idempotent; safe to re-run.

Total time on first run: **2–3 minutes** (mostly pulling Docker images).

Once it finishes, you'll see:

```
==========================================
  SeDoc is ready.

  Web UI:  http://localhost:5173
  API:     http://localhost:8080

  Default admin login:
    Tenant slug:  acme
    Email:        admin@acme.local
    Password:     ChangeMe!Now2026

  CHANGE THIS PASSWORD IMMEDIATELY after first login.
==========================================
```

## Running the web UI

```bash
make run-web
```

Then open **http://localhost:5173** and log in with:

- **Tenant:** `acme`
- **Email:** `admin@acme.local`
- **Password:** `ChangeMe!Now2026`

First action after login: **Settings → Profile → Change Password**.

## Running all backend services locally

Two options:

**Option 1 — Docker (recommended for most developers):**
All backend services run inside Docker as part of `make up`. You only need `make run-web` on the host.

**Option 2 — Host (for active development):**
```bash
make run-all   # starts all 11 Go services in the background
make stop-all  # stops them
```

Logs land in `.run/<service>.log`.

## Daily workflow

```bash
make up          # start infra
docker compose stop    # stop without deleting data
make reset       # destroy everything and start fresh (DANGER: wipes local data)
```

## Security notes (READ THIS)

- The default admin password (`ChangeMe!Now2026`) is **PUBLIC KNOWLEDGE** — it's in this repo.
  Never use this setup flow in production.
- Production MUST:
  - Set `SEED_ADMIN_PASSWORD` to a random value (or skip seeding entirely and provision the first admin via `dms-admin`)
  - Set `SEDOC_ENVIRONMENT=prod` so `config.Validate()` rejects missing required secrets
    (`SEDOC_PUBLIC_URL`, `SEDOC_LOCAL_KEK` when `kms_provider=local`, etc.)
  - Source real secrets from Vault / AWS Secrets Manager / Kubernetes Secrets — never from `.env`
- `.env` is gitignored. Verify with `git check-ignore .env` before committing anything.

## Troubleshooting

### `Connection refused` on login
Auth service isn't running. Check:
```bash
docker compose logs auth
```

### `No migration files` error
Migrations CLI isn't installed. Run:
```bash
brew install golang-migrate
# or
go install github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

### OCR doesn't run on uploaded PDFs
Intelligence worker isn't consuming NATS. Check:
```bash
docker compose logs intelligence
```

### Can't find the admin user on login
Seed hasn't run. Run:
```bash
make seed
```

### `pg_isready` not found in `make seed`
The seed script falls back to a 5-second sleep when `pg_isready` isn't installed. Install PostgreSQL client tools if you hit flakiness:
```bash
brew install libpq && brew link --force libpq
```

### `.env` already exists, won't regenerate
The `gen-env` script refuses to overwrite `.env` (avoids wiping your real secrets). Delete it first:
```bash
rm .env && make gen-env
```

### `make reset` destroyed my data
That's what it does. Read the target before running it.

## File map

| Path | Role |
|------|------|
| `scripts/seed.sh` | Wait-for-postgres → migrate → seed → print login |
| `scripts/seed/main.go` | Go program that inserts the default org/user/workspace |
| `scripts/gen-dev-env.sh` | Generate `.env` from template with random secrets |
| `scripts/wait-for-health.sh` | Poll `docker compose ps` until all services healthy |
| `scripts/run-all-services.sh` | Start all Go services on the host (tmpfile logs) |
| `.env.example` | Committed template — no real secrets |
| `.env` | Generated, gitignored, contains real dev secrets |
| `Makefile` | `setup`, `up`, `migrate`, `seed`, `run-web`, `run-all`, `stop-all`, `reset` |
