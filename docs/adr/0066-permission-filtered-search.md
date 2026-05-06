# ADR 0066 — Permission-filtered search + propagation SLI

Date: 2026-05-06
Status: Accepted
Supersedes: nothing (was scheduled as ADR 0057 in the §7.3 blueprint;
0057 is taken by OCR Quality Scoring. Same numbering shift as
0061-0065.)

## Context

The search service has had a `readable_by` keyword field on every
indexed document since v1, populated as a flat mix of:

- the document's `created_by` user_id
- every group_id with a permission grant
- `"everyone"` for tenant-wide visibility

The query injects `terms readable_by [user_id, ...group_ids,
"everyone"]` into the bool.filter clause. Tenant isolation comes
from a separate `tenant_id` term filter. Permission changes flow
through `dms.permission.changed.v1`, which the search service
subscribes to and propagates by partial-updating the affected
docs' `readable_by` field.

§7.3 asks for four things on top of what's there:

1. **Split user vs group access** so the query (and audit) can tell
   whether a hit matched because of a direct user grant or via a
   group membership. The mixed field works for correctness but loses
   the explanation.
2. **External share-token matching** as a separate path — revoking
   a share link should not require re-publishing the full
   readable-by set.
3. **Debounced 5s batch** on permission propagation so a bulk grant
   rollout (e.g. adding a group to a workspace with 100k docs)
   doesn't fan out to 100k OpenSearch round-trips.
4. **SLI metric** — `permission_propagation_lag_p95 < 5s`. The
   on-call rotation needs to know within seconds when permission
   changes are stuck.

## Decision

### Schema

Three new keyword fields on the index template:

| Field | Source | Used for |
|-------|--------|----------|
| `readable_by_users`  | direct user_ids + group memberships expanded | the `terms readable_by_users [user_id]` query clause |
| `readable_by_groups` | group_ids granted access to the doc | the `terms readable_by_groups [user_groups]` clause — matches a user via the groups they belong to without having to expand into users at index time |
| `share_tokens`       | opaque tokens for external share links | unauthenticated link-followers; the gateway maps the URL token to a single-element `[token]` filter at query time |

The legacy `readable_by` field is kept populated during the
migration window. Docs indexed before this ADR landed don't have
the new fields; the query layer ORs over `readable_by` as a bridge
clause so they keep matching. Cleanup follow-up: a one-shot
backfill that splits the mix and drops the old field once every
deploy is on the new query shape.

### Query

```
bool.filter:
  - term:  tenant_id = $tenant
  - bool.should:
      - terms: readable_by_users  = [$user_id]
      - terms: readable_by_groups = $user_groups
      - terms: share_tokens       = [$share_token]   (when present)
      - terms: readable_by        = [$user_id, ...$user_groups, "everyone"]   (legacy bridge)
    minimum_should_match: 1
```

`bool.should` with `minimum_should_match: 1` means "the doc must
match at least one of these access paths". `bool.filter` runs in a
non-scoring context — same as today, no scoring impact.

### Permission-change propagation

Existing handler updates `readable_by` synchronously on every
`dms.permission.changed.v1`. ADR 0066 adds a debouncer keyed on
`(tenant_id, resource_type, resource_id)`:

- Each event tickles the entry's deadline 5s into the future.
- A flusher loop wakes every 1s, walks entries whose deadline has
  passed, and issues one OpenSearch update per resource.
- A bulk grant (group added to a workspace) collapses N updates
  for the same workspace_id into one — drops the 100k-op fan-out
  problem.

Trade-off: a single grant change takes up to 5s to land vs.
sub-second today. Acceptable for the use case; the SLI metric
makes the latency visible.

### SLI

Prometheus histogram `search_permission_propagation_lag_seconds`
with buckets at `[0.1, 0.5, 1, 2, 5, 10, 30]`. Observed at the
moment the OpenSearch update commits, with `(now - event.time)`.
The runbook will eventually wire an alert on `histogram_quantile(0.95, …) > 5`.

A separate counter `search_permission_propagation_total{result}`
tracks success / failure / dropped (term'd malformed message).

## Consequences

- **Audit clarity.** A search hit's matched-clause identifies whether
  the user got there via a direct grant, group membership, or share
  token. The query response doesn't expose the matched clause yet,
  but the index is now structurally able to.
- **Migration window.** Until every doc is reindexed, the `readable_by`
  bridge clause is load-bearing. An aggressive cleanup that removes
  it before backfill would silently strip access from old docs.
  Tracked as a follow-up.
- **5s tail latency on permission changes.** Existing was sub-second.
  The SLI metric makes the regression visible; the bulk-grant
  scenario is what we're optimizing for.
- **Two more keyword fields per doc.** Storage cost is negligible —
  keyword index overhead ≈ field cardinality × doc count, and the
  expansion ratio is small for typical access lists.
