# Inventory layout

Every host belongs to exactly one **tier** group:

| Group | What runs | Sizing guidance |
|---|---|---|
| `db` | PostgreSQL 16 | 4 vCPU / 8 GB RAM minimum; SSD; primary + standby for HA |
| `cache` | Redis 7 | 2 vCPU / 4 GB RAM; one host is fine for dev, sentinel/cluster for prod |
| `search` | OpenSearch 2.12 | 4 vCPU / 16 GB RAM minimum; 3-node cluster recommended |
| `messaging` | NATS 2.10 + JetStream | 2 vCPU / 4 GB RAM; 3-node cluster for HA |
| `blob` | MinIO | 2 vCPU / 4 GB RAM; 4+ drives or distributed mode for production |
| `workflow` | Temporal | 2 vCPU / 4 GB RAM; uses the `db` Postgres for persistence |
| `antimalware` | ClamAV | 2 vCPU / 4 GB RAM (heavy on memory during scans) |
| `app` | All 14 Go services as systemd units | 4 vCPU / 8 GB RAM minimum |

Aggregate group `vaultdms` (declared via `[vaultdms:children]`) lets you target
"every host this project runs on" for the `common` baseline role.

## Files in this directory

- `example.ini` — template, committed. Copy to `prod.ini` (gitignored) and fill in.
- `prod.ini` — your real inventory. **Never commit.**

## Variable precedence

Inventory vars come from (low → high):

1. `group_vars/all.yml` — defaults for everything
2. `group_vars/<group>.yml` — per-tier overrides
3. `host_vars/<hostname>.yml` — per-host overrides
4. `--extra-vars` on the CLI — highest

## Vault

Secrets (DB passwords, KEK material, third-party API keys) live in
`group_vars/all.vault.yml` encrypted with `ansible-vault`. Plaintext copy
of the vault password goes in `deploy/ansible/.vault-password` (chmod 600,
gitignored).
