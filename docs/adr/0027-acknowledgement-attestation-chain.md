# ADR 0027 — Acknowledgement attestation chain

* Status: Accepted
* Date: 2026-04-21
* Wave: 15.1

## Context

Wave 15.1 acknowledgement campaigns must produce **tamper-evident attestations** that a specific user acknowledged a specific campaign at a specific moment — strong enough for SOC2 / HIPAA / ISO-27001 audit evidence. Requirements:

1. An auditor can verify an exported attestation offline (no call back to the platform).
2. A single compromised row must not enable forging a plausible chain.
3. Plaintext signing material never lives at rest.
4. Verification is deterministic and reproducible.

## Decision

Two-layer design:

### Layer 1 — per-assignment HMAC

When a user acknowledges, the service computes:

```
attestation_hash = HMAC-SHA256(
    key     = tenant_signing_key,                # 32 bytes, never on disk in plaintext
    message = campaign_id || "|" || assignee_id || "|" || RFC3339Nano(acknowledged_at)
)
```

- `tenant_signing_key` is generated on first ack per tenant, AES-KW-wrapped under the tenant's KEK, and persisted in `acknowledgement_signing_keys(tenant_id, kek_id, wrapped_key)` with RLS. Plaintext is cached in-memory per process; cache is cleared on KEK rotation.
- `kek_id` is `vaultdms/tenant/<uuid>/acknowledgement` — namespace-separated from document / MFA / storage keys per ADR 0022.
- The stored 32-byte `attestation_hash` goes on the assignment row. Recomputing it from `(campaign_id, assignee_id, acknowledged_at)` reproduces the exact bytes if-and-only-if the caller has the tenant signing key.

### Layer 2 — per-campaign append-only hash chain

Every state transition (`campaign.created`, `acknowledged`, `campaign.closed`, and the `reminded` / `escalated` events when those land) writes a row to `acknowledgement_events` with:

```
prev_hash  = previous row's self_hash (NULL for the first row in a campaign)
self_hash  = SHA-256(prev_hash || canonical_json(payload))
```

- `canonical_json` serializes map keys in sorted order so the hash is byte-stable across replicas and replays (Go's `encoding/json` already does this for `map[string]any`; we document the dependency).
- Verification: walk the rows for a campaign in `created_at` order, recompute `self_hash` from `prev_hash + payload`, require `computed == stored`. Mismatch at row N means tampering between row N-1 and row N.

The two layers are independent — forging a chain requires both rewriting the `acknowledgement_events` rows (detectable) and producing a matching `attestation_hash` (requires the tenant signing key).

### Export format

A signed export bundle contains:
1. `attestations.ndjson` — one line per assignment with `{campaign_id, user_id, acknowledged_at, attestation_hash}`.
2. `events.ndjson` — one line per `acknowledgement_events` row.
3. `cover.json` — `{tenant_id, campaign_id, exported_at, chain_head_self_hash}`.

`dms-admin audit verify-attestation --bundle <path>` replays the chain and recomputes every HMAC, emitting green / red per row. Signing key rotation is out of scope for Wave 15.1 — see Follow-ups.

## Consequences

**Positive**
- Offline verification: auditor only needs the bundle + the tenant's KEK (or the signing key itself, extracted under a controlled break-glass procedure) to verify every row.
- Tamper-evidence per row + across the whole chain. A silent edit to `acknowledged_at` breaks the HMAC; a silent row deletion breaks the chain.
- Separates the "did X really happen?" question (HMAC) from the "were these events in this order?" question (chain). Either alone is weaker; the combination matches auditor mental models.

**Negative**
- Plaintext signing key lives in process memory. Compromising the service host lets an attacker forge future ACKs but NOT retroactively alter stored ones (row hashes don't change). Mitigated by the KEK rotation path; plaintext is re-derived from the wrapped form.
- `prev_hash` is a row attribute, so replay detection during export relies on `created_at` ordering; we cannot reorder rows without breaking the chain, which is the property we want.
- Canonical-JSON relies on Go's map-key sort behaviour. A refactor that switches to a struct serialiser could silently break hash stability; test `TestCanonicalJSON_StableKeyOrder` guards this.

**Neutral**
- The hash chain is per-campaign, not per-tenant. Cross-campaign correlation attacks aren't in the threat model (auditors look at one campaign at a time).

## Alternatives considered

1. **Per-assignment signature with Ed25519** (instead of HMAC). Rejected: requires per-tenant key-pair management; the public key would need distribution for offline verification, and the key-rotation story is more complex than HMAC with wrapped symmetric keys.
2. **Global hash chain** across all campaigns. Rejected: cross-tenant isolation would force the whole chain to live behind an RLS boundary, which breaks the offline-verify property for anything other than the tenant's own rows.
3. **Merkle tree per campaign**. Considered. Offers log-size proofs, but the overhead of producing a consistent inclusion proof alongside every row adds code paths auditors don't ask for. Revisit if campaign sizes exceed ~100k assignments.
4. **Storing only HMAC, no chain**. Rejected: silent row deletion is invisible.
5. **Storing only chain, no HMAC**. Rejected: HMAC binds a row to the tenant's key; an attacker with DB access could fabricate a plausible-looking row without the key but chain verification alone would accept it as long as hashes line up.

## Follow-ups

- Temporal schedule for reminder + escalation (writes chain rows + outbox events).
- Signing-key rotation — adds a `rotated_at` row and keeps the old key available during a dual-window. `acknowledgement_signing_keys.rotated_at` is already in the schema.
- `dms-admin audit verify-attestation` subcommand that reads a bundle, replays the chain, and reports per-row verdicts.
- Signed ZIP export with cosign attestation for the cover sheet.
- `go-mutesting` score ≥ 70 % on the HMAC + canonical-JSON paths (CC-6 of the Wave 15 brief).
