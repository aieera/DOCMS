# Runbook — region violation response

**Audience:** on-call + compliance officers. **Signals:**

- Alert: `storage_residency_violations_total` non-zero over 10 min.
- Admin UI: a red `Residency violation` row in `/admin/audit-log`.
- User report: "I got a 451 / REGION_VIOLATION when trying to upload / move / share."

## Event shape

Every violation writes one outbox row with subject `dms.residency.violation.v1`. The payload carries:

```json
{
  "tenant_id":        "...",
  "actor_id":         "...",
  "workspace_id":     "...",
  "requested_region": "eu-west-1",
  "resolved_region":  "us-east-1",
  "operation":        "create_document | create_version | move | share",
  "reason":           "not_allowed | cross_boundary | unknown_region | layer_mismatch"
}
```

## Triage by `reason`

| `reason`         | Meaning | Action |
|------------------|---------|--------|
| `not_allowed`    | Caller picked a region that isn't in `organizations.allowed_regions`. | Usually a UI selector drift — the tenant recently narrowed their allowlist. Confirm with the tenant owner before loosening. |
| `cross_boundary` | Target region is in a different geopolitical boundary than the document. | **Never auto-recover.** Cross-boundary moves are a contractual decision — escalate to legal + the account owner. |
| `unknown_region` | Region string is not in `supported_regions`. | Client/config bug. Check for a stale front-end build, a misconfigured tenant default, or a typoed `X-Force-Region` header. |
| `layer_mismatch` | Target layer (storage bucket / search index / cache keyspace) doesn't match the document's pinned region. | Usually a misrouted service call. Inspect gateway logs for the correlation id and trace which service picked the wrong endpoint. |

## Immediate triage

1. Pivot from the violation row to the `correlation_id` and pull the request log.
2. Confirm `organizations.default_region_pin` + `allowed_regions` for the tenant match what the customer expects.
3. Check `/admin/tenant/residency` — did an admin recently update the allowlist?

## Recovering from an accidental policy tightening

If the violation is caused by an admin removing a region from `allowed_regions` while documents still exist there:

```sql
-- Identify documents stranded outside the current allowlist.
SELECT d.id, d.region_pin, COUNT(*) AS cnt
FROM documents d
JOIN organizations o ON o.id = d.tenant_id
WHERE d.tenant_id = :tenant
  AND NOT (d.region_pin = ANY(COALESCE(o.allowed_regions, ARRAY[]::text[])))
GROUP BY d.id, d.region_pin;
```

Two options — both owner-role-gated:

- **Widen the allowlist:** re-add the region to `organizations.allowed_regions`. Non-destructive, fastest.
- **Migrate the stranded documents:** kick off a residency-migration workflow into a region that IS allowed. See `/admin/residency`. Must be same geopolitical boundary.

## Cross-boundary violation response

A `cross_boundary` violation is a policy/legal event, not a technical one.

1. Freeze the offending workspace: `UPDATE workspaces SET locked = true WHERE id = :ws`.
2. Notify the tenant owner via the notification service (`dms.notify.residency_violation.v1` — queue an admin-only alert).
3. File a compliance ticket citing the `correlation_id`. Do not migrate or release until legal signs off.

## Metrics worth watching

- `storage_residency_violations_total{reason, operation}` — per-cause counter.
- `data_residency_compliance` — SLI; docs in their own region / total docs. Target 100%.
- `residency_migrations_total{status}` — should stay at zero for `failed` after the reconciliation sweep runs.

## See also

- ADR 0034 — trust-boundary model for region pinning.
- ADR 0026 — per-region KEK masters.
- `/admin/residency` — migration form + per-region doc counts.
- `docs/runbooks/06-key-management.md` — KEK rotation, relevant when a region is decommissioned.
