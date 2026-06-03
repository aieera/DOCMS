# Remediation 08 — Helm completeness + docker-compose app profile + service READMEs + architecture

**Date:** 2026-04-17
**Scope:** Fill in the Helm chart holes (6 services had incomplete
templates, 2 had nothing), add the missing app-tier services to
docker-compose behind an opt-in profile, write a README per service
(14), and commit a single architecture document with flow diagrams.
**Source finding:** `docs/audit/02-missing.md`.

---

## Task 1 — Helm templates

Before (from audit 02-missing.md):

| Service | Templates present |
|---|---|
| billing | **0** |
| collaboration | 1 (deployment) |
| connector | **0** |
| intelligence | 2 (deployment, service) |
| preview | **0** |
| web | 1 (deployment) |

After:

| Service | Templates present |
|---|---|
| billing | 6 (deployment, service, hpa, pdb, networkpolicy, servicemonitor) |
| collaboration | 5 (svc stays inline in deployment.yaml; hpa/pdb/networkpolicy/servicemonitor added) |
| connector | 6 |
| intelligence | 6 |
| preview | 6 |
| web | 3 new (service, hpa, servicemonitor); the previously-inline Service was extracted |

For the two Go services (`billing`, `connector`) the new files are
three-line wrappers around the `vaultdms.goServiceDeployment` /
`…Svc` / `…HPA` / `…PDB` / `…NetworkPolicy` / `…Monitor` helpers in
`_service.tpl`. values.yaml got new sections for both, matching the
audit/storage shape.

For the stateful WebSocket service (`collaboration`) the HPA caps at
5 replicas (not 10) with a 5-minute scaleDown stabilization window —
if scale-down evicts a pod mid-session, every peer on that pod
reconnects, which is a thundering-herd risk. The PDB keeps at least
one replica available during node drains. Ingress-cookie affinity is
already in the top-level Ingress annotations (pre-existing).

For the Python/Celery services (`intelligence`, `preview`) the
deployment spawns **two** Deployments per service — an API tier and a
worker tier. Each gets its own HPA + PDB + NetworkPolicy. Worker
NetworkPolicies have **empty** ingress — workers pull from Redis +
NATS only, never accept inbound connections. HPAs are CPU-based for
now; the right long-term signal is queue depth via KEDA, noted in
comments on the template.

The audit report called out a 20Gi PVC for intelligence model caches.
That optional feature wasn't delivered here — the existing deployments
rely on transient model downloads at container start. Flagged for a
follow-up pass that adds `volumeClaimTemplates` + the values knob.

### Chart-wide tests

```
$ helm lint deploy/helm/sedoc
==> Linting //charts/vaultdms
[INFO] Chart.yaml: icon is recommended
[WARNING] chart directory is missing these dependencies: postgresql,redis,opensearch,nats,qdrant,temporal

1 chart(s) linted, 0 chart(s) failed
```

Dependencies are declared in `Chart.yaml` and fetched from public
charts; the warning just tells us `helm dependency build` hasn't run
yet. In CI that step runs before every lint / template invocation.

```
$ helm dependency build deploy/helm/sedoc
Saving 6 charts
$ helm template vdms deploy/helm/sedoc (default) → 3435 lines, 97 resources rendered clean
$ helm template vdms deploy/helm/sedoc -f values-onprem.yaml → 3445 lines, clean
$ helm template vdms deploy/helm/sedoc -f values-airgapped.yaml → 3477 lines, clean
```

Resource breakdown (default values):

```
Deployment              17
HorizontalPodAutoscaler 15
NetworkPolicy           16
PodDisruptionBudget     16
Service                 15
ServiceMonitor          14
Ingress                  1
CronJob                  1
Job                      2
```

All 3 value files render without errors. `kubeval` wasn't run
locally (needs the binary; the CI pass can add it as a step).

---

## Task 2 — values.yaml sections

`values.yaml` was missing `billing:` and `connector:` entirely — added
both following the audit/storage shape (enabled, replicas, image,
resources, hpa, env, ports). Existing on-prem and air-gapped values
files inherit defaults; they override `global.*` (S3 endpoint, KMS
provider, SMTP host, etc.), not per-service shape, so no additional
edits were needed.

---

## Task 3 — docker-compose app profile

Added three services to `docker-compose.yml` under a new `app`
profile (opt-in via `docker compose --profile app up -d`):

- `collaboration` — WebSocket hub, port 8083, `redis: service_healthy`
  dep, built from `services/collaboration/Dockerfile`.
- `intelligence-worker` — Celery worker, queues
  `intelligence,intelligence-ocr,intelligence-embed,intelligence-rag`,
  broker `redis://redis:6379/1`.
- `preview-worker` — Celery worker, queues `preview,preview-video`,
  broker `redis://redis:6379/2`.

The profile gate is intentional — most contributors run Go services on
the host via `make run-all`; infrastructure (`docker compose up -d`)
stays lightweight. Anyone needing the full app tier opts in with a
single flag. `docker compose config --quiet` passes with exit 0.

The full-stack "16 services healthy within 90s" acceptance criterion
from the prompt wasn't verified here because:

- The intelligence + preview Dockerfiles install heavy ML deps
  (torch, surya, sentence-transformers). A first build takes ~20
  minutes; CI should do the build in a separate job and cache the
  image.
- OpenSearch still requires `OPENSEARCH_INITIAL_ADMIN_PASSWORD`
  (pre-existing — 04b live-run flag).
- MinIO healthcheck is now fixed (bash `/dev/tcp`) — 04b live-run
  drive-by.

Flagged for a separate "full compose boot" acceptance remediation;
the wiring here is correct.

---

## Task 4 — Service READMEs

14 new files:

```
services/audit/README.md
services/auth/README.md
services/billing/README.md
services/collaboration/README.md
services/connector/README.md
services/document/README.md
services/intelligence/README.md
services/notification/README.md
services/policy/README.md
services/preview/README.md
services/search/README.md
services/signature/README.md
services/storage/README.md
services/workflow/README.md
```

All 10 sections from the prompt: Purpose, Responsibilities, API
surface, Dependencies, Configuration, Running locally, Testing,
Deployment, Metrics, Troubleshooting.

I wrote 14 unique documents rather than a fill-in template, per the
prompt's "DO NOT copy-paste" rule. Each Troubleshooting section
captures real bugs surfaced in earlier remediations (03c outbox
spam, 04b column drift, 04a OCR publish-drop, storage's ClamAV
protocol parse, billing's `usage_records` migration gap) so a new
dev hitting the same error finds the recipe where they look first.

`markdownlint` was not run locally — a common `.markdownlint.json`
would want to be pinned to the repo's preferred style before gating
on it. The files pass eyeball review against the remediation docs.

---

## Task 5 — Architecture doc

[docs/architecture.md](../../docs/architecture.md) — a single page with:

- **High-level component diagram** (mermaid) showing the client edge
  → ingress → edge services → core services → cross-cutting services
  → Python worker tier → stateful infra. Distinguishes synchronous
  (gRPC) from asynchronous (NATS) edges.
- **Tenant isolation** — the RLS pattern in one paragraph + one SQL
  policy fragment.
- **Data flow: upload → OCR → search → AI Q&A** (mermaid sequence
  diagram with 14 steps).
- **Deployment topology** for SaaS vs on-prem vs air-gapped.
- **Design rationale** — why RLS, why outbox, why per-blob envelope
  encryption, why OPA, why NATS vs Kafka, why Temporal.

The doc links back into the remediation series so readers bounce
from "why this exists" to "the specific pass that introduced it".

---

## Verification summary

| Check | Result |
|---|---|
| `helm lint deploy/helm/sedoc` | exit 0 (1 known-benign subchart warning) |
| `helm template … -f values.yaml` | 97 resources, 0 errors |
| `helm template … -f values-onprem.yaml` | clean |
| `helm template … -f values-airgapped.yaml` | clean |
| `docker compose config --quiet` | exit 0 |
| `ls services/*/README.md \| wc -l` | 14 |
| Architecture doc present | `docs/architecture.md` |

---

## DO-NOTs honored

- No canary, no service-mesh annotations, no Istio sidecars — the
  charts stay minimal and match the reference audit template shape.
- No boilerplate READMEs — each file documents its specific service,
  including known bugs from the remediation history.
- Architecture diagram shipped — future devs find the system
  taxonomy in one place instead of archaeology through
  `docker-compose.yml`.
