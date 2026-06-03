# Load-test runbook — blueprint §16 sign-off (ADR 0105)

Audience: SRE running a §16 validation campaign. Pre-reqs: AWS
account access to the load-test org, kubectl, helm, k6 (or
k6-operator), terraform 1.5+, psycopg installed locally for the
seeder.

## 0. Why we run this

A buyer's enterprise-evaluation team will ask:

> "Can SeDoc handle 100k concurrent users on a 100M-doc tenant,
> sustained 10k req/s with 50k bursts, p95 API < 200 ms?"

The right answer is "yes, here is the artifact." This runbook produces
that artifact in `docs/load-tests/<YYYY-MM-DD>/summary.md`.

## 0.5. Preflight (run BEFORE any spend)

`terraform apply` triggers ~$10/hour of cluster spend. Discovering a
missing IAM permission or absent state bucket 30 minutes in costs
real money. Run the preflight script first — it costs nothing and
fails fast.

```sh
./deploy/load-test/preflight.sh deploy/load-test/terraform/campaigns/$(date +%Y-%m).tfvars
```

The script verifies: terraform/helm/kubectl/aws/k6/jq installed; AWS
identity callable; the eight required IAM actions allowed (where
`iam:SimulatePrincipalPolicy` is granted); the `vaultdms-loadtest-tfstate`
S3 bucket exists with versioning + encryption + public-block; the
campaign tfvars has no `CHANGE_ME` placeholders and references a real
VPC id; and the local Helm chart is lint-clean.

If you've never run a campaign in this AWS account, do the network
stack first (one-time, ~5 min):

```sh
cd deploy/load-test/terraform
cp network.tf.example network.tf
terraform init -backend-config="key=load-test/network.tfstate"
terraform apply
# Copy the vpc_id + private_subnet_ids outputs into your campaign tfvars.
```

## 1. One-time setup per campaign (~3 hours)

```sh
# 1.1. Author your campaign tfvars from the example:
cd deploy/load-test/terraform
cp campaigns/2026-05.tfvars.example campaigns/$(date +%Y-%m).tfvars
$EDITOR campaigns/$(date +%Y-%m).tfvars   # fill in vpc_id + subnets

# 1.2. Apply infra:
terraform init
terraform plan  -var-file=campaigns/$(date +%Y-%m).tfvars -out=plan.tfplan
terraform apply plan.tfplan

# 1.3. Connect kubectl:
$(terraform output -raw kubeconfig_command)
kubectl get nodes   # confirm three node groups, all Ready

# 1.4. Deploy SeDoc via the existing Helm chart at the
#      git SHA you want to validate. Pin the SHA in
#      docs/load-tests/<date>/summary.md → "SUT version" field.
helm upgrade --install vaultdms ../../../deploy/helm/vaultdms \
  --namespace vaultdms --create-namespace \
  --values values.loadtest.yaml \
  --wait --timeout 20m
```

## 2. Seed the corpus (~8-12 hours)

```sh
# 2.1. Bootstrap the 100 tenants. Calls billing's
#      POST /internal/v1/tenants/provision 100x.
#
#      Pre-req secret (billing api-key matches the billing svc
#      --api-key flag; rotate via `helm upgrade --set
#      billing.apiKey=…`):
kubectl create secret generic loadtest-tenants \
  --from-literal=billing-api-key="$BILLING_API_KEY" \
  --namespace=vaultdms

kubectl apply  -f deploy/load-test/seed-tenants.yaml
kubectl wait   --for=condition=complete job/create-loadtest-tenants \
               --namespace=vaultdms --timeout=30m

# Capture the manifest from the PVC (you'll feed it to k6 below):
kubectl cp vaultdms/$(kubectl get pod -n vaultdms \
    -l job-name=create-loadtest-tenants \
    -o jsonpath='{.items[0].metadata.name}'):/manifest/manifest.json \
  ./tests/load/manifest.json

# 2.2. Seed 100M docs. Runs as a k8s Job so it can survive a
#      kubectl session drop. Resumable — restart the job and it
#      picks up from load_seed_progress.
#
#      Pre-req: a seeder-scoped Postgres DSN whose database name
#      contains "loadtest" (seed.py refuses otherwise):
kubectl create secret generic loadtest-seeder \
  --from-literal=database-url="postgresql://seeder:${PG_PASS}@vaultdms-postgres-rw.vaultdms.svc:5432/vaultdms_loadtest" \
  --namespace=vaultdms

kubectl apply  -f deploy/load-test/seed-corpus.yaml
kubectl logs -f job/dms-load-seed --namespace=vaultdms

# Expected throughput: ~25k rows/s × 8 workers = ~720k docs/min ≈
# 100M docs in ~2.5 h on c6i.4xlarge. Most time is the COPY into
# Postgres; OpenSearch backfill is asynchronous via the outbox
# publisher and takes longer.

# 2.3. Wait for the search index to catch up:
kubectl exec deploy/dms-search -- /app/dms-search outbox-lag
# Acceptable: lag < 60 seconds before starting the run.
```

## 3. The protocol (~2 hours 10 min)

```sh
cd tests/load
make run-protocol \
  BASE_URL=https://loadtest.vaultdms.internal/api/v1 \
  TENANTS=100 \
  TARGET_VUS=100000 \
  TARGET_RPS=10000 \
  HOLD=1h
```

Phases (k6 emits a stage marker for each):

| Phase    | Duration | VUs / RPS                  | What we measure                     |
|---------:|---------:|----------------------------|-------------------------------------|
| warm-up  | 10 min   | 5% of target               | dependency warm-up; ignore in p95   |
| ramp     | 20 min   | 5% → 100% linear           | scaling behaviour, autoscaler lag   |
| **hold** | **1 h**  | **100% (10k req/s)**       | **§16 SLOs — this is the verdict**  |
| burst    | 10 min   | 500% (50k req/s)           | burst headroom + recovery           |
| cool     | 20 min   | 100% → 0                   | leak / lingering goroutine check    |

**Chaos overlay** — fires automatically 50 min into the run (last
30 min of the hold + into burst). Kills one random pod every 5 min
across the data node group. The chaos script is at
`tests/load/chaos/chaos-tests.sh`; the operator should NOT add
anything that takes a service down for >120 s — the §16 error
budget is 0.1%, which 120s of total downtime on a 10k-req/s scenario
breaches by 12×.

## 4. Capture artifacts (~30 min)

```sh
# 4.1. Move k6's raw JSON + protocol's chaos log into the dated
#      report directory.
RUN_DATE=$(date +%Y-%m-%d)
mkdir -p docs/load-tests/$RUN_DATE/raw
mv tests/load/results/protocol-*/* docs/load-tests/$RUN_DATE/raw/

# 4.2. Generate the per-endpoint p95/p99 table from the JSON. The
#      k6 stdout summary in mixed-protocol.txt is already close;
#      see _template/README.md for the conversion checklist.

# 4.3. Export the Grafana dashboard view as PDF. The url is in
#      `terraform output -raw grafana_url`; pick the "SeDoc —
#      Service overview" dashboard, time range = the run window.

# 4.4. Fill in docs/load-tests/$RUN_DATE/summary.md from the
#      _template/summary.md. Every field is required; do not
#      ship a summary with a "—" in the verdict cell.
cp docs/load-tests/_template/summary.md docs/load-tests/$RUN_DATE/summary.md
$EDITOR docs/load-tests/$RUN_DATE/summary.md
```

## 5. Tear down

```sh
cd deploy/load-test/terraform
terraform destroy -var-file=campaigns/$(date +%Y-%m).tfvars
```

Verify in the AWS console that no NAT gateways / EKS clusters / RDS
instances tagged `vaultdms_environment_class=loadtest` survive. The
campaign cost should be bounded by the apply→destroy window.

## 6. Common failure modes

| Symptom                                                 | Cause                                                                                      | Fix                                                                                       |
|---------------------------------------------------------|--------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------|
| k6 dial errors during ramp                              | gateway autoscaler is behind; HPA needs ~2 min                                             | Increase warm-up to 15 min OR pre-scale gateway replicas to target                        |
| `409 conflict` on `CREATE document` during write phase  | dedup hash collision (synthetic content too uniform)                                       | Vary upload payload — the harness already randomises bytes; check SMALL constant          |
| Burst phase p99 spikes to >2 s                          | Postgres connection pool exhausted                                                         | Raise `PGBOUNCER_DEFAULT_POOL_SIZE`; verify `pg_stat_activity` count during burst         |
| Search p95 > 1 s                                        | OpenSearch under-provisioned OR outbox lag too high                                        | Either scale the OS node group, OR pause seeding 30 min before the run so the lag drains |
| Chaos overlay triggers >0.1% error spike                | service has no liveness/readiness probe split → traffic lands on a draining pod            | File a ticket against that service to add the probe split                                 |
| Seed throughput < 5k rows/s                             | Postgres WAL pressure or AZ-local latency                                                  | Reduce workers to 4 or move the seeder pod into the same AZ as the primary                |

## 7. Sign-off criteria (every cell green)

- [ ] mixed-realistic p95 API < 200 ms (hold phase)
- [ ] mixed-realistic p99 API < 500 ms (hold phase)
- [ ] mixed-realistic search p95 < 300 ms, p99 < 1 s
- [ ] mixed-realistic upload (10 MB scenario 11) p95 < 2 s
- [ ] burst phase error rate < 1%
- [ ] hold phase error rate < 0.1%
- [ ] chaos overlay produces zero alerts above sev-2
- [ ] OCR pipeline (scenario 4, separate run) ≥ 1000 pages/min/worker
- [ ] No tenant_id leak in cross-tenant isolation (scenario 7)
- [ ] All resource saturation panels < 80% during hold

Sign-off requires every box checked AND the SRE who ran the campaign
named in `summary.md` → "Run by".

## 8. What's NOT in this runbook (and why)

- **Soak runs > 1 hour.** §16 doesn't mandate it. If you need an
  overnight run, set `HOLD=12h` and budget the spend.
- **Per-tenant noisy-neighbor.** Phase 2 of ADR 0105.
- **Cross-region tests.** Phase 2 of ADR 0105.
- **GPU OCR throughput.** Lives in scenario 04 and needs its own
  GPU node group — out of scope for this protocol.
