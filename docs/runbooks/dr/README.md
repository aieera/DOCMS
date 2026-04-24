# Disaster Recovery

Companion files to [../10-disaster-recovery.md](../10-disaster-recovery.md)
(the canonical comprehensive runbook). This directory holds the
artifacts the canonical runbook pre-supposes:

- [per-tenant-restore.md](./per-tenant-restore.md) — restore a single
  tenant's state without touching anyone else's.
- [tabletop-template.md](./tabletop-template.md) — structured
  tabletop exercise (no cluster touch; just comms + decisions).
- [rehearsals/](./rehearsals/) — one dated file per executed drill.
  Each file is signed by the incident commander + a peer reviewer.

## RPO / RTO

Canonical source is `../10-disaster-recovery.md` §"RTO / RPO targets".
Proposed headline numbers:

| Tier | RPO | RTO |
|---|---|---|
| **Platform-wide** (default) | 15 min | 4 h |
| Postgres primary | 5 min (PITR) | 1 h |
| NATS JetStream | 1 min | 15 min |
| Temporal history | 15 min | 1 h |

Per-store variations are tighter than the platform headline —
platform RTO of 4 h assumes the slowest store (OpenSearch reindex) is
the critical path, which it usually isn't on real incidents.

## Cadence

- **Tabletop** — monthly (1 h). No cluster touch; exercises comms +
  decisions only. Owner rotates through the on-call roster.
- **Postgres PITR drill** — quarterly (4 h window). Against staging.
- **Full region failover** — biannual (1-day window). Against
  staging. Measures true cluster-wide RTO.

Rehearsal outputs land under [rehearsals/](./rehearsals/) named
`YYYY-MM-DD-<type>.md` (e.g. `2026-07-15-pitr-drill.md`).
