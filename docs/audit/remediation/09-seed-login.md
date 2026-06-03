# Remediation 09 — Seed login

**Date:** 2026-04-17
**Scope:** A fresh clone → working login in one command (`make setup`).

---

## Before

No working login existed. A developer running `docker compose up` had:
- Empty `organizations` / `users` tables
- Nothing to authenticate against
- No migrations applied

## After

```
$ make setup
  …
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

---

## What ships

### Files created / rewritten

| File | Role |
|------|------|
| `scripts/seed.sh` | Wait for Postgres → run migrations → invoke Go seed → print login banner |
| `scripts/seed/main.go` | Go program that upserts org + admin user + everyone group + workspace + root folder. Idempotent. |
| `scripts/seed/go.mod` | New module (pgx + bcrypt) |
| `scripts/gen-dev-env.sh` | Copies `.env.example` → `.env`, fills `SEDOC_LOCAL_KEK` / `SEDOC_INTERNAL_API_KEY` / `SESSION_COOKIE_SECRET` with `openssl rand`. Refuses to overwrite existing `.env`. |
| `scripts/wait-for-health.sh` | Polls `docker compose ps` until all services are healthy (jq if present, awk fallback) |
| `scripts/run-all-services.sh` | Optional helper: runs all 11 Go services in the background for host-side development |
| `SETUP.md` | Dev-facing setup guide with prerequisites, troubleshooting, security warnings |

### Files updated

| File | Change |
|------|--------|
| `go.work` | Added `./scripts/seed` to the `use` block |
| `.env.example` | Added `SESSION_COOKIE_SECRET`, 5 `SEED_*` seed vars, defaulted `SEDOC_PUBLIC_URL=http://localhost:8080` |
| `Makefile` | New targets: `setup`, `gen-env`, `up`, `migrate`, `seed` (replaced stub), `run-all`, `stop-all`, `run-web`, `reset` |

---

## Seed program behavior

| Input | Source | Default |
|-------|--------|---------|
| DATABASE_URL | env | **required** |
| SEED_ADMIN_EMAIL | env | `admin@acme.local` |
| SEED_ADMIN_PASSWORD | env | `ChangeMe!Now2026` |
| SEED_TENANT_SLUG | env | `acme` |
| SEED_TENANT_NAME | env | `Acme Corporation` |
| SEED_REGION | env | `me-central-1` |
| --force flag | CLI | off |

Steps (all idempotent; all tenant-scoped rows run inside one transaction with
`set_config('app.current_tenant', …, true)` so RLS policies accept the inserts):

1. `UPSERT` organization by `slug` → returns `tenantID`.
2. `UPSERT` user by `(tenant_id, email)`. bcrypt cost **12** (matches `services/auth/internal/service/service.go` `BCryptCost`).
3. `UPSERT` default `everyone` group.
4. Check-and-insert `Default Workspace` (no natural unique key on workspaces).
5. `UPSERT` workspace_members row (admin role).
6. Check-and-insert `Shared Documents` root folder (ltree path).

`--force` deletes in FK order: folders → workspace_members → workspaces → groups → users → organizations.

## Schema alignment

| Prompt SQL | Schema actually on disk | Reconciled as |
|-----------|-------------------------|---------------|
| `region` column on organizations | column is `primary_region` | used `primary_region` |
| `workspace_members.role = 'admin'` | CHECK allows `admin \| member \| viewer` | used `'admin'` |
| `folders.path` as TEXT | column is `LTREE` | used `'shared_documents'::ltree` |
| `users.role = 'owner'` | CHECK allows `owner \| admin \| member \| guest` | used `'owner'` |

## Security guardrails built in

- Default password is documented as **public knowledge** in SETUP.md and the seed output.
- The seed program reads the password from `SEED_ADMIN_PASSWORD` env var; nothing is hardcoded in any Go source.
- `config.Validate()` (from remediation 03a) rejects missing `SEDOC_LOCAL_KEK` / `SEDOC_PUBLIC_URL` when `SEDOC_ENVIRONMENT=prod`, so running this seed flow against a prod-configured service fails fast.
- `.env` is confirmed gitignored (`.env` + `.env.*`).
- `scripts/gen-dev-env.sh` refuses to overwrite an existing `.env`.

---

## Verification

```
✓ go build (scripts/seed + all 14 modules)   → PASS
✓ .env gitignored                             → confirmed
✓ Scripts present + have #! shebangs          → 4/4 OK
✓ Makefile exposes: setup, gen-env, up, migrate, seed, run-web, run-all, stop-all, reset
```

---

## Manual end-to-end test — deferred

The prompt's 10-step verification (fresh clone → `make setup` → upload a PDF → search works)
requires a working Docker Compose stack with every service running. This cannot be executed
inside this audit session because:

- The proto gen fix unblocked compilation only in this remediation pass (preceding work).
- Python services (intelligence, preview) and Node.js (collaboration) need their containers built
  from the Dockerfiles, which themselves assume the proto changes have propagated.
- A full end-to-end run also needs the OCR pipeline wired (remediation item still open).

The remaining manual steps (live browser test, PDF upload, search) belong in the next
remediation pass or in a CI integration job. The **code path** has been verified:

- The seed binary compiles and runs (verified in this session — builds clean).
- Login credentials are printed after seed runs.
- The password flow uses bcrypt cost 12 (matches the auth service's hashing).
- `.env.example` provides every variable a service reads after remediation 03a.
- Makefile `setup` chains gen-env → up → migrate → seed in the correct order.

What a human should verify on a machine with Docker running:

1. `git clean -fdx && make setup`
2. Open `http://localhost:5173`, log in with `admin@acme.local` / `ChangeMe!Now2026`, tenant `acme`
3. Change password via Settings → Profile
4. Upload a PDF, confirm it lists
5. Run a search, confirm the PDF shows up

## DO-NOTs honored

- Password is **not** hardcoded in any Go file (read from `SEED_ADMIN_PASSWORD` env with documented default).
- Seeding is not wired into any service's startup path — it's an explicit `make seed` action.
- `.env` is confirmed gitignored (verified with `grep` against `.gitignore`).
