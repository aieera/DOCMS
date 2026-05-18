# ADR 0092 — Helm chart for on-prem distribution (§13.1)

**Status:** Accepted. Chart at `deploy/helm/vaultdms/` is the supported
primary on-prem distribution mechanism.

**Date:** 2026-05-18

## Context

Blueprint §13.1 calls for a production-ready Helm chart as the primary
on-prem distribution. The playbook entry described the existing chart
as a "skeleton" — that was misleading. As of this ADR the chart
already carries:

- 14 service subdirectories under `templates/` (after this commit
  series; was 13 before — mcp-server added in commit 1)
- A shared `_service.tpl` macro library with `goServiceDeployment`,
  `goServiceSvc`, `goServiceHPA`, `goServicePDB`,
  `goServiceNetworkPolicy`, `goServiceMonitor` helpers
- Per-service HPA + PDB + NetworkPolicy + ServiceMonitor templates
- `Chart.yaml` dependencies on Bitnami postgresql/redis, plus
  opensearch / nats / qdrant / temporal / cloudnative-pg
  (last added in this series)
- A 496-line `values.yaml` covering all 14 services + 6 subchart
  configs + ingress + global

The playbook's stated ADR number 0083 was stale (already taken by
permission-filtered-search). This ADR is **0092**.

## Decisions

### Subchart shape

Each service template is a 3-line wrapper that calls into the shared
macro library. Consequence: adding a 14th service is 6 tiny YAML
files + a values block, not a full new chart. mcp-server (commit 1
of this series) followed exactly this pattern.

Why not per-service named subcharts (`charts/auth/Chart.yaml`, etc.)?
- The services are deployment-only — no per-service custom k8s
  resources beyond Deployment / Service / HPA / PDB / NP / SM that
  the macros already handle.
- Named subcharts add Chart.lock churn and slow `helm dependency
  update`.
- The current single-chart layout still allows `--set auth.enabled=false`
  to disable a service per-deploy.

### PodSecurityStandards: restricted

`goServiceDeployment` sets a restricted-profile-compatible
`securityContext` on every Go service container:

- `runAsNonRoot: true`, `runAsUser/Group: 1000`
- `readOnlyRootFilesystem: true`
- `allowPrivilegeEscalation: false`
- `capabilities.drop: [ALL]`
- `seccompProfile.type: RuntimeDefault`

Non-Go services (`collaboration`, `intelligence`, `preview`) match
all of the above EXCEPT `readOnlyRootFilesystem` — Python and the
Node CRDT server need `/tmp` + cache dirs writable. The K8s admission
controller will `warn`/`audit` those workloads under restricted but
not block them. Adding `emptyDir` mounts for the writable paths and
flipping the flag on is a follow-up.

The `web` (nginx-static) container keeps `readOnlyRootFilesystem:
true` since it serves immutable bundles.

Namespace enforcement (`pod-security.kubernetes.io/enforce: restricted`
label) is conditional on `global.podSecurity.createNamespace=true`.
When false (the default, expected in platform-team-provisioned
namespaces) operators must apply the labels externally:

```bash
kubectl label namespace vaultdms \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/warn=restricted
```

### Secret management: External Secrets Operator opt-in

`templates/externalsecrets.yaml` renders one `ExternalSecret` per
logical group (platform, eSign, Twilio, Stripe) when
`externalSecrets.enabled=true`. The application Deployments reference
the synced K8s Secret names via `envFrom`/`valueFrom` regardless of
who created them, so enabling ESO is a pipeline change, not an app
change.

Tenant-scoped credentials (per-tenant DocuSign / Twilio / SMTP) flow
through the DB (see `esign_provider_configs`, `tenant_twilio_configs`,
`tenant_smtp_configs` from ADRs 0071 + previous notification work)
and deliberately do NOT route through ESO — they are tenant-managed,
not platform-team-managed.

### Postgres: Bitnami today, CloudNativePG opt-in

The chart still ships the Bitnami `postgresql` StatefulSet as the
default Postgres backend. CloudNativePG is added as a conditional
dependency + an opt-in Cluster CR template
(`templates/postgres-cluster.yaml`), gated on
`postgresql.useOperator=true`.

Why opt-in instead of replace:
- Migration from Bitnami → operator-managed Cluster cannot be a flag
  flip. The on-disk PGDATA layouts differ and the operator wants to
  do its own `initdb`. Data must be `pg_dump`'d from the Bitnami
  pod and restored into the new Cluster before disabling Bitnami.
- The runbook documents the procedure end-to-end.

The operator path brings: streaming replication with quorum-based
auto-failover, WAL archiving to S3, point-in-time recovery,
post-init SQL that ALTERs the app role NOBYPASSRLS (the multi-
tenant invariant in CLAUDE.md). The Bitnami chart provides none of
these out of the box.

### NetworkPolicies + ServiceMonitors + HPA

Already shipped per-service before this ADR. The
`goServiceNetworkPolicy` macro defaults to ingress-from-namespace +
egress to DNS + database + Redis + NATS + opensearch + temporal.
Per-service overrides via `.svc.networkPolicy`.

`goServiceMonitor` renders a Prometheus Operator `ServiceMonitor`
scraping the `health` port at `/metrics`. Per-service tuning via
`.svc.serviceMonitor`.

`goServiceHPA` renders an autoscaling/v2 HPA when `.svc.hpa.enabled`
is true. Defaults: CPU target 70%, min=2, max=8.

## What this ADR DOES NOT do

- **OpenSearch operator migration** (Bitnami opensearch chart →
  opensearch operator). Same migration cost as Postgres; deferred
  until there's a stated operational driver.
- **NATS operator** (NATS Helm chart → nack operator). Current
  chart works; operator would add stream-CR management. Deferred.
- **MinIO operator** subchart. The chart references S3 via
  `global.s3.endpoint`; MinIO is dev-only via compose, not Helm.
  Adding the MinIO operator as an option is a follow-up.
- **ClamAV subchart**. Dev compose runs it as a standalone container;
  prod typically routes uploads through a managed scanning service.
  Adding it as a Helm-managed Deployment is a follow-up.
- **Kong ingress controller** chart dep. The existing `gateway/`
  templates run Kong in DB-less mode as a regular Deployment;
  swapping to the `kong-ingress` controller is a separate change
  that affects routing semantics.
- **kind / minikube install smoke** + kill-a-pod resilience test.
  Operational; the chart is structured for it but executing the
  test requires a working k8s cluster.

## File map (this ADR series)

| Path | Purpose |
|---|---|
| `deploy/helm/vaultdms/templates/mcp-server/*.yaml` (6) | mcp-server subchart templates |
| `deploy/helm/vaultdms/templates/_service.tpl` | PSS-restricted securityContext on every Go pod |
| `deploy/helm/vaultdms/templates/namespace.yaml` | Optional namespace with PSS labels |
| `deploy/helm/vaultdms/templates/externalsecrets.yaml` | ExternalSecret resources gated on opt-in |
| `deploy/helm/vaultdms/templates/postgres-cluster.yaml` | CloudNativePG Cluster CR gated on opt-in |
| `deploy/helm/vaultdms/Chart.yaml` | Adds cloudnative-pg conditional dep |
| `deploy/helm/vaultdms/values.yaml` | mcpServer block, podSecurity, externalSecrets, postgresql.useOperator + operator subblock |
| `deploy/helm/vaultdms/templates/{intelligence,collaboration,preview,web}/deployment.yaml` | Tightened securityContext on non-Go services |
| `docs/runbooks/helm-install.md` | Install / upgrade / rollback / migrate procedures |

## Acceptance

```bash
# Lint passes
helm lint deploy/helm/vaultdms

# Templates render to valid k8s YAML
helm template vaultdms deploy/helm/vaultdms > /tmp/rendered.yaml
kubectl apply --dry-run=client -f /tmp/rendered.yaml

# Per-service rendering works
helm template vaultdms deploy/helm/vaultdms --show-only templates/mcp-server/deployment.yaml

# Operator path renders only when opted in
helm template vaultdms deploy/helm/vaultdms --set postgresql.useOperator=true \
  --show-only templates/postgres-cluster.yaml | head -5
# (returns Cluster CR; without --set returns nothing)

# ExternalSecrets renders only when enabled
helm template vaultdms deploy/helm/vaultdms --set externalSecrets.enabled=true \
  --show-only templates/externalsecrets.yaml | head -5
```
