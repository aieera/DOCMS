# Cross-platform test-server installer — design

Date: 2026-07-24
Status: approved for implementation (autonomous session; user directive:
"make a complete installation bat file … fix all the issues … i need to run
this one windows and linux")

## Problem

SeDoc's onboarding (`make setup`) is bash-only and assumes a Linux host with
Go, golang-migrate and openssl installed. Deploying a test server on Windows
today fails on: no native entry point, OpenSearch's `vm.max_map_count`
requirement unmet in the Docker Desktop/WSL2 VM, stale doc prerequisites
(Go 1.22 vs required 1.25+), and a broken `restart-dev.ps1`.

## Decision: one installer per OS, identical behavior

A single `.bat` cannot execute on Linux (cmd.exe only). Deliverable is a
matched pair with the same 8-step contract:

- `install.bat` → thin cmd wrapper → `scripts/install/install.ps1`
  (Windows PowerShell 5.1 compatible)
- `install.sh` (bash, Linux and WSL2)

Both are idempotent and safe to re-run.

## Shared step contract

1. **Preflight** — docker CLI present, daemon running, compose v2 available,
   daemon in Linux-containers mode. Actionable remediation text on failure
   (Windows: winget install hint; Linux: get.docker.com hint, `--install-docker`
   flag automates it when running with root/sudo).
2. **Kernel prep** — ensure `vm.max_map_count >= 262144` (OpenSearch bootstrap
   check). Linux: `sysctl -w` + persist to `/etc/sysctl.d/99-sedoc.conf`.
   Windows: set it inside the Docker Desktop VM now (`wsl -d docker-desktop`,
   fallback: privileged `nsenter` container) and persist via
   `%USERPROFILE%\.wslconfig` `kernelCommandLine` (takes effect next WSL boot).
3. **Memory guard** — the stack wants ~10–12 GB (ClamAV ~2 GB, OnlyOffice 4 GB,
   OpenSearch heap…). Windows: add `[wsl2] memory=` to `.wslconfig` when absent
   (¾ of host RAM, clamped 8–24 GB); warn when host RAM is low. Linux: warn only.
4. **Env generation** — `.env` + `web/.env` with random secrets; never
   overwrite an existing `.env`. Linux reuses `scripts/gen-dev-env.sh` (now
   with a /dev/urandom fallback so openssl is no longer a hard dependency);
   Windows is a faithful PowerShell port (same keys, same
   backend/frontend `SEDOC_GATEWAY_SECRET` sync).
5. **Stack up** — `docker compose up -d --build`; `--prebuilt` / `-Prebuilt`
   layers `docker-compose.prebuilt.yml` for the 15 published images.
6. **Health wait** — Linux reuses `scripts/wait-for-health.sh` (TIMEOUT=900
   for cold first boot); Windows has an equivalent `docker compose ps
   --format json` polling loop.
7. **Migrations + seed — fully dockerized** so the host needs neither Go nor
   golang-migrate:
   - migrations via `migrate/migrate:v4.17.1` attached to the
     `sedoc-dev_default` network against
     `postgres://sedoc:devpassword@postgres:5432/sedoc`, replicating
     `scripts/migrate-all.sh` order exactly (document `up 1` on fresh DB →
     intelligence → document → search, audit, billing, connector,
     notification; non-document tracks use `x-migrations-table=<svc>_schema_migrations`).
   - seed via `golang:1.25-alpine` running `go run .` in `scripts/seed`
     with `GOWORK=off` (root `go.work` doesn't list the seed module) and a
     `sedoc-gomodcache` volume; `SEED_*` values passed through from `.env`.
8. **Smoke + summary** — probe gateway `http://localhost:8080/` (any HTTP
   response counts, matching `wake.sh` semantics), print admin credentials
   from `.env` and the next step for the web UI (`cd web && npm install &&
   npm run dev` — the compose stack deliberately has no web container).

## Blocker fixes bundled with the installer

- `README.md` / `SETUP.md`: Go prerequisite corrected to 1.25+ (go.work
  toolchain go1.26.2); SETUP gets a Windows section pointing at the installer.
- `scripts/gen-dev-env.sh`: openssl hard-requirement → fallback to
  /dev/urandom.
- `restart-dev.ps1`: Postgres port 5432 → 15432 (compose maps 15432:5432);
  service port map aligned with `scripts/run-all-services.sh`
  (storage 8183, billing 8189, distinct gRPC/health ports, inter-service
  addrs exported); hardcoded dev KEK removed in favor of reading `.env`.
- `CLAUDE.md`: `cmd/` list corrected (dms-admin, dms-sync, license-gen —
  there is no dms-installer).
- `.env.example`: document `SEDOC_INTEGRATION_BFF_URL` (compose defaults it
  to a dev-LAN IP that resolves nowhere on a test server).
- `.gitattributes`: `*.bat` / `*.cmd` check out CRLF (cmd.exe edge cases with
  LF-only batch files).
- New `docs/deploy/windows-test-server.md` runbook.

## Out of scope

- `docker-compose.prod.yml` Linux bind mounts (prod path; test servers use the
  dev stack).
- Publishing prebuilt images for signature-signer / preview-api /
  graphql-gateway / mcp-server / intelligence-worker-misc (CI concern;
  installer builds them).
- Auto-installing Docker Desktop end-to-end on Windows (first launch requires
  GUI interaction; installer detects, attempts winget, instructs, exits).

## Error handling

Every step fails fast with a numbered step banner and remediation text; the
scripts exit non-zero on failure so CI/automation can gate on them. Re-running
after fixing the reported issue is always safe (idempotent steps, `.env`
preserved, migrations/seed idempotent by design).

## Testing

- `bash -n` + shellcheck (if available) on `install.sh` / edited shell scripts.
- PowerShell parse validation where pwsh is available; otherwise careful
  5.1-compatibility review (no `&&`/`||` pipeline chains, no ternary).
- Dockerized migrate/seed path smoke-tested against the running local stack
  where possible.
