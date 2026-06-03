# SeDoc — Ansible bundle

Bare-metal / VM installer for SeDoc (ADR 0093, §13.1). The Helm
chart at [`../helm/`](../helm/) is the supported on-prem path for
customers with Kubernetes; this Ansible bundle is for everyone else.

## Status

**Foundation only — NOT production-complete.** See ADR 0093 §"Scope"
for the full picture. What ships here:

- `common` role (user, paths, firewall, logrotate)
- `postgres` role (full, idempotent, RHEL 8/9 + Ubuntu 22.04/24.04)
- `sedoc-service` role (templated systemd-unit installer reusable
  for all 14 Go services)
- `playbooks/site.yml` umbrella + `postgres.yml` + `document.yml` +
  `upgrade.yml`

What's deferred to future commits:

- Roles for Redis, OpenSearch, NATS, MinIO, Temporal, ClamAV
- Per-service blocks in `site.yml` for the other 13 Go services
- Cert provisioning (Let's Encrypt or org-CA)
- Production backup + restore role (today: only daily local pg_dump cron)
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

# 2. Create the vault file (passwords + KEK + gateway secret)
ansible-vault create group_vars/all.vault.yml
# Inside, set: vault_db_password, vault_local_kek, vault_gateway_secret, …

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

`serial: 1` ensures one host at a time. Each host: atomic binary
swap → `systemctl restart` → wait for `/healthz`. If `/healthz`
doesn't return 200 within 30 × 2 s, the playbook fails and stops
before touching the next host. Rollback is the same playbook with
the previous release tag.
