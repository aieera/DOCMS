# Audit chain re-baseline (one-time cutover)

## When

Run this once per deployment that wrote audit events before the canonical-hash fix (audit commit `07f13d71`, review finding M-14/H-12).

Those chains hashed the raw event envelope, but verification re-reads the stored `jsonb`, so they never verify. Two symptoms show it:

- `POST /api/v1/audit/verify-integrity` reports `valid: false` from an early event.
- GDPR erasure (`POST /api/v1/audit/data-subject/anonymize`) answers **409 `audit_chain_unverifiable`**.

A chain that already verifies needs nothing. The tool reports `differing=0` and writes nothing.

If a tenant's chain is unverifiable but was written after the fix, investigate tampering first. Do not re-baseline it.

## How

The tool rewrites append-only (WORM) rows. Run it with the **migration/owner DSN**: a superuser or a `BYPASSRLS` role. It refuses any other role, including `dms_app`, and exits with code 3. With `--apply` it also requires `SEDOC_AUDIT_SIGNING_KEY` (exit code 4 without it).

```sh
cd services/audit
export SEDOC_MIGRATE_DATABASE_URL='postgres://<owner>:<pw>@<host>:5432/<db>?sslmode=require'
# Required for --apply (the run refuses with exit code 4 before touching
# any tenant if it is unset or invalid). A dry run works without it but
# prints a note that apply will need it.
export SEDOC_AUDIT_SIGNING_KEY='<same base64 seed the audit service uses>'

# 1. Dry run (the default): reports per tenant, writes nothing.
go run ./cmd/rebaseline
# or one tenant:
go run ./cmd/rebaseline --tenant <uuid>

# 2. Apply, recording who ran it and the cutover (see below):
go run ./cmd/rebaseline --apply --operator "jane.doe@example.com (change CHG-1234)" \
    --cutover 2026-10-14T09:30:00Z

# 3. Confirm: a second run reports differing=0, and verify-integrity is valid.
go run ./cmd/rebaseline
```

### The cutover (`--cutover`, required with `--apply`)

`--cutover` is the moment the canonical-hash fix went live: the deploy time of the audit service build that contains commit `07f13d71`. Take it from your deploy record, for example the rollout timestamp in the CD log or `kubectl rollout history`, or from the first audit-service pod start of that release. If unsure, use the earliest time that build could have been running; an early cutover is the safe direction.

Events written after the cutover were hashed canonically and must already verify. So for each tenant the tool lists every event that does not recompute (count, first and last `id@created_at`, and how many are at or after the cutover). Pass `--cutover` to the dry run as well to see that last number before you apply.

**Exit code 5: a tenant was refused.** Some event at or after the cutover does not verify. That is not pre-fix hashing; it is tampering or a bug. **Investigate. Do not re-baseline that tenant**, and do not move the cutover later just to get past the refusal. Refused tenants get nothing written. Other tenants in the same run are processed normally.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | a tenant failed |
| 2 | usage error, e.g. `--apply` without `--operator` or `--cutover` |
| 3 | not a BYPASSRLS/owner role |
| 4 | `--apply` without a valid signing key |
| 5 | a tenant was refused: differing events at or after the cutover, or (on a re-run) a chain head the latest signed checkpoint does not vouch for. Investigate; do not re-baseline. |

Each tenant is processed in one transaction under its audit-chain advisory lock, so live ingest simply waits for it. Every tenant is logged, with event count, number of differing events, old and new head hash, and whether a checkpoint was written. The exit code is non-zero if any tenant failed.

## What gets written (with `--apply`)

The tool writes three things:

- **Rewritten links.** Every event whose stored `previous_hash`/`event_hash` differs from the verifier's own computation is rewritten to the canonical form. Event content is never changed.
- **An `audit.chain_rebaselined` marker event.** It is appended to the chain with `actor_name` set to `--operator`, and its details record the old head hash, the new head hash, the rewritten count, the operator and the cutover. Anyone reading the trail can tell this authorised cutover apart from tampering.
- **A signed checkpoint** over the new head, made with `SEDOC_AUDIT_SIGNING_KEY`. The key is mandatory for `--apply`: without a valid key the tool exits with code 4 before touching any tenant, because a re-baselined chain with no signed checkpoint would be a tail nobody certified. Older checkpoints sign heads that no longer exist; only the latest is checked.

### Re-running

A re-run with `--apply` rewrites nothing for a tenant whose chain already verifies. However, if the latest signed checkpoint does not cover the current head, the tool may sign a new checkpoint. A chain that verifies internally is not enough on its own, because someone with database access but no signing key can rewrite every hash consistently. So the tool signs only when one of these holds:

- **Extension.** The latest checkpoint's signature verifies with the configured key, and the chain still contains its signed prefix: the event at position `event_count` carries the signed head hash. Only events were appended after it.
- **Repair.** The latest checkpoint's signature verifies, its prefix no longer matches, but an `audit.chain_rebaselined` marker sits after the events it covered. This is a re-baseline whose own checkpoint failed after the rewrite committed.
- **First checkpoint.** The tenant has no checkpoint at all and its chain fully verifies.

In every other case (a signature that does not verify with this key, or a changed or truncated prefix with no re-baseline marker after it) the tenant is refused with exit code 5 and nothing is signed. **Investigate; do not re-baseline.**

The first-checkpoint case cannot be cross-checked against anything signed. If checkpoints were expected for a tenant and none exist, treat that as an incident before you run `--apply`.

## Rollback

There is none: this is a one-way cutover. The old hashes are recorded only as the old head in the marker event. Before applying, take a database backup if you need to keep the pre-cutover hashes as evidence.

## Known gap

Tenants are enumerated from active (not deleted) `organizations`. Audit rows that belong to a deleted organization are not re-baselined.
