# Air-gapped installation

For environments where the SeDoc cluster has **no outbound
internet access**. The bundle produced by
`scripts/airgap/build-bundle.sh` contains every image SeDoc
needs; nothing is pulled at install time.

## What you need

- A Kubernetes cluster, version 1.27+. One worker can run the stack
  for evaluation; production sizing: see below.
- A **private container registry** reachable from the cluster. We
  don't ship one — Harbor, Artifactory, or ECR-inside-VPC all work.
- `helm` 3.12+ and `kubectl` configured against the target cluster.
- A workstation that can `docker load` and `docker push` to the
  private registry. This workstation does not need to reach the
  SeDoc cluster directly, but it must reach the registry.
- Storage: at least 200 GiB for Postgres WAL + snapshots, plus
  whatever the object-store needs for documents (rule of thumb:
  2× total document volume).
- An **offline license file** (see §License below).

## Install steps

### 1. Transfer the bundle

The bundle is a tarball, typically 6–10 GiB depending on variant:

```bash
# ~10 GiB for amd64 with all optional services
sha256sum sedoc-airgap-1.0.0.tar.gz
```

Verify the SHA256 against the value in the release notes. Do not
trust a bundle whose checksum doesn't match — ask for a fresh one.

### 2. Push images to the private registry

On the workstation:

```bash
tar -xzf sedoc-airgap-1.0.0.tar.gz -C sedoc-airgap
cd sedoc-airgap
PRIVATE_REGISTRY=registry.example.internal/vaultdms bash load.sh
```

`load.sh` loads every image into the local Docker daemon, re-tags it
against your private registry, and pushes. Re-run is safe — Docker
dedup makes it fast the second time.

### 3. Install the chart

```bash
kubectl create namespace vaultdms
helm install vaultdms chart/vaultdms \
    --namespace vaultdms \
    -f chart/vaultdms/values-airgapped.yaml \
    --set image.registry=registry.example.internal/vaultdms \
    --set license.offlineToken="$(cat license.jwt)"
```

The `values-airgapped.yaml` file sets:

- Image pull policy `IfNotPresent` (don't try to re-pull at rollout).
- All external URLs set to in-cluster services (no `*.vaultdms.io`).
- Telemetry endpoints default to disabled.
- Connector OAuth providers default to disabled; enable per-provider.

### 4. Bootstrap admin

```bash
kubectl -n vaultdms exec -it deploy/auth -- \
    /bin/vaultdms-ctl admin bootstrap \
        --email first-admin@example.com \
        --tenant 'Customer Inc'
# Prints a one-time invite URL valid for 15 minutes.
```

### 5. Smoke test

Port-forward and hit the health endpoints:

```bash
kubectl -n vaultdms port-forward svc/web 8080:80
curl -sf http://localhost:8080/api/v1/health
curl -sf http://localhost:8080/api/v1/readyz
```

Then walk through: log in → create folder → upload a document →
search for it. If any step 500s, check `kubectl logs` on the
corresponding service.

## License

Air-gapped and on-prem installs require an offline license JWT,
signed by Raabyt. The JWT encodes:

- Customer name.
- Tenant cap (`unlimited` for enterprise).
- Feature flags (`connectors`, `intelligence`, `ediscovery`, etc.).
- Expiry (issued 1y out by default; renewed during support cycle).

License enforcement is **non-cryptographic** — the service will
refuse to start if the JWT is missing or expired, but does not
phone home. Tampering with the JWT invalidates the signature and
the service refuses to start.

Renewing: customer receives a new `license.jwt`, updates the secret,
restarts the `auth` deployment:

```bash
kubectl -n vaultdms create secret generic vaultdms-license \
    --from-file=license.jwt=license.jwt --dry-run=client -o yaml \
    | kubectl apply -f -
kubectl -n vaultdms rollout restart deploy/auth
```

## Updates

Air-gapped customers update by repeating steps 1–3 with a new
bundle. There is no in-place rolling update from the internet; the
new bundle is always applied top-to-bottom. Kubernetes handles the
rolling pod restart, so there's no downtime as long as replicas > 1.

Before updating, read the `UPGRADE.md` shipped in that version's
bundle. Database migrations run automatically on startup of the
`auth` and `document` pods — a failed migration fails the pod's
readiness probe, so the old replica continues serving until you
roll back.

Rollback:

```bash
helm rollback vaultdms <previous-revision>
```

## Hardened / classified deployments

For customers running in classified environments (gov, defence):

- Bundle can be signed with the customer's own signing key on
  request; contact Raabyt release engineering.
- All images are based on distroless or UBI-minimal; CVE scan
  reports shipped alongside each release (Wave 14.6).
- FIPS-140-2 mode: set `security.fipsMode: true` in values;
  requires the FIPS-validated Go toolchain bundle (shipped as
  `sedoc-airgap-1.0.0-fips.tar.gz`).

## Troubleshooting

**Pods stuck `ImagePullBackOff`** — check that images were pushed
to the private registry and that the cluster nodes can reach it
(`kubectl exec -n kube-system -- curl ...`).

**Auth service won't start, log says `license: invalid signature`**
— either the license JWT is tampered or you're using a license
from another environment. Get a fresh JWT from Raabyt support.

**Postgres pod pending** — usually a PersistentVolume allocation
problem. Check `kubectl get pvc -n vaultdms` and your storage
class.

**Database migrations fail on upgrade** — the `auth` deployment's
init container runs migrations. `kubectl logs -n vaultdms
deploy/auth -c migrate` shows which migration failed. Most common
cause: customer-modified schema. Do not modify schema outside the
supported `tenant_custom_fields` mechanism.

## Pointers

- Bundle build: `scripts/airgap/build-bundle.sh`
- On-prem values: `deploy/helm/sedoc/values-onprem.yaml`
- Air-gapped values: `deploy/helm/sedoc/values-airgapped.yaml`
- Support: `support@raabyt.com` (24h turnaround on the enterprise tier)
