# Tabletop exercise template

Monthly 1-hour exercise. No cluster touch — we talk through a
hypothetical, document decisions, and identify gaps in the runbook.

Fill a copy of this template as `rehearsals/YYYY-MM-DD-tabletop.md`
after the session.

## Pre-read (send 24 h ahead)

- `../10-disaster-recovery.md`
- The tabletop scenario (§"Scenario" below, picked by the facilitator).

## Roster

- **Facilitator** — reads scenario beats, takes notes, does not
  provide answers.
- **Incident Commander (IC)** — directs the exercise; the person
  currently on-call.
- **Engineering** — 2-3 engineers who'd actually run the restore.
- **Observer** — writes the rehearsal report afterward.

## Scenario

Pick ONE per session. Rotate so every scenario runs at least yearly.

### S1 — Postgres primary total loss at 03:00

The primary `postgres-0` pod is NotReady. Patroni reports lost
quorum. WAL archive is current to 02:58.

- Decide: failover vs restore.
- Walk through: §"Postgres primary loss only" in the main runbook.
- Decision checkpoint: when do we declare a customer-visible
  incident? Who owns the status-page update?

### S2 — Object-store ransomware event

MinIO primary bucket has been re-encrypted by an attacker. Hot data
is inaccessible. Versioning was enabled; prior object versions
survive. Attack timestamp best-estimate: 14:00.

- Decide: restore vs rebuild.
- Walk through: §"Object-store corruption / deletion".
- Decision checkpoint: do we invalidate + re-mint all tenant KEKs?
  (ADR 0022 says no — the bytes are fine, the wrap chain is
  untouched — but confirm.)

### S3 — Region failover

Primary region `us-east-1` is declared down by the cloud provider.
Secondary is `us-west-2`. NATS JetStream mirror was 30 s behind at
the moment of failure.

- Decide: promote secondary vs wait for primary to recover.
- Walk through: §"Total primary region loss".
- Decision checkpoint: which customer communication channel
  (status page vs per-tenant email)?

### S4 — KMS master key compromise

A security audit discovers the KMS master key was accessed by an
ex-employee. Every wrap produced since the breach is suspect.

- Decide: rotate every tenant KEK vs just the master.
- Walk through: §"KMS key compromised or scheduled for deletion".
- Decision checkpoint: do we take customer-visible downtime to
  re-wrap 100% of blobs?

### S5 — Single tenant data corruption

A bug in the retention cron hard-disposed 50 tenants that shouldn't
have been. Discovered 3 h later. Some tenants were already
crypto-shredded.

- Decide: restore order (which tenant first?).
- Walk through: [per-tenant-restore.md](./per-tenant-restore.md).
- Decision checkpoint: how do we communicate with affected
  tenants mid-restore?

## Running the exercise

1. Facilitator reads scenario (5 min).
2. IC verbally runs through the restore procedure (20 min). Peer
   engineers interject when they'd do something differently.
3. Facilitator injects **one twist** mid-exercise (10 min):
   - "Paging fails — how do you escalate?"
   - "The runbook says step 4 first but you just noticed step 3
     has a dependency on something the runbook doesn't mention."
   - "Database tools pod is missing — how do you run `psql`?"
4. Debrief (20 min): what's missing from the runbook? What's
   missing from tooling? Who owns each follow-up?
5. Observer writes the rehearsal report.

## Report outputs

- List of runbook gaps identified (each with an owner + deadline).
- List of tooling gaps (each with a backlog entry).
- Time to declared IC-ready state (target: <5 min from page).
- Sign-off: facilitator + IC.
