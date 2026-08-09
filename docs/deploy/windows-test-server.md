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

- **Web UI**: the compose stack has no web container. For a quick local poke
  run `cd web && npm install && npm run dev` (Node 20+) →
  `http://localhost:3000`. That is the **Vite dev server** — a development
  tool. If anyone other than you is going to open this server, serve the
  built app instead: see [Serving the web UI to other people](#serving-the-web-ui-to-other-people).
- **After a Docker Desktop / WSL restart**: run `make wake` from WSL or
  Git Bash — host ports frequently come back dead or cross-wired; `wake`
  batch-restarts the API-fronting containers.
- **ERP integration**: `SEDOC_INTEGRATION_BFF_URL` defaults to a dev-LAN IP
  (`http://192.168.70.22:18091`). Set it in `.env` to your ERP BFF, or ignore
  it — the proxy features fail gracefully when unreachable.
- **Change the seeded admin password** (`admin@acme.local` /
  `ChangeMe!Now2026` unless overridden via `SEED_*` in `.env`).

## Serving the web UI to other people

`npm run dev` is fine while you are the only user. It is the wrong thing to
leave running for a shared test server, for three concrete reasons:

- **It is slow.** The dev server ships unbundled ES modules, so a cold page
  load fetches a few hundred separate files. Multi-second first paints on a
  test box are normal and are not a bug in the app.
- **It is plain HTTP.** Browsers only enable a set of APIs in a *secure
  context* — HTTPS, or `localhost`. Over `http://<machine-name>:3000` the
  browser removes `window.PublicKeyCredential` entirely, so **passkeys cannot
  be registered or used at all**, and the clipboard helpers fall back to a
  legacy path. The Security settings page detects this and says so.
- **Different origin from the API.** The dev server reaches the gateway
  through Vite's proxy, which the built app does not have; the built app
  assumes the API is same-origin at `/api/v1`.

There is no `web/Dockerfile` and no `web` service in any compose file — the
web UI is deliberately not containerised. The supported shape is: **build the
static bundle, and put it behind a reverse proxy that also proxies `/api/*` to
Kong on `:8080`, with TLS terminated at that proxy.**

```bat
cd web
npm ci
npm run build
REM → web\dist  (static files: index.html, assets\*)
```

Then serve `web\dist` from a reverse proxy on the same hostname that fronts
the API. The reference configuration lives in
[`ec2-full-stack.md` §8–§9](./ec2-full-stack.md) — a Caddy site that serves
the SPA with a client-side-routing fallback, proxies `/api/*` and `/healthz`
to `localhost:8080`, proxies `/collab/*` to `localhost:8083`, and obtains a
Let's Encrypt certificate automatically. `deploy/ec2/bootstrap-full.sh`
generates exactly that file, so it is a tested config rather than an example.
Caddy has a Windows build (`caddy.exe`) and the same Caddyfile works there;
if the site already sits behind IIS or nginx, keep that and mirror the three
rules (static root + SPA fallback, `/api/*` → `:8080`, `/collab/*` → `:8083`).

TLS is not optional for a shared server. Caddy needs a real DNS name to get a
public certificate; for an internal-only box, point the site at an internal CA
certificate or an `internal` issuer instead — but do give it HTTPS, otherwise
passkeys stay unavailable no matter what the backend is configured to do.

### Response security headers

Every service stamps `X-Content-Type-Options`, `X-Frame-Options`,
`Referrer-Policy`, `Permissions-Policy` and a Content-Security-Policy on its
responses, and Kong adds the same set to the responses it generates itself
(404s, rate-limit 429s, upstream 502s). Defaults are safe for this stack and
need no configuration. The knobs, if you need them:

| Variable | Default | Notes |
|---|---|---|
| `SEDOC_SECURITY_HEADERS_ENABLED` | `true` | Master switch. Turn off only if your proxy sets conflicting values. |
| `SEDOC_CSP_ENFORCE` | `false` | CSP ships as `Content-Security-Policy-Report-Only`. Watch your browser console for violations on your own deployment, then set `true` to make it blocking. |
| `SEDOC_CONTENT_SECURITY_POLICY` | built-in | Override the whole policy. `off` disables it. |
| `SEDOC_CSP_REPORT_URI` | *(unset)* | Optional violation collector. |
| `SEDOC_HSTS_ENABLED` | `true` | Armed, but the header is only ever sent on a request that actually arrived over HTTPS — a plain-HTTP test server can never pin itself out of reach. |
| `SEDOC_HSTS_MAX_AGE` | `63072000` | Two years. |
| `SEDOC_HSTS_PRELOAD` | `false` | Leave off unless you intend to submit the domain to the browser preload list; that is close to irreversible. |
| `SEDOC_FRAME_OPTIONS` | `DENY` | `SAMEORIGIN` or `off` if you embed the UI in another site. |
| `SEDOC_REFERRER_POLICY` | `no-referrer` | Document and workspace IDs live in URLs. |
| `SEDOC_PERMISSIONS_POLICY` | `camera=(), microphone=(), geolocation=()` | Do **not** add `publickey-credentials-*` — that disables passkeys. |
| `SEDOC_TRUST_FORWARDED_PROTO` | `true` | Correct behind Kong/Caddy/ingress. Set `false` only if a service terminates TLS itself. |

The CSP default assumes the layout above (SPA and API on one origin). If you
put the collaboration WebSocket or an object store on a *different* host,
extend `connect-src` / `img-src` before switching `SEDOC_CSP_ENFORCE` on.

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
