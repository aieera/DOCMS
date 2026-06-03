# ADR 0110 — UAE data-residency deployment

Status: Accepted (Phase 1 — code + Helm overlay shipped; live
cluster validation deferred to operator).
Date: 2026-05-19
Depends on: ADR 0007 (region pinning at the document model layer),
ADR 0092 (Helm chart).

## Context

ADR 0007 added a `primary_region` column to `organizations` and
made every blob carry a `region_pin` so the storage service can
refuse cross-region reads at the data layer. That's enough to keep
a US tenant's blobs out of an EU bucket WHEN both regions run the
same multi-tenant binary. It is NOT enough to clear a CBUAE
residency review, where the contractual ask is "no byte of this
tenant's data leaves the UAE."

UAE-residency deals require a deployment topology, not just a
column. That topology has three pieces:

1. **A dedicated cluster** in AWS me-central-1 (Dubai), with its
   own RDS / OpenSearch / KMS / NATS / Redis, no cross-region
   replicas, S3 buckets created in-region with public-access
   blocked and replication disabled.
2. **Hard region enforcement at the request boundary** so a
   misrouted load-balancer or kubectl-context mistake can't land
   a non-UAE tenant on the UAE cluster (or vice-versa).
3. **An egress NetworkPolicy** denying traffic to any non-UAE AWS
   endpoint.

## What ships now

- **`pkg/middleware.EnforceRegion`** + `PgxRegionResolver`. The
  middleware reads the cluster's `SEDOC_REGION_ID` at boot,
  resolves each request's tenant→primary_region, and returns
  **451 Unavailable For Legal Reasons** when they disagree. The
  resolver caches in-process per tenant — `primary_region` is
  immutable post-first-upload (ADR 0007) so a cache miss only
  happens on first access. 7 unit tests pass.
- **`/healthz` exposes `service` + `region`** via the canonical
  `pkg/health.NewServerWithMeta` constructor. The 12 Go services
  were migrated automatically via `scripts/migrate-health-region.mjs`;
  every binary now reports its cluster region to load balancers,
  the frontend residency banner, and operators running curl.
- **Billing provisioner residency guard** —
  `provisioner.NewWithRegion(cfg.Region, …)` refuses to create a
  tenant whose requested `Region` doesn't match the cluster. A
  Stripe webhook misrouted to the wrong cluster fails fast with a
  clear "residency mismatch" error rather than silently writing
  the org row to the wrong Postgres.
- **`deploy/regions/uae-central.yaml`** — Helm values overlay that
  pins S3 / Aurora / OpenSearch / KMS / NATS / Redis / Temporal to
  me-central-1, sets `global.region: uae-central`, and ships an
  egress NetworkPolicy with `denyDefault: true` and FQDN allow-list
  scoped to `*.me-central-1.amazonaws.com` + in-cluster DNS.
- **Frontend residency banner** at `/admin/residency`. Fetches
  `/healthz`, compares cluster region against the regions present
  in the tenant's document distribution, and shows green/red/amber
  per state.
- **Operator runbook** at `docs/deploy/uae-residency.md` covers AWS
  account setup, KMS key creation, Helm apply, sovereign-cloud
  alternatives (Core42 / G42), CBUAE encryption posture, Arabic
  production-support SLA.
- **Cross-region block integration test** at
  `services/connector/internal/region_test.go` exercises the 451
  path end-to-end through the chi router.

## What is NOT done (operator responsibility)

- **AWS account in me-central-1.** This module assumes the account
  already exists with quotas for VPC + EKS + RDS + OpenSearch +
  KMS. The chart references it; provisioning it is out of scope.
- **KMS key creation with no cross-region grants.** The overlay
  references key aliases; their creation lives in the operator's
  Terraform.
- **Live cluster validation.** The §16 load-test ADR (0105)
  documented this distinction: we ship a reproducible deployment,
  we do not stand up the cluster as part of the ADR's acceptance
  criteria. Phase 2 — once we have a sandbox lease — runs the §16
  protocol against the UAE topology.
- **Soft / hot-warm tiering across regions.** Today the chart
  configures three buckets (hot, warm, previews) in me-central-1.
  Phase 2 will introduce a Dubai-Bahrain hot-warm split for
  customers who want the warm tier in a cheaper neighbour; for
  now we keep the contractual posture "no data leaves the UAE"
  strict.

## Why 451, not 403 or 404?

The HTTP spec is explicit: 451 Unavailable For Legal Reasons is
the correct status when "the server is unwilling/unable to provide
the resource because of a legal demand to deny access to it."
Residency policy is exactly that. 403 implies the caller could
re-auth and succeed; 404 implies the resource doesn't exist; 451
correctly conveys "this server CAN'T serve this request as a
matter of policy, but it exists somewhere reachable."

The `X-DMS-Region-Block: tenant-region=…;cluster-region=…` header
on the 451 lets a multi-region client (the SaaS gateway, the
SDKs) redirect without us leaking the full cluster topology.

## Why `uae-central` and not `me-central-1`?

`primary_region` is a logical label, not an AWS region. The two
diverge intentionally:

- **Logical**: `uae-central` — what the column stores. Stable
  across a future Bahrain failover (`me-south-1`) that we'd treat
  as the same residency boundary.
- **Physical**: `me-central-1` — what S3 / KMS / Aurora know
  about. Lives in the Helm overlay, never in a tenant row.

Coupling the two would break the day we add a UAE secondary
region and have to rewrite every `primary_region='me-central-1'`
row instead of just adding `me-south-1` to the residency
boundary's allowed-physical-regions list.

## Failure modes the middleware catches

1. **Misrouted DNS** — operator points `vaultdms.example.com` at
   the wrong cluster. Every tenant request 451s with a clear
   header; the operator sees it in the access log within seconds.
2. **Stripe webhook fires at the wrong cluster** — provisioner
   refuses with "residency mismatch" before writing to Postgres.
3. **Kubectl context mistake** — operator runs a one-off `kubectl
   exec` against the wrong cluster's pod, prompts the service to
   serve a foreign tenant. Tenant→region cache miss → 451.
4. **Cross-region NATS jet pull** — an outbox publisher pointed at
   the wrong NATS lands in a foreign tenant context. The
   middleware doesn't fire on internal NATS handlers (no HTTP),
   but `RegionEnforcer.CheckBucket` + KMS key separation catches
   the attempt at the data layer.

The two failure modes the middleware does NOT catch are
deliberately out of scope:

- A buggy handler that holds a foreign-region S3 client open. The
  data-layer `RegionEnforcer.CheckBucket` exists for this; the
  middleware is the request-layer complement.
- A platform-admin invoking ADR 0069 federated search across
  regions. That code path has its own auditing; residency
  enforcement does not fire on it.

## Open questions deferred

- **CBUAE TRA "sovereign cloud" tier**. Some UAE buyers will
  contractually require G42 Cloud / Core42 instead of AWS
  me-central-1. The Helm overlay's only AWS-specific bits are the
  S3 + KMS provider; swapping in Core42's S3-compatible endpoint
  + their KMS HSM is documented in the runbook but unverified.
- **Per-tenant `data_residency_region` mutability**. Today
  `primary_region` is immutable after first upload. CBUAE-tier
  customers may need a documented "migration window" where the
  column can be updated under a cross-region migration workflow.
  ADR 0007 already has the migration tool; this ADR doesn't
  extend it.
- **Cross-tenant compare under residency lock**. ADR 0101's
  compare endpoint can in principle compare a US doc with a UAE
  doc — should it refuse? Defer until a customer asks.
