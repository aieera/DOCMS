# Chaos 02 — Postgres network partition

## Premise

Partition the Postgres primary from its replicas for 5 minutes.
Reads must fail over to the hot replica; writes must back-
pressure without data loss.

## Setup

- Chaos-mesh CRD: `NetworkChaos` with `action: partition`,
  `direction: both`, source: Postgres primary pod, target:
  replica pods.
- k6 scenario 01-document-crud at steady 100 docs/min.
- Seed a synthetic write just before the partition so we can
  verify the replica catches up on reconnect (no lost commit).

## Verification

- `pg_stat_replication.replay_lag` spikes then recovers.
- Service health: any pod-side `pgx` pool that fails over to
  the replica (based on Postgres endpoint routing) returns
  5xx on writes for the partition duration, `202 Accepted +
  retry-after` on the outbox path.
- Zero lost commits. The pre-partition synthetic write appears
  on the replica after reconnect.

## Expected behaviour

- Pgpool / pgbouncer at the edge routes reads to healthy
  replicas during the partition. Writes to the primary queue;
  a 10-s write timeout trips and returns 503.
- After reconnect, WAL streaming catches up within 30 s.

## Fail-the-test triggers

- Any committed write missing after the partition heals.
- Replica lag > 60 s at any point after healing.
- Service reports data corruption (panic, checksum mismatch).
