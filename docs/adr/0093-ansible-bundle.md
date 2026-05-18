# ADR 0093 — Ansible bundle for bare-metal / VM installs (§13.1)

**Status:** Accepted (foundation only — see Scope below).

**Date:** 2026-05-18

## Context

Blueprint §13.1 calls for an Ansible bundle as the bare-metal / VM
counterpart to the Helm chart. The playbook entry's "ADR 0084"
reference is stale (taken by grouped-autocomplete-suggester since
search work). This ADR is **0093**.

The Helm chart at `deploy/helm/vaultdms/` (ADR 0092) covers customers
who run Kubernetes. The Ansible bundle covers everyone else:
on-prem deployments to RHEL 8/9 or Ubuntu 22.04/24.04 VMs, regulated
environments where K8s isn't approved, and small-scale single-host
installs.

This ADR ships the **foundation**, not the full production-grade
bundle. The full deliverable described in the playbook is 2-3 weeks
of focused work spread across role authorship + cross-OS testing +
backup/cert/log-rotation policy + per-OS CI matrix. What lands today
demonstrates the design and gives operators a working starting point
they can pattern-match against.

## Scope

### Shipped today

- **`deploy/ansible/`** directory scaffold with `ansible.cfg`,
  inventory template, group_vars defaults, vault-password handling.
- **`common`** role: vaultdms user/group, FHS-compliant paths
  (`/opt/vaultdms`, `/etc/vaultdms`, `/var/lib/vaultdms`,
  `/var/log/vaultdms`), firewall rules for inter-service ports
  (firewalld on RHEL, ufw on Ubuntu), `/etc/logrotate.d/vaultdms`.
- **`postgres`** role done fully: PGDG repo install, initdb,
  `postgresql.conf` + `pg_hba.conf` templates, application DB + role
  provisioning, `ALTER ROLE NOBYPASSRLS` to keep the multi-tenant RLS
  invariant honest (CLAUDE.md), daily `pg_dump` cron. Idempotent
  across RHEL 8/9 + Ubuntu 22.04/24.04.
- **`vaultdms-service`** role: templated systemd-unit installer
  reusable for all 14 Go services. Atomic binary swap (`.new` →
  `cmp -s` → `mv`) so rolling upgrades either succeed or leave the
  prior binary untouched. Restart-on-failure with start-limit-burst
  ceiling. Aggressive `systemd` hardening (`NoNewPrivileges`,
  `ProtectSystem=strict`, `ProtectHome=true`, `PrivateTmp=true`,
  `MemoryDenyWriteExecute=true`, etc.) — same intent as the Helm
  chart's PSS-restricted securityContext.
- **`playbooks/site.yml`** umbrella (calls `common` → `postgres` →
  document). Other 13 Go services are listed in comments with the
  exact pattern.
- **`playbooks/postgres.yml`** for DB-only rebuilds.
- **`playbooks/document.yml`** for one-service rollouts.
- **`playbooks/upgrade.yml`** rolling-upgrade with `serial: 1` and
  per-host `/healthz` wait.
- **Runbook** at `docs/runbooks/ansible-install.md`.

### Deferred (named explicitly)

| Item | Why deferred |
|---|---|
| Roles for **Redis, OpenSearch, NATS, MinIO, Temporal, ClamAV** | Each is 3-5 days of authorship + testing. `postgres` is the worked example demonstrating the pattern (PGDG repo for both OS families, idempotent initdb, template-rendered config, `community.postgresql` for provisioning, daily backup cron). Apply the same shape per role. |
| Per-service playbook blocks for the **other 13 Go services** | One-line additions to `site.yml` per service. The `vaultdms-service` role accepts service name + ports as vars; expanding `site.yml` is repetitive but mechanical. |
| **Certificate provisioning** (Let's Encrypt + acme.sh, or org-CA) | Org-specific policy decision. Bundle is OS-installer; cert lifecycle usually owned by a separate ops surface. |
| **Production backup role** (pg_dump → S3 + retention + restore drill + MinIO mirror + OpenSearch snapshot repo + NATS JetStream state snapshot) | Today's cron entry covers the local-disk daily dump only. Proper offsite backup is its own 2-day workstream. |
| **Gateway drain/re-attach hooks** in `upgrade.yml` | Requires Kong DBless config in `deploy/gateway/kong.yaml` to grow opt-in drain semantics first. Without it, rolling upgrade still works but in-flight requests on the host being swapped will fail. |
| **molecule + Vagrant CI matrix** covering all 4 OS variants | Per-OS Vagrant boxes + molecule scenarios; ~1 day. |
| **Subject erasure / DSR-export role** (Wave 12.6 equivalent) | Lower priority — application services already implement the DSR endpoint via REST. |

## Decisions

### Role layout: tier-based, not service-based

Inventory groups hosts by **tier** (`db`, `cache`, `search`,
`messaging`, `blob`, `workflow`, `antimalware`, `app`) rather than by
service name. Two reasons:

1. Most small/medium deploys put multiple Go services on the same
   `app` host — service-keyed groups would force a multiplication of
   identical host entries.
2. Tiers map 1:1 to the Helm chart's subcharts, so operators moving
   between the two distributions reason about the same partition.

A future deploy that wants per-service hosts can use Ansible's group
intersection: `hosts: app:&signature-hosts`.

### Single templated service role, not 14 roles

The `vaultdms-service` role takes `vaultdms_service_name` as a
variable. All 14 Go services share the same install shape (download
binary → render `.env` → render unit → start). Service-specific
config flows through `vaultdms_service_extra_env`. This is the same
design as the Helm chart's `goServiceDeployment` macro and shrinks
the role count from 14 to 1.

Non-Go services (intelligence Python, collaboration Node,
signature-signer Kotlin) are deferred — they need OS-specific
runtime installs (Python+pip, Node+npm, JDK) that don't fit the
single Go-binary pattern. Each gets its own role when prioritised.

### systemd hardening: matches the Helm PSS-restricted profile

Every systemd unit sets the most aggressive defaults that don't
break the service. Same fields as the Helm chart's restricted
`securityContext` (ADR 0092):

- `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome=true`
- `PrivateTmp=true`, `PrivateDevices=true`
- `RestrictSUIDSGID`, `RestrictRealtime`, `RestrictNamespaces`
- `LockPersonality`, `MemoryDenyWriteExecute`, `SystemCallArchitectures=native`
- `CapabilityBoundingSet=` (drop all)
- `ReadWritePaths=/var/lib/vaultdms` (the only writable path)

The non-Go services will need different hardening — Python needs
write access for pip + HuggingFace caches; Node needs the same for
npm caches. Same gap as the Helm chart; same deferred fix.

### Secrets: Ansible Vault today, External Secrets parity later

`group_vars/all.vault.yml` (encrypted with `ansible-vault`) holds the
DB password, KEK, gateway secret, eSign state HMAC, internal API key.
The vault password lives in `.vault-password` (chmod 600,
gitignored). This is the right answer for small deploys; for larger
ones the same kind of External-Secrets-Operator pattern from ADR 0092
applies (a future task can fetch secrets from Vault at playbook
runtime via `community.hashi_vault.vault_kv2_get`).

### Upgrade: atomic binary swap + `/healthz` gate

The `vaultdms-service` role downloads to `<bin>.new`, `cmp -s`-checks
against the live binary, and only `mv`s if they differ. The
`upgrade.yml` playbook drives this with `serial: 1` and a 30 × 2 s
`/healthz` post-check; if health doesn't recover, the playbook fails
and stops before touching the next host. Rollback is the same
playbook with the previous release tag.

This does NOT yet drain the host from the gateway upstream before the
swap, so in-flight requests on the swapped host fail. Flagged in the
playbook as a TODO; needs gateway-side support.

## File map

| Path | Purpose |
|---|---|
| `deploy/ansible/ansible.cfg` | Inventory + roles_path + vault-password file |
| `deploy/ansible/inventories/example.ini` | Tier-grouped inventory template |
| `deploy/ansible/inventories/README.md` | Sizing guidance + variable precedence |
| `deploy/ansible/group_vars/all.yml` | Defaults: versions, paths, network, secrets-by-reference |
| `deploy/ansible/playbooks/site.yml` | Umbrella |
| `deploy/ansible/playbooks/postgres.yml` | DB tier only |
| `deploy/ansible/playbooks/document.yml` | One-service install |
| `deploy/ansible/playbooks/upgrade.yml` | Rolling upgrade, `serial: 1` |
| `deploy/ansible/roles/common/tasks/main.yml` | User, paths, firewall, logrotate |
| `deploy/ansible/roles/postgres/{tasks,handlers,templates,defaults,meta}` | Full Postgres role |
| `deploy/ansible/roles/vaultdms-service/{tasks,handlers,templates,defaults,meta}` | Templated Go-service installer |
| `deploy/ansible/README.md` | Operator-facing quickstart |
| `docs/runbooks/ansible-install.md` | Runbook |

## Acceptance

```bash
cd deploy/ansible

# Syntax + lint
ansible-playbook --syntax-check playbooks/site.yml
ansible-lint roles/

# Dry-run against a real inventory
cp inventories/example.ini inventories/prod.ini   # then edit
ansible-playbook -i inventories/prod.ini playbooks/site.yml --check --diff

# Actual install (single-host all-in-one is the fastest verify)
ansible-playbook -i inventories/prod.ini playbooks/site.yml
systemctl status vaultdms-document
curl http://localhost:8081/healthz
```

The acceptance is satisfied when `vaultdms-document.service` is
active, `/healthz` returns 200, and `psql` can connect as
`vaultdms` from the app host with NOBYPASSRLS confirmed via
`SELECT rolname, rolbypassrls FROM pg_roles WHERE rolname='vaultdms';`.

Full §13.1 acceptance (Redis, OpenSearch, NATS, MinIO, Temporal,
ClamAV up and all 14 Go services running) is **out of scope for this
ADR** and tracked as the deferred items above.
