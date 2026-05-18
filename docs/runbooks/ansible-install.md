# Ansible install / upgrade / rollback runbook

*ADR 0093 — Ansible bundle at `deploy/ansible/`. Companion to the
chart-side runbook at `helm-install.md`.*

This runbook is operational. Read ADR 0093 first if you want to know
why the bundle is shaped the way it is.

---

## Prerequisites

- Ansible 2.15+ on the control node (`ansible --version`)
- Required collections (install once):
  ```bash
  ansible-galaxy collection install community.postgresql ansible.posix community.general
  ```
- SSH access to every target host as a user that can `sudo`
- A vault password for `ansible-vault` (any string; store somewhere safe)
- Target hosts: RHEL 8/9 or Ubuntu 22.04/24.04, fresh installs

---

## First-time install

### 1. Inventory

```bash
cd deploy/ansible
cp inventories/example.ini inventories/prod.ini
```

Edit `prod.ini` — fill in real hosts per tier:

```ini
[db]
db01.internal ansible_host=10.0.1.10

[cache]
cache01.internal ansible_host=10.0.1.20

[app]
app01.internal ansible_host=10.0.2.10
app02.internal ansible_host=10.0.2.11
```

Smaller deploys can put every tier on one host:

```ini
[db]
host01.internal

[cache]
host01.internal

[search]
host01.internal

[app]
host01.internal
```

### 2. Secrets

```bash
# Set up the vault password file
echo "your-vault-password-here" > .vault-password
chmod 600 .vault-password

# Create the encrypted secrets file
ansible-vault create group_vars/all.vault.yml
```

Inside the editor, paste:

```yaml
vault_db_password:           "<generate: openssl rand -hex 24>"
vault_local_kek:             "<generate: openssl rand -hex 32>"
vault_gateway_secret:        "<generate: openssl rand -base64 48>"
vault_internal_api_key:      "<generate: openssl rand -hex 32>"
vault_esign_state_hmac:      "<generate: openssl rand -hex 32>"
vault_stripe_webhook_secret: "<paste from Stripe dashboard>"
```

### 3. Smoke test connectivity

```bash
ansible -i inventories/prod.ini vaultdms -m ping
```

Expected: every host returns `pong`.

If you get permission denied, check `ansible_user` + sudo:

```bash
ansible -i inventories/prod.ini vaultdms -m ping -u root -k
```

### 4. Dry-run

```bash
ansible-playbook -i inventories/prod.ini playbooks/site.yml --check --diff
```

This shows what WOULD change without applying. Review the diff
carefully — pay attention to `pg_hba.conf` and the systemd unit
files.

### 5. Apply

```bash
ansible-playbook -i inventories/prod.ini playbooks/site.yml
```

First run: 5-15 minutes depending on network speed (PGDG repo
download is the slow part).

### 6. Verify

```bash
# Postgres
ansible -i inventories/prod.ini db -a "systemctl status postgresql-16"
ansible -i inventories/prod.ini db -a "sudo -u postgres psql -c '\\du vaultdms'"
#   → expect rolname=vaultdms, rolbypassrls=f

# document service
ansible -i inventories/prod.ini app -a "systemctl status vaultdms-document"
ansible -i inventories/prod.ini app -a "curl -fsS http://localhost:8081/healthz"
```

---

## Adding a service

The bundle ships `document` as the worked example. To add `signature`
to a running deploy:

```bash
ansible-playbook -i inventories/prod.ini playbooks/document.yml \
  -e vaultdms_service_name=signature \
  -e vaultdms_service_http_port=8080 \
  -e vaultdms_service_health_port=8081
```

Or edit `playbooks/site.yml` and add a permanent block (per ADR 0093
§"Adding a new service"). Re-run `site.yml` and only the new service
changes — Ansible's idempotency handles the rest.

---

## Upgrade

Rolling release across all `app` hosts, one at a time:

```bash
ansible-playbook -i inventories/prod.ini playbooks/upgrade.yml \
  -e service=document \
  -e vaultdms_release=v1.1.0
```

For each host the playbook:

1. Downloads the new binary to `/opt/vaultdms/bin/document.new`
2. `cmp -s` against the live binary; only moves if different
3. `daemon-reload` + restart the systemd unit
4. Polls `/healthz` for up to 60 s; fails the playbook if it doesn't
   come back green

If `/healthz` fails, the next host is NOT touched. Inspect the failed
host:

```bash
ssh app01.internal
journalctl -u vaultdms-document -n 100
```

### Rollback

```bash
ansible-playbook -i inventories/prod.ini playbooks/upgrade.yml \
  -e service=document \
  -e vaultdms_release=v1.0.0     # previous tag
```

Same flow, going backwards. Idempotent.

⚠️ **Rollback does NOT undo DB migrations.** If the upgrade ran a
schema migration that the previous binary can't read, you need a
hand-rolled `migrate-down` step before the rollback playbook.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `ansible -m ping` fails with "Permission denied" | Wrong `ansible_user` or no sudo | Check `inventories/prod.ini` `[vaultdms:vars]` block + `ssh user@host sudo -n true` |
| `community.postgresql.postgresql_user` fails with "psycopg2 not found" | Forgot to install the python module | `apt install python3-psycopg2` / `dnf install python3-psycopg2` on the DB host, NOT the control node |
| Postgres install hangs on "Disable distro module" (RHEL) | Already disabled in a prior partial run | Manually `rm /etc/dnf/modules.d/postgresql.module` and re-run |
| Systemd unit `Active: failed (Result: exit-code)` | Service crashed reading bad config | `journalctl -u vaultdms-document -n 100` — usually a missing env var or a Postgres password mismatch in `/etc/vaultdms/document.env` |
| `vault_db_password` undefined error | Forgot to populate `group_vars/all.vault.yml` or `.vault-password` doesn't decrypt it | Re-run `ansible-vault edit group_vars/all.vault.yml` and check `--vault-password-file=.vault-password` is being read |
| `/healthz` returns 200 but the service can't reach Postgres | App can route to DB but credentials are wrong | `journalctl -u vaultdms-document` will show `ping database: password authentication failed` — re-check `vault_db_password` matches what's in Postgres + re-render the env file |

---

## What's NOT in this bundle (per ADR 0093)

- Redis / OpenSearch / NATS / MinIO / Temporal / ClamAV roles
- 13 of the 14 Go services in `site.yml` (mechanical to add)
- Certificate provisioning
- Backup beyond a daily local `pg_dump` cron
- Gateway drain/re-attach hooks during upgrade
- Tested CI matrix for all 4 OS variants

For each, either run the component out of band (e.g. install Redis
via your distro's package manager + point `vaultdms_cache_host` at
it in `group_vars/all.yml`), or wait for the follow-up commits that
add the missing roles.

---

## Disaster recovery

The daily cron in the `postgres` role writes
`/var/backups/vaultdms-YYYYMMDD.dump` on the DB host. Recovery:

```bash
# Get the dump off the DB host
scp db01.internal:/var/backups/vaultdms-20260518.dump /tmp/

# On a fresh DB host (or after a wipe)
ansible-playbook -i inventories/prod.ini playbooks/postgres.yml
scp /tmp/vaultdms-20260518.dump db01.internal:/tmp/
ansible db -a "sudo -u postgres pg_restore -d vaultdms --clean --if-exists /tmp/vaultdms-20260518.dump"
```

This is **NOT** a full DR strategy — there's no PITR, no offsite
storage, no automated restore test. Implementing one is the
"production backup role" deferred item in ADR 0093.
