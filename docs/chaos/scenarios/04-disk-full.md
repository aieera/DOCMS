# Chaos 04 — Postgres disk full

## Premise

Fill `/var/lib/postgresql` on the primary to 98 % and hold
for 10 minutes. Writes must throttle; Postgres must not
crash-loop.

## Setup

- Chaos-mesh CRD: `IOChaos` + filesystem fill helper (not a
  first-class primitive; use a sidecar `dd if=/dev/zero` to a
  temp file until the volume is 98 %).
- k6 scenario 03-upload at steady 50 uploads/min.
- Set Postgres `log_checkpoints = on` + WAL archiving to a
  separate volume so checkpointing isn't blocked by the fill.

## Verification

- `pg_stat_wal` shows WAL pressure; `CHECKPOINT` runs on
  schedule.
- Writes slow: `upload_initiate_duration_seconds` p95 spikes
  to > 1 s. Requests still complete (no 5xx flood).
- Postgres process stays up. `SELECT 1` returns within 1 s.

## Expected behaviour

- `autovacuum_vacuum_scale_factor` aggressive enough that dead
  tuples drop and disk pressure eases.
- Once we unfill the volume, writes resume normal latency
  within 60 s.

## Fail-the-test triggers

- Postgres crash or forced restart.
- Commits fail with "no space left" — means the throttle didn't
  engage in time.
- WAL archiving stalls (WAL segments pile up on the data
  volume because the archive destination was on the same
  volume — ops misconfiguration).
