# ADR 0069 — Platform-admin federated search

Date: 2026-05-06
Status: Accepted
Supersedes: nothing (was scheduled as ADR 0060 in §7.7; 0060 is taken
by Active Learning Pipeline. Same shift as 0061-0068.)

## Context

The customer-facing search service is structurally tenant-scoped:
every query passes through middleware that issues
`SET LOCAL app.current_tenant`, the DB role `dms_app` is
`NOBYPASSRLS`, and OpenSearch queries always include a
`term tenant_id` filter. Cross-tenant data has never been reachable
from any user-facing path.

Support team needs occasional cross-tenant visibility:
- "Customer reports they can't find a document — does it exist
  somewhere they shouldn't expect?"
- "Our threat-intel team flagged a doc hash — is it indexed in any
  tenant?"
- "An incident review needs to confirm a phishing payload was NOT
  ingested anywhere."

§7.7 calls for a **platform-admin** tier — a small allow-listed
group above the per-tenant role hierarchy — with a single
deliberately-narrow endpoint that bypasses the tenant filter, plus
heavy auditing. Customer-facing APIs MUST stay isolated; this is
explicitly an internal-support surface.

## Decision

### Permission model

A new permission `platform.search.federated` granted via membership
in the `platform_admins` table. The table:

- Has no `tenant_id` column — it's a platform-level resource.
- Lives in the search service's DB (the only consumer today).
- Is NOT under RLS — the federated handler reads it with no tenant
  context. Putting the platform-admin allow-list under tenant RLS
  would be circular.
- Carries a required `reason` for the grant + `granted_by` audit
  trail; revocation is soft via `revoked_at`.

Allowing the *check* itself to bypass RLS is intentional. RLS
forces correct tenant scoping on the data plane; the platform-
admin allow-list is the ONE explicit exception, and the audit
trail makes every use traceable.

### Endpoint

```
POST /api/v1/platform/search/federated
Body: {
  query:           string,           // free-text, full search query
  filters:         {…},              // any standard filters
  reason:          string,           // REQUIRED, ≥ 10 chars
  max_per_tenant:  int,              // optional cap, default 10
  page_size:       int,              // optional, default 50
}

Response: {
  results_by_tenant: {
    "<tenant_id>": [hits…],
    …
  },
  total_hits: int,
  tenants_with_hits: int,
  audit_id: uuid,                    // links to federated_search_audit row
  latency_ms: int,
}
```

The handler:
1. Reads X-User-ID + X-Auth-Tenant-ID headers (any tenant — the
   platform admin may be logged into one of their own tenants).
2. Validates `reason` is non-empty and at least 10 chars (a single
   word like "test" doesn't satisfy compliance review).
3. Looks up the user in `platform_admins` (active = revoked_at IS
   NULL). Non-member → 403 + audit row with `outcome='denied_perm'`.
4. Checks the rate limit (Redis: `federated_search:{user_id}:{day}`,
   100/day). Over → 429 + audit row with `outcome='denied_quota'`.
5. Builds an OpenSearch query with the SAME shape as the regular
   /search but **omits the `tenant_id` term filter** (and the ADR
   0066 readable_by chain — federated context bypasses both).
6. Searches across the cross-tenant index pattern (`dms-documents-*`).
7. Groups hits by `tenant_id` from the source.
8. Writes the audit row (`outcome='success'`) with the full query
   body + per-tenant hit summary.

### Rate limiting

100 queries per admin per UTC day. Cheap to implement (a single
Redis counter with day-rounded key + 86400s expiry). Hard cap —
when an admin hits the limit they wait until 00:00 UTC. The
business reason is shaped to support, not bulk export.

### Audit invariants

The audit row is written **always**:
- on success (with full results_summary)
- on permission denial (with outcome='denied_perm' — denied
  attempts are visible)
- on quota denial (with outcome='denied_quota')
- on error (with outcome='error', error_kind populated)

This is non-negotiable: the auditing surface for cross-tenant
access is the load-bearing compliance signal. A handler bug that
lets a query run without auditing is treated as a P0 incident.

### What's intentionally NOT supported

- **Bulk export.** The endpoint is for diagnosis, not export.
  `max_per_tenant` defaults to 10, max 100. There's no
  `from`/`page_token` pagination — a query that needs to walk
  millions of hits is the wrong tool.
- **Cross-tenant aggregations.** Buckets that span tenants (e.g.
  "top tags across all customers") would be analytics, not support.
  Same audit-and-narrow-scope reasoning applies.
- **Direct DB access via this endpoint.** The federated path goes
  through OpenSearch only. Postgres queries via this endpoint stay
  forbidden — the existing per-service DB code paths can't bypass
  RLS even for a platform admin (the role is `NOBYPASSRLS`).

### Why not gate via the existing role string?

Adding a `platform_admin` value to the existing `users.role` enum
would conflate per-tenant roles (owner|admin|member) with a
platform-level capability. The user might be `member` in their
home tenant AND a platform admin — those are different shapes.
Separate `platform_admins` table keeps the concepts orthogonal.

## Consequences

- **Cross-tenant visibility** is now possible for an allow-listed
  group with full audit. Every use is traceable to a caller, a
  reason, and a query payload reviewable by compliance.
- **Customer-facing APIs stay structurally isolated.** The new
  endpoint is on `/platform/search/federated` — every existing
  `/search`, `/search/suggest`, `/saved-searches` path keeps the
  tenant filter and never reaches this code.
- **A misconfigured allow-list is a serious leak vector.** Adding
  `platform_admins` rows is itself audited (granted_by + reason);
  there's no UI for granting, only the manual SQL path. The
  separation of "data is exposed" (search service) from "trust
  decisions" (DBA-managed grant) is intentional.
- **No automatic test for "admin queries don't bleed into customer
  APIs".** The integration test asserts a regular user gets 403 on
  the federated endpoint, and the existing /search tests assert
  the tenant_id filter is always present. Together these are
  sufficient — there's no code path connecting the two.
