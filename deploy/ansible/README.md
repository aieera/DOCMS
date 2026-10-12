# SeDoc — Ansible bundle

Bare-metal / VM installer for SeDoc (ADR 0093, §13.1). The Helm
chart at [`../helm/`](../helm/) is the supported on-prem path for
customers with Kubernetes; this Ansible bundle is for everyone else.

## Status

**Foundation only — NOT production-complete.** See ADR 0093 §"Scope"
for the full picture. What ships here:

- `common` role (user, paths, firewall restricted to the app CIDR,
  logrotate)
- `postgres` role (full, idempotent, RHEL 8/9 + Ubuntu 22.04/24.04;
  TLS, `hostssl` only, owner role `vaultdms` + runtime role `dms_app`)
- `sedoc-migrate` role (every service's migrations, in
  `scripts/migrate-all.sh` order, as the postgres superuser over the
  local socket on the primary DB host)
- `sedoc-service` role (templated systemd-unit installer reusable
  for all 14 Go services; checksum-verified binaries; connects as
  `dms_app` with `sslmode=verify-full`)
- `playbooks/site.yml` umbrella + `postgres.yml` + `migrate.yml` +
  `document.yml` + `upgrade.yml`

What's deferred to future commits:

- Roles for Redis, OpenSearch, NATS, MinIO, Temporal, ClamAV
- Per-service blocks in `site.yml` for the other 13 Go services
- Cert provisioning (Let's Encrypt or org-CA)
- Production backup + restore role (today: only a daily local pg_dump
  cron — `/var/backups/vaultdms/vaultdms-YYYYMMDD.dump`, 0700 postgres,
  `postgres_backup_retention_days` days kept; copy them off-host)
- Gateway drain/re-attach hooks in `upgrade.yml`
- molecule + Vagrant CI matrix covering all 4 supported OS variants
- Subject erasure / DSR-export role

The runbook ([docs/runbooks/ansible-install.md](../../docs/runbooks/ansible-install.md))
documents the operational gaps.

## Quickstart (single-host all-in-one)

```bash
cd deploy/ansible

# 1. Copy + edit the inventory
cp inventories/example.ini inventories/prod.ini
# Put your host(s) in each tier group.

# 2. Create the vault file. Every name listed under "Secrets" in
#    group_vars/all.yml is required; the play stops on the first one
#    that is missing or empty.
ansible-vault create group_vars/all.vault.yml
# Also set in group_vars (not secret): vaultdms_db_ca_cert,
# vaultdms_license_public_key, vaultdms_region, vaultdms_app_cidr,
# vaultdms_service_allowed_cidrs (add the gateway/LB CIDR),
# vaultdms_trusted_proxies, vaultdms_signer_tsa_url.

# 3. Drop your vault password (chmod 600)
echo "YOUR_VAULT_PASSWORD_HERE" > .vault-password
chmod 600 .vault-password

# 4. Smoke-check connectivity
ansible -i inventories/prod.ini vaultdms -m ping

# 5. Install
ansible-playbook -i inventories/prod.ini playbooks/site.yml
```

## Layout

```
deploy/ansible/
├── ansible.cfg
├── inventories/
│   ├── example.ini      ← committed template
│   ├── prod.ini         ← gitignored; your real hosts
│   └── README.md
├── group_vars/
│   ├── all.yml          ← committed defaults
│   └── all.vault.yml    ← gitignored, encrypted with ansible-vault
├── playbooks/
│   ├── site.yml         ← umbrella
│   ├── postgres.yml     ← DB tier only
│   ├── document.yml     ← document service only
│   └── upgrade.yml      ← rolling app-tier upgrade
└── roles/
    ├── common/
    ├── postgres/
    └── sedoc-service/   ← templated; reused per service
```

## Adding a new service

Each of the 14 Go services follows the same pattern. To add
`signature` to `site.yml`:

```yaml
- name: Application services (signature)
  hosts: app
  become: true
  roles:
    - role: sedoc-service
      vars:
        vaultdms_service_name: signature
        vaultdms_service_http_port: 8080
        vaultdms_service_health_port: 8081
        # signature doesn't expose gRPC
        vaultdms_service_grpc_port: 0
```

Per-service env overrides go in `vaultdms_service_extra_env`:

```yaml
- role: sedoc-service
  vars:
    vaultdms_service_name: storage
    vaultdms_service_extra_env:
      SEDOC_S3_ACCESS_KEY: "{{ vault_minio_access_key }}"
      SEDOC_S3_SECRET_KEY: "{{ vault_minio_secret_key }}"
```

## Upgrades

```bash
ansible-playbook playbooks/upgrade.yml \
  -e service=document \
  -e vaultdms_release=v1.1.0
```

Run it from a checkout of the release tag: the first play applies that
release's migrations on the primary DB host (the play refuses a
checkout that is not at `vaultdms_release`). Then `serial: 1` ensures
one app host at a time. Each host: checksum-verified binary download
→ atomic swap → `systemctl restart` → wait for `/healthz`. If
`/healthz` doesn't return 200 within 30 × 2 s, the playbook fails and
stops before touching the next host. Rollback is the same playbook
with the previous release tag — migrations are not rolled back.

## Upgrade notes (2026-10 review fixes)

One-time steps on the first upgrade onto a release that contains them:

- **Database roles (DEC-1).** Services now connect as `dms_app`
  (NOBYPASSRLS, owns no tables) over TLS with `sslmode=verify-full`.
  Before upgrading set `vault_db_app_password`, `vault_db_tls_cert`,
  `vault_db_tls_key` and `vaultdms_db_ca_cert`; the certificate must
  name `vaultdms_db_host`. The owner role `vaultdms` loses network
  access (pg_hba allows only `dms_app`). The legacy `dms_readonly`
  role is set NOLOGIN.
- **Audit chain cutover.** After the upgrade, run the one-time
  re-baseline in
  [docs/runbooks/audit-chain-rebaseline.md](../../docs/runbooks/audit-chain-rebaseline.md)
  (`--cutover`, needs the audit signing key). Until it runs, GDPR
  erasure returns 409.
- **Rolling back past the M-10 fix.** Delete the JetStream durable
  consumers before starting a pre-fix build, or it crash-loops.
- **Preview durable.** The `preview-svc` JetStream durable may need
  deleting once so it is recreated with the new filter.
- **Licensing.** `SEDOC_REQUIRE_LICENSE=true` by default: set
  `vault_license_jwt` and `vaultdms_license_public_key` (the release
  public key; the embedded dev key is rejected in prod).

## Backups

The `postgres` role installs a nightly `pg_dump -Fc` to
`/var/backups/vaultdms/vaultdms-YYYYMMDD.dump` (directory 0700
postgres, `postgres_backup_retention_days` days kept). Copy the files
off-host; there is no PITR.
