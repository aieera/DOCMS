# Upgrading to the 2026-10 review-remediation release

Every step below happens once per deployment, in the order given. Steps 1–4 come before the new binaries take traffic. Steps 5–7 belong to the rollout itself.

Applies to Helm (`deploy/helm/sedoc`), the production compose override (`docker-compose.yml` + `docker-compose.prod.yml`) and Ansible (`deploy/ansible`).

## 0. Back up

Take a full database backup first. Steps 3 (audit re-baseline) and 4 (migrations) rewrite data, and neither can be rolled back.

- Helm: run `kubectl create job --from=cronjob/sedoc-backup sedoc-backup-pre-upgrade` (it runs as `dms_backup`).
- Other installs: run `pg_dump` as the owner role.

## 1. New required configuration

Create these Secrets / `.env` keys **before** upgrading. Each one is fail-closed: the service refuses to start, or the route answers 503, until it is set.

| What | Helm (Secret / key or value) | Compose prod (`.env`) | Ansible (vault) |
|---|---|---|---|
| Runtime DB role password (DEC-1) | `sedoc-db-credentials` / `dms-app-password` | `DMS_APP_PASSWORD` | `vault_db_app_password` |
| KEK — no longer optional (H-20) | `sedoc-encryption` / `local-kek` | `SEDOC_LOCAL_KEK` | `vault_local_kek` |
| NATS password (O-4) | `sedoc-nats-credentials` / `nats-password` | `SEDOC_NATS_PASSWORD` | OPS |
| Redis password (R-19) | `sedoc-redis-credentials` / `redis-password` | `REDIS_PASSWORD` | OPS |
| Internal service key | `sedoc-platform-secrets` / `internal-api-key` | `SEDOC_INTERNAL_API_KEY` | `vault_internal_api_key` |
| IRM signing key, zt pepper, OnlyOffice JWT | `sedoc-platform-secrets` / `irm-signing-key`, `zt-token-pepper`, `onlyoffice-jwt` | `SEDOC_IRM_SIGNING_KEY`, `SEDOC_ZT_TOKEN_PEPPER`, `SEDOC_ONLYOFFICE_JWT` | `vault_irm_signing_key`, `vault_zt_token_pepper`, `vault_onlyoffice_jwt` |
| DSR anonymisation salt | `sedoc-platform-secrets` / `dsr-anonymize-salt` | `SEDOC_DSR_ANONYMIZE_SALT` | `vault_dsr_anonymize_salt` |
| e-discovery signing key | `sedoc-platform-secrets` / `ediscovery-signing-key` | `SEDOC_EDISCOVERY_SIGNING_KEY` | OPS |
| License JWT + public key (O-10) | `sedoc-license` / `license-jwt`, `license-public-key` | `SEDOC_LICENSE_JWT` | `vault_license_jwt` + public-key file |
| Signer keystore | `sedoc-signer-keystore` / `keystore.p12`, `keystore-password` | `SIGNER_KEYSTORE_FILE`, `SIGNER_KEYSTORE_PASS` | OPS |
| Seal level / TSA | `signature.sealLevel`, `signature.tsaURL` | `SEDOC_SEAL_LEVEL`, `SIGNER_TSA_URL` | OPS |
| Client-IP trust (DEC-5) | `global.trustedProxies`, `gateway.trustedIPs` (no defaults) | fixed `/32` (compose subnet) | OPS |
| Collaboration browser origin | `collaboration.allowedOrigins` (default `https://<domain>`) | `SEDOC_ALLOWED_ORIGINS` | OPS |

Notes on the table:

- **Zero-trust pepper.** Setting or rotating `zt-token-pepper` invalidates every zero-trust link minted under the previous value; links live 72 h.
- **DSR salt.** Changing `dsr-anonymize-salt` later changes every pseudonym produced afterwards, so set it once.
- **URL-safe values.** URL-embedded passwords (`dms-app-password`, `redis-password`, `nats-password`) must be URL-safe. Generate them with `openssl rand -hex 24`.
- **Secret list.** Helm prints the exact list in `NOTES.txt`. With `externalSecrets.enabled=true` the ExternalSecrets are generated from the same list.

## 1a. Object store image (MinIO)

MinIO withdrew its community images from Docker Hub and quay.io in 2025; `minio/minio:*` and `minio/mc:*` no longer pull. The stack now uses Chainguard's builds, pinned by multi-arch index digest (`cgr.dev/chainguard/minio@sha256:87896cb3…`, `cgr.dev/chainguard/minio-client@sha256:dd66c90b…`).

- **Data is untouched**: the on-disk format is the same MinIO; the `miniodata` volume carries over.
- **Ownership**: the Chainguard image defaults to uid 65532, but every existing volume was written by the old image as root, so `docker-compose.yml` runs MinIO as `user: "0:0"` (unchanged behaviour). Helm's bucket-init Job already runs as uid 1000 against S3 over the network and needs nothing.
- **Hosts without internet access to cgr.dev** must mirror both digests (e.g. into `ghcr.io/aieera`) and override the image references; the air-gap bundle (`scripts/airgap/build-bundle.sh`) already saves them.
- **OPS precondition**: Chainguard's free registry only promises `:latest`; mirror the pinned digests into `ghcr.io/aieera` so a garbage-collected digest cannot take fresh installs down again.

## 2. Database roles

- **What the migration step does.** It runs as the owner/superuser role and creates `dms_app` (NOLOGIN, NOBYPASSRLS) together with `dms_evidence` and `dms_backup`. It grants LOGIN with the passwords from step 1 and sets the default privileges (`scripts/db-bootstrap-roles.sql`, `scripts/db-bootstrap-postmigrate.sql`).
- **Where it runs:**
  - Helm: the migrate Job's role bootstrap.
  - Compose prod: the `migrate` service.
  - Ansible: OPS's postgres role.
- **Grants on older tables.** Default privileges only cover tables created **after** they are set. On a database whose tables already existed, grant `dms_app` once, matching `scripts/init-db.sql`: SELECT, INSERT, UPDATE, DELETE on the tables and USAGE, SELECT on the sequences. Then re-apply the REVOKEs from document 000001: no INSERT/UPDATE/DELETE on `organizations`, and no UPDATE/DELETE on `audit_events`.

## 3. Audit chain re-baseline

Audit chains written before the canonical-hash fix never verify. Until they are re-baselined, GDPR erasure answers 409 `audit_chain_unverifiable`.

Follow `docs/runbooks/audit-chain-rebaseline.md`:
- use the owner DSN and the audit signing key;
- do a dry run first;
- there is no rollback, so the backup from step 0 is the only way back.

## 4. Migrations — before any new binary

The full chain must be applied before the new binaries start. Never apply a subset: golang-migrate never goes back for a lower number once a higher one is recorded.

| Track | Up to | Notable |
|---|---|---|
| document | **000114** | 000104 outbox drain definer; 000106 legal holds; **000107 `upload_sessions.storage_bucket/storage_key` — must precede the storage binary**, because every upload fails without these columns; 000108 FORCE RLS; 000111–000113 definers; **000114 `_sedoc.source` backfill** (data only; holds row locks on the marked rows until it commits) |
| auth | 000004 | 000003 `auth_session_identity` — every non-auth service answers 503 on session validation without it |
| audit | 000006 | partition RLS + definers |
| notification | 000004 | push devices / prefs RLS |
| connector | 000002 | OAuth state nonce |
| billing | 000012 | Stripe customer map RLS |
| intelligence | 000006 | FORCE RLS |
| search | 000004 | — |
| task | 000002 | — |

How the migrations run on each path:

- **Helm.** `helm upgrade` runs the migrate Job as a **pre-upgrade hook**, so the schema moves before any Deployment rolls. Follow it with `kubectl logs -f job/sedoc-migrate-<revision>`.
- **Compose prod.** The `migrate` service runs first; every app service waits for it to complete (`service_completed_successfully`).
- **Manual.** Run `scripts/migrate-all.sh` with `SEDOC_DB_BOOTSTRAP_ROLES=1`, `DATABASE_URL` set to the owner, and `PGPASSWORD`. It runs the tracks in dependency order, each with its own bookkeeping table.

## 5. JetStream durables

- **audit, search, notification, policy.** Their consumers are recreated with a deliver group on the first start of the new build. Roll out **one replica of each first**: two replicas upgrading the same legacy durable at once can strand one of them on a dead subject until restart. To **roll back**, delete those durables before starting the old build, which cannot bind to grouped durables (see `docs/runbooks/05-nats-topology.md`).
- **preview (`preview-svc` on `DOC_EVENTS`).** The consumer config changed. An existing `preview-svc` durable with the old config must be deleted once (`nats consumer rm DOC_EVENTS preview-svc`).
  - The new durable uses `DeliverPolicy.ALL`, so on first start it **replays the whole DOC_EVENTS backlog**, and every retained `dms.version.uploaded.v1` re-renders a preview.
  - On a large deployment, plan for that load, or create the durable yourself with `deliver_policy: new` before starting the preview worker and backfill previews separately.

## 6. Roll out

- **Helm.** Run `helm upgrade` with the values from step 1. Watch the hook, then the pods.
- **Compose prod.**

  ```sh
  docker compose -f docker-compose.yml -f docker-compose.prod.yml pull
  docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
  ```

## Dev box (host-mode `make run-all`)

The document service treats an unset or blank `SEDOC_ENVIRONMENT` as not-dev for its dev-only secret fallbacks: the OnlyOffice JWT, the zt pepper, the IRM key and the DSR salt. So on a host-mode run (`make run-all` sources `.env`), those four features answer 503 until `.env` says dev.

- `.env.example` now ships `SEDOC_ENVIRONMENT=dev`, so `make setup` / `scripts/gen-dev-env.sh` (which copy it) write `dev`.
- **An existing `.env` needs the line added by hand:**

  ```sh
  grep -q '^SEDOC_ENVIRONMENT=' .env && sed -i 's/^SEDOC_ENVIRONMENT=.*/SEDOC_ENVIRONMENT=dev/' .env || echo 'SEDOC_ENVIRONMENT=dev' >> .env
  ```

- The compose dev stack is unaffected, because it defaults to dev.

## 7. Verify

- `curl https://<domain>/api/v1/health` returns 200. This is auth's dependency-checked `/readyz`, through Kong.
- Log in.
- Upload a document. This exercises 000107, storage, the KEK and the presigned URL host (`SEDOC_S3_PUBLIC_BASE`).
- Open a preview and run a search.
- `POST /api/v1/audit/verify-integrity` reports `valid: true` for a re-baselined tenant.
