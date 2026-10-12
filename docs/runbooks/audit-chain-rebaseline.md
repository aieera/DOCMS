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
| 5 | a tenant was refused. Nothing was written or signed for it. Investigate; do not re-baseline. The causes are: differing events at or after the cutover; a chain whose latest checkpoint signature does not verify with this key; a chain that no longer carries its latest checkpoint's signed prefix; or a tenant with no checkpoint at all when `--allow-first-checkpoint` was not given (its output line says `no_checkpoint=true`). |

Each tenant is processed in one transaction under its audit-chain advisory lock, so live ingest simply waits for it. The rewrite, the marker and the checkpoint commit together or not at all. Every tenant is logged, with event count, number of differing events, old and new head hash, and whether a checkpoint was written. The exit code is non-zero if any tenant failed.

## What gets written (with `--apply`)

The tool writes three things, in **one transaction**. If any part fails, including the checkpoint insert, none of it is written and the chain is left as it was:

- **Rewritten links.** Every event whose stored `previous_hash`/`event_hash` differs from the verifier's own computation is rewritten to the canonical form. Event content is never changed.
- **An `audit.chain_rebaselined` marker event.** It is appended to the chain with `actor_name` set to `--operator`, and its details record the old head hash, the new head hash, the rewritten count, the operator and the cutover. It is informational: it tells a reader of the trail that this was an authorised cutover. The tool never trusts it, because anyone with database write access could insert such a row.
- **A signed checkpoint** over the new head, made with `SEDOC_AUDIT_SIGNING_KEY` before the transaction commits. The key is mandatory for `--apply`: without a valid key the tool exits with code 4 before touching any tenant, because a re-baselined chain with no signed checkpoint would be a tail nobody certified. Older checkpoints sign heads that no longer exist; only the latest is checked.

### Re-running

A re-run with `--apply` rewrites nothing for a tenant whose chain already verifies. If the latest checkpoint already covers the head, nothing is written. Otherwise the tool may sign a new checkpoint, but a chain that verifies internally is not enough on its own: someone with database access but no signing key can rewrite every hash consistently. So it signs only when both of these hold:

- the latest checkpoint's signature verifies with the configured key, and
- the chain still contains that checkpoint's signed prefix (the event at position `event_count` carries the signed head hash), so events were only appended after it.

Anything else, such as a signature that does not verify with this key or a changed or truncated prefix, refuses the tenant with exit code 5 and nothing is signed. **Investigate; do not re-baseline.**

### A tenant with no checkpoint (`--allow-first-checkpoint`)

A tenant with no checkpoint at all cannot be cross-checked against anything signed. Its chain looks exactly like one whose checkpoints were deleted. By default such a tenant is refused (exit code 5), and its output line says `no_checkpoint=true` so the case is visible rather than silently signed. This applies when the chain needs no rewrite. A tenant whose chain is re-baselined gets its checkpoint with the rewrite.

`--allow-first-checkpoint` signs the first checkpoint for such tenants. Treat it as an **incident-level decision**. First confirm that the tenant never had checkpoints, for example because signing was only enabled recently, and that none were deleted. Record that decision in the change ticket. Then run it for that tenant alone:

```sh
go run ./cmd/rebaseline --apply --operator "jane.doe@example.com (INC-5678)" \
    --cutover 2026-10-14T09:30:00Z --tenant <uuid> --allow-first-checkpoint
```

### After a signing-key rotation

The audit service checks checkpoints against one key only; it has no previous-key (`_PREV`) support. After rotating `SEDOC_AUDIT_SIGNING_KEY`, every existing checkpoint fails verification with the new key, so a re-run would refuse each tenant with exit code 5. **Wait for a fresh checkpoint signed by the service with the new key** (`POST /api/v1/audit/checkpoint` for the tenant) before re-baselining. Make sure the tool runs with the same new key.

## Rollback

There is none: this is a one-way cutover. The old hashes are recorded only as the old head in the marker event. Before applying, take a database backup if you need to keep the pre-cutover hashes as evidence.

## Known gap

Tenants are enumerated from active (not deleted) `organizations`. Audit rows that belong to a deleted organization are not re-baselined.
