# Audit chain re-baseline (one-time cutover)

## When

Run this once per deployment that wrote audit events before the canonical-hash fix (audit commit `07f13d71`, review finding M-14/H-12).

Those chains hashed the raw event envelope, but verification re-reads the stored `jsonb`, so they never verify. Two symptoms show it:

- `POST /api/v1/audit/verify-integrity` reports `valid: false` from an early event.
- GDPR erasure (`POST /api/v1/audit/data-subject/anonymize`) answers **409 `audit_chain_unverifiable`**.

A chain that already verifies needs nothing. The tool reports `differing=0` and writes nothing.

If a tenant's chain is unverifiable but was written after the fix, investigate tampering first. Do not re-baseline it.

## How

The tool rewrites append-only (WORM) rows. Run it with the **migration/owner DSN**: a superuser or a `BYPASSRLS` role. It refuses any other role, including `dms_app`, and exits with code 3.

```sh
cd services/audit
export SEDOC_MIGRATE_DATABASE_URL='postgres://<owner>:<pw>@<host>:5432/<db>?sslmode=require'
export SEDOC_AUDIT_SIGNING_KEY='<same base64 seed the audit service uses>'

# 1. Dry run (the default): reports per tenant, writes nothing.
go run ./cmd/rebaseline
# or one tenant:
go run ./cmd/rebaseline --tenant <uuid>

# 2. Apply, recording who ran it:
go run ./cmd/rebaseline --apply --operator "jane.doe@example.com (change CHG-1234)"

# 3. Confirm: a second run reports differing=0, and verify-integrity is valid.
go run ./cmd/rebaseline
```

Each tenant is processed in one transaction under its audit-chain advisory lock, so live ingest simply waits for it. Every tenant is logged, with event count, number of differing events, old and new head hash, and whether a checkpoint was written. The exit code is non-zero if any tenant failed.

## What gets written (with `--apply`)

The tool writes three things:

- **Rewritten links.** Every event whose stored `previous_hash`/`event_hash` differs from the verifier's own computation is rewritten to the canonical form. Event content is never changed.
- **An `audit.chain_rebaselined` marker event.** It is appended to the chain with `actor_name` set to `--operator`, and its details record the old head hash, the new head hash, the rewritten count and the operator. Anyone reading the trail can tell this authorised cutover apart from tampering.
- **A signed checkpoint** over the new head, made with `SEDOC_AUDIT_SIGNING_KEY`. If the key is not set, no checkpoint is written and a warning is logged. Older checkpoints sign heads that no longer exist; only the latest is checked.

## Rollback

There is none: this is a one-way cutover. The old hashes are recorded only as the old head in the marker event. Before applying, take a database backup if you need to keep the pre-cutover hashes as evidence.
