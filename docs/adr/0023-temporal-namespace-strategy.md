# 0023 — Temporal namespace strategy: single namespace, tenant as search attribute

- **Status:** Accepted
- **Date:** 2026-04-17
- **Deciders:** Wave 7 Prompt 7.1 execution
- **Supersedes:** —

## Context

`DMS Architecture/final.md` § 6.2 posed the choice: one Temporal
namespace per tenant, or a single shared namespace with a `tenant_id`
search attribute.

Context:

- **Scale.** The product target is 1M+ organizations. Temporal
  Cloud bills per-namespace and its Server limits (default 10k
  namespaces per cluster) make per-tenant namespaces untenable above
  mid-four-figures of tenants.
- **Isolation.** Self-hosted on-prem customers run one cluster per
  customer deployment — there's only ever one tenant, so the choice
  collapses. The question is SaaS-only.
- **Operability.** Each namespace has its own retention, archival,
  and search-attribute registration. Changing retention on 100k
  namespaces is a nightmare; changing it on one is a single
  `tctl admin cluster update` call.
- **Workflow isolation.** Temporal enforces isolation by namespace:
  a workflow in namespace A cannot signal or query a workflow in
  namespace B without cross-namespace RPC — something we never want
  anyway.

## Decision

Use a **single Temporal namespace** named `vaultdms` for SaaS
deployments. Every workflow carries:

- **Search attribute `tenant_id` (Keyword)** — registered cluster-wide
  at namespace creation. All workflows MUST set this at start time.
  Every query or list operation that the workflow service issues
  against Temporal must filter on `tenant_id = current_tenant`.
- **Workflow ID prefix `<tenant_id>-`** — so the namespace's workflow
  list is visually / greppably tenant-partitioned. Required format:
  `<workflow_type>-<tenant_id>-<resource_id>[-<version>]`.
- **Taskqueue `vaultdms-default`** — shared across all workflows and
  tenants. Workers pull from one queue and dispatch by workflow type.

For **on-prem single-tenant customers**, the namespace defaults to
`vaultdms` too; the `tenant_id` search attribute is still set
(always the customer's sole tenant id). No deployment-mode branch
in code.

## Consequences

**Easier**
- One namespace to monitor, backup, archive, upgrade.
- Changing retention policy is a single operation.
- No quota limit on tenants (bounded only by cluster-wide workflow
  count).
- Workflow history is co-located — cross-tenant analytics (opt-in,
  careful) is feasible.

**Harder**
- **Filter-every-query.** Every `List`, `Describe`, and `Query`
  call the workflow service issues to Temporal MUST narrow by
  `tenant_id`. Forgetting the filter = cross-tenant data exposure.
  Mitigation: a thin wrapper (`pkg/temporal/tenant_filter.go`) that
  every caller must use; code review gate; plus an integration test
  (Wave 13.1) that starts a workflow in tenant A and proves listing
  in tenant B's context returns zero hits.
- **Signal/query routing safety.** Workflow IDs contain the tenant;
  callers must validate the tenant in the ID matches the caller's
  tenant before sending a signal. Wrapper enforces this too.
- **Scheduled workflows (retention cron, etc.)** fan out per-tenant.
  Use `ScheduleClient` per tenant; each schedule's workflow type is
  the same, only the search-attribute + workflow-id differ.

**Neutral**
- Temporal's RBAC model has per-namespace roles only, not
  per-search-attribute. We enforce tenant boundary in our code,
  not in Temporal's ACL — same as we do with Postgres RLS.

## Alternatives considered

1. **Namespace per tenant.** Rejected above 10k tenants — Temporal
   cluster limits. Also 10x the operational surface.

2. **Namespace per region + `tenant_id` search attribute.** Two
   layers: `vaultdms-us-east`, `vaultdms-eu-central`, etc., with
   search attribute inside each. Could work as a future split once
   residency requirements force regional isolation at the cluster
   level. Deferred; current pilot is single-region.

3. **Workflow ID carries ONLY tenant, no prefix.** Rejected because
   operators need to see workflow type in `tctl` listings without
   running a describe — readability win.

## Implementation notes

- `services/workflow/cmd/worker/main.go` connects with
  `client.Options{Namespace: "vaultdms"}`.
- `SearchAttributes` are registered at boot via a one-shot admin
  call in `cmd/worker/main.go` if absent (idempotent).
- Every `StartWorkflowOptions` built by `handler.StartWorkflow` sets:
  ```go
  SearchAttributes: map[string]any{"tenant_id": tenantID.String()}
  ID:               fmt.Sprintf("%s-%s-%s", workflowType, tenantID, resourceID)
  TaskQueue:        "vaultdms-default"
  ```

## Sources

- Temporal docs — [Namespace](https://docs.temporal.io/namespaces).
- Temporal docs — [Search Attributes](https://docs.temporal.io/visibility#search-attribute).
- `DMS Architecture/final.md` § 6.2.
