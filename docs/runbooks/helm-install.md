# Helm install / upgrade / rollback runbook

*ADR 0092 — SeDoc Helm chart at `deploy/helm/sedoc/`.*

This runbook is the operational companion to the chart. Three audiences:

1. **First-time installer** — bringing the chart up against a fresh
   namespace + dependency operators.
2. **Upgrader** — pulling new images / new chart versions onto an
   already-running deploy.
3. **Migrator** — moving the Postgres backend from Bitnami's
   StatefulSet to the CloudNativePG operator (the only non-trivial
   migration in scope right now).

---

## Prerequisites

- Kubernetes 1.27+ (PodSecurityStandards GA)
- Helm 3.13+ (`helm version --short` should print `v3.13` or newer)
- `kubectl` configured against the target cluster + namespace
- A `vaultdms-postgres-credentials` Secret (or ESO + Vault wired
  to produce one — see §"ExternalSecrets" below)
- A storage class capable of `ReadWriteOnce` for Postgres + OpenSearch

If using **CloudNativePG** (opt-in Postgres operator):

```bash
helm repo add cloudnative-pg https://cloudnative-pg.github.io/charts
helm install cnpg cloudnative-pg/cloudnative-pg \
  --namespace cnpg-system --create-namespace
kubectl wait --for=condition=Available deployment/cnpg-cloudnative-pg \
  --namespace cnpg-system --timeout=2m
```

If using **External Secrets Operator** (recommended for prod):

```bash
helm repo add external-secrets https://charts.external-secrets.io
helm install external-secrets external-secrets/external-secrets \
  --namespace external-secrets --create-namespace
# Then configure your ClusterSecretStore pointing at Vault / AWS SM / etc.
```

---

## Fresh install

### 1. Create + label the namespace

```bash
kubectl create namespace vaultdms
kubectl label namespace vaultdms \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/warn=restricted
```

OR let the chart create + label it for you:

```bash
helm install vaultdms deploy/helm/sedoc \
  --namespace vaultdms --create-namespace \
  --set global.podSecurity.createNamespace=true
```

### 2. Pull subchart dependencies

```bash
cd deploy/helm/sedoc
helm dependency update
ls charts/  # should show postgresql-*.tgz, redis-*.tgz, etc.
```

### 3. Create the Postgres credentials Secret (skip if using ESO)

```bash
kubectl -n vaultdms create secret generic vaultdms-postgres-credentials \
  --from-literal=username=vaultdms \
  --from-literal=password="$(openssl rand -hex 32)" \
  --from-literal=database=vaultdms
```

### 4. Install

```bash
helm install vaultdms deploy/helm/sedoc \
  --namespace vaultdms \
  --set global.domain=dms.example.com \
  --set global.tlsSecretName=vaultdms-tls \
  --set global.imageTag=$(git describe --tags --always) \
  --wait --timeout 10m
```

### 5. Smoke test

```bash
helm status vaultdms -n vaultdms
kubectl get pods -n vaultdms

# All Deployments Ready?
kubectl get deployments -n vaultdms

# Run migrations (one per service that owns a schema)
kubectl exec -n vaultdms -it deploy/vaultdms-document -- \
  /app/dms-admin migrate up

# Hit /healthz
kubectl port-forward -n vaultdms svc/vaultdms-document 8081:8081 &
curl -fsS http://localhost:8081/healthz
```

### 6. Verify PSS enforcement

```bash
# Should report all three labels = restricted
kubectl get namespace vaultdms -o jsonpath='{.metadata.labels}' | jq .

# Try to deploy a privileged pod — admission MUST reject
kubectl apply -n vaultdms -f - <<EOF
apiVersion: v1
kind: Pod
metadata: { name: psp-violation-test }
spec:
  containers:
  - name: bad
    image: busybox
    securityContext: { privileged: true }
EOF
# expected: Error from server (Forbidden): pods "psp-violation-test"
#           is forbidden: violates PodSecurity "restricted:latest"
```

---

## Upgrade

```bash
git pull
cd deploy/helm/sedoc
helm dependency update

# Dry-run renders the diff so you see what's about to land
helm upgrade vaultdms deploy/helm/sedoc \
  --namespace vaultdms \
  --reuse-values \
  --set global.imageTag=$(git describe --tags --always) \
  --dry-run --debug | head -200

# Actual upgrade
helm upgrade vaultdms deploy/helm/sedoc \
  --namespace vaultdms \
  --reuse-values \
  --set global.imageTag=$(git describe --tags --always) \
  --wait --timeout 15m
```

If the upgrade includes new migrations:

```bash
# Run before flipping traffic
kubectl exec -n vaultdms -it deploy/vaultdms-document -- \
  /app/dms-admin migrate up
```

---

## Rollback

Always one command:

```bash
helm rollback vaultdms <revision> -n vaultdms --wait
```

Find the revision with `helm history vaultdms -n vaultdms`. Rollback
applies the chart + values from the chosen revision; **it does NOT
roll back DB migrations**. If the failed upgrade ran migrations that
the previous code version can't read, the data migration must be
hand-rolled.

---

## Postgres operator migration (Bitnami StatefulSet → CloudNativePG)

**This is destructive if done wrong. Read all three sections before
starting.**

### Strategy

Two clusters run in parallel during the migration:

1. **Source**: existing Bitnami `vaultdms-postgresql` StatefulSet
2. **Target**: new operator-managed Cluster CR (`vaultdms-postgres`)

A `pg_dump` from (1) is restored into (2). The application keeps
pointing at (1) until the restore is verified, then a `helm upgrade`
flips `global.database.host` from the Bitnami service name to the
operator's service name AND disables the Bitnami subchart.

### Pre-flight

```bash
# Take a fresh full dump of the running Bitnami cluster
kubectl exec -n vaultdms vaultdms-postgresql-0 -- \
  pg_dump -U sedoc -Fc vaultdms > /tmp/vaultdms-pre-migrate.dump

# Verify the dump
ls -lh /tmp/vaultdms-pre-migrate.dump  # should be >0 bytes

# Snapshot the PVC for emergency rollback
kubectl get pvc -n vaultdms -o yaml | grep -A 5 postgresql
```

### Stand up the operator-managed cluster

```bash
# CRDs already installed? (prereqs §)
kubectl get crd clusters.postgresql.cnpg.io >/dev/null || {
  echo "Install CloudNativePG operator first (see prereqs)"
  exit 1
}

# Render the Cluster spec
helm upgrade vaultdms deploy/helm/sedoc \
  --namespace vaultdms \
  --reuse-values \
  --set postgresql.useOperator=true \
  --wait

# Wait for the new cluster to be Healthy
kubectl wait -n vaultdms cluster.postgresql.cnpg.io/vaultdms-postgres \
  --for=jsonpath='{.status.phase}'=Cluster\ in\ healthy\ state \
  --timeout 5m

kubectl get cluster.postgresql.cnpg.io -n vaultdms
```

### Restore into the new cluster

```bash
# Copy the dump into the new primary
PRIMARY=$(kubectl get pod -n vaultdms -l cnpg.io/cluster=vaultdms-postgres,role=primary -o name)
kubectl cp /tmp/vaultdms-pre-migrate.dump vaultdms/${PRIMARY##pod/}:/tmp/dump

# Restore (this drops + recreates the vaultdms DB inside the cluster)
kubectl exec -n vaultdms $PRIMARY -- \
  pg_restore -U sedoc -d sedoc --clean --if-exists /tmp/dump

# Verify row counts match
kubectl exec -n vaultdms vaultdms-postgresql-0 -- \
  psql -U sedoc -d sedoc -c "SELECT count(*) FROM documents;"
kubectl exec -n vaultdms $PRIMARY -- \
  psql -U sedoc -d sedoc -c "SELECT count(*) FROM documents;"
# numbers MUST match
```

### Flip the application

```bash
# Update the app's DB host + disable Bitnami in one helm upgrade
helm upgrade vaultdms deploy/helm/sedoc \
  --namespace vaultdms \
  --reuse-values \
  --set postgresql.useOperator=true \
  --set postgresql.enabled=false \
  --set global.database.host=vaultdms-postgres-rw \
  --wait --timeout 10m

# Watch pods cycle
kubectl get pods -n vaultdms -w
```

### Verify, then clean up

```bash
# Smoke the application end-to-end before deleting the old PVC
kubectl port-forward -n vaultdms svc/vaultdms-document 8081:8081 &
curl -fsS http://localhost:8081/healthz

# Once you're satisfied, reclaim the Bitnami volume
kubectl delete pvc -n vaultdms data-vaultdms-postgresql-0
```

If anything looks wrong AT ANY POINT: `helm rollback vaultdms <prev> -n vaultdms`.
The old Bitnami cluster + its PVC are still there until the explicit
delete above.

---

## ExternalSecrets

Enabling requires (a) an installed ESO and (b) a `ClusterSecretStore`
pointing at your secret backend.

### Vault example

```bash
# 1. ClusterSecretStore (one-time, namespace-scoped or cluster-scoped)
kubectl apply -f - <<EOF
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata: { name: vaultdms-vault }
spec:
  provider:
    vault:
      server: https://vault.example.com
      path: secret
      version: v2
      auth:
        kubernetes:
          mountPath: kubernetes
          role: vaultdms
EOF

# 2. Populate Vault with the values the chart expects
vault kv put secret/vaultdms/local-kek value="$(openssl rand -hex 32)"
vault kv put secret/vaultdms/gateway-secret value="$(openssl rand -hex 32)"
# … etc.

# 3. helm upgrade with ESO enabled
helm upgrade vaultdms deploy/helm/sedoc \
  --namespace vaultdms \
  --reuse-values \
  --set externalSecrets.enabled=true \
  --set externalSecrets.secretStoreRef.name=vaultdms-vault \
  --wait
```

### Check sync

```bash
kubectl get externalsecret -n vaultdms
# STATUS column should show SecretSynced=True for each row

kubectl get secret vaultdms-platform-secrets -n vaultdms -o yaml | \
  grep SEDOC_LOCAL_KEK   # should be present (base64-encoded)
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Pods stuck in `CreateContainerConfigError` | Referenced Secret doesn't exist | `kubectl get secret -n vaultdms` — create it manually or enable ExternalSecrets |
| Pods fail with `violates PodSecurity "restricted"` | A workload's securityContext lost the required fields | Inspect the Deployment YAML; ensure capabilities.drop=[ALL], seccompProfile, runAsNonRoot all set |
| `helm upgrade` hangs on rollout | A new Deployment fails its readiness probe | `kubectl describe pod -n vaultdms <pod>` — usually env-var or volume mount problem |
| CloudNativePG Cluster stays in `Setting up primary` | Operator CRDs not installed OR storage class doesn't support RWO | `kubectl get crd | grep cnpg` + `kubectl get sc` |
| ExternalSecret status `SecretSyncedError` | ClusterSecretStore reference wrong, or the remote key doesn't exist | `kubectl describe externalsecret -n vaultdms <name>` — error message names the missing key |

---

## What's NOT in this runbook

- OpenSearch operator migration (deferred per ADR 0092)
- NATS operator migration (deferred)
- MinIO operator wiring (no Helm dep yet)
- Kong ingress controller migration (current chart runs Kong as a
  Deployment, not as an ingress controller)
- kind/minikube local install (works with the commands above by
  pointing kubectl at the local cluster)

Each of these is its own ADR + runbook when prioritised.
