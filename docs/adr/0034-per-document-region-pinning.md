# ADR 0034 — Per-document region pinning trust boundaries

**Status:** Accepted · **Date:** 2026-04-24 · **Supersedes:** none · **Builds on:** ADR 0022 (envelope encryption), ADR 0026 (per-region KEK masters).

## Context

`documents.region_pin` and `workspaces.region_pin` landed in Wave 8.4. ADR 0026 layered per-region KEK masters on top. The residency migration workflow moves documents between regions. What was missing was a single document tying the pieces together — where residency is enforced, which surfaces are authoritative for which decision, and how a cross-region attempt propagates an error from the service layer to the admin audit log.

This ADR is that document. It does not change any on-disk invariant.

## Decision

Residency is enforced at four distinct surfaces. The invariant is: **no authenticated write touches data outside the document's pinned region's geopolitical boundary.** We accept that same-boundary cross-region moves (e.g. `eu-west-1` → `eu-central-1`) are legitimate and handled only through the residency-migration workflow.

### The four surfaces

1. **Service layer (authoritative policy decision)** — `services/document/internal/service/region_resolver.go::resolveRegionForCreate` reads `organizations.default_region_pin` + `workspaces.region_pin` + `organizations.allowed_regions` and chooses the final region. No code path that writes a document row bypasses this resolver.

2. **Storage layer (physical residency)** — `bucketName(region, tier)` → `dms-{region}-{tier}`. The bucket is the region. A caller asking to `PutObject` against a bucket whose region string doesn't match the document's `region_pin` is rejected by `pkg/middleware.RegionEnforcer.CheckBucket`. The storage service's KEK alias (`vaultdms/tenant/<uuid>/<region>/documents` per ADR 0026) adds cryptographic residency — ciphertext written under an `eu-west-1` KEK is structurally undecryptable from a `us-east-1` KMS principal.

3. **Index layer (query residency)** — OpenSearch index naming `vaultdms-{region}-*` is enforced by `RegionEnforcer.CheckIndex`. *Query fan-out* across regions a user is entitled to is a Wave 12+ item and explicitly out of scope here; today, every document is indexed in exactly one per-region-labeled shared index, and the search service scans by filter.

4. **Audit + event layer (trail of every decision)** — `dms.residency.migrated.v1` for accepted moves, `dms.residency.violation.v1` (new in this wave) for rejected attempts. Both land in the audit chain via the outbox, so the /admin/audit-log page shows attempts and successes side by side.

### Geopolitical boundaries are the primary policy axis

`pkg/regionenforcer/boundaries.go` mirrors `supported_regions` with a Boundary field (EU / US / MENA / APAC / OTHER). Cross-*boundary* movement requires a policy exception — there is no admin-UI path that permits it; the residency-migration workflow refuses to enumerate targets outside the source boundary. Cross-*region within boundary* is the normal residency-migration workflow.

### What this ADR does not decide

- **Physical blob re-wrap on residency migration** — Wave 12. The workflow today flips `region_pin` and emits the migrated event; the storage service does the bytes copy + rekey separately.
- **Per-region Redis / Qdrant sharding** — Wave 12+. Today both are single-cluster with tenant-id filters.
- **Log shipper per-region routing** — Out of scope; the OpenSearch log cluster is global.

Each of these deferrals is tracked in `docs/backlog/out-of-scope.md`.

## Consequences

**Positive**
- New tenants default to `me-south-1` via `organizations.default_region_pin` without breaking existing tenants' `us-east-1` contract (Wave 14 migration 000014 backfills explicitly).
- Admin UI can offer a clean `allowed_regions` checklist at `/admin/tenant/residency` — the policy is one SQL column, not scattered across config files.
- Every residency violation produces a first-class audit row, not just a 4xx in the request log.

**Negative**
- The Go mirror in `boundaries.go` must track the SQL seed. `TestBoundariesMatchMigration` guards this at CI time; drift still requires a two-file PR.
- Cross-boundary movement cannot happen even with admin override short of a new migration and a new code path. This is the intended behavior; customers contractually requiring cross-boundary moves would be a separate customer-tier decision.

## References

- Blueprint §9.1 — per-document region pinning
- ADR 0022 — envelope encryption (non-regional)
- ADR 0026 — per-region KEK masters
- `docs/runbooks/region-violation-response.md`
- `docs/backlog/out-of-scope.md` (Wave 12 deferrals)
