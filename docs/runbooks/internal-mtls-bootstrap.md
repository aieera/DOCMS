# Runbook — internal mTLS bootstrap and rotation

Scope: how to stand up or rotate the internal-auth plane defined in
ADR 0031. Covers production k8s (cert-manager) and local hacking
(mkcert).

## Production bootstrap

Prereq: cert-manager ≥ v1.14 is already installed cluster-wide. Verify
with:

```sh
kubectl -n cert-manager get deploy | grep cert-manager
```

### 1. Apply the PKI manifests

```sh
kubectl apply -f deploy/k8s/internal-mtls/00-namespace.yaml
kubectl apply -f deploy/k8s/internal-mtls/10-issuer-bootstrap.yaml
kubectl apply -f deploy/k8s/internal-mtls/20-ca.yaml
# Wait for the CA Secret to be ready:
kubectl -n vaultdms-pki wait --for=condition=Ready certificate/vaultdms-internal-ca --timeout=2m
kubectl apply -f deploy/k8s/internal-mtls/30-issuer-internal.yaml
kubectl apply -f deploy/k8s/internal-mtls/40-leaf-services.yaml
```

After the leaf certs are ready, each service's namespace will have a
`Secret` per caller — e.g. `internal-mtls-worker`,
`internal-mtls-ack-sweeper`.

### 2. Mount into service pods

Each Helm chart values file or deployment manifest adds a volume:

```yaml
volumes:
  - name: internal-mtls
    secret:
      secretName: internal-mtls-<service>
volumeMounts:
  - name: internal-mtls
    mountPath: /etc/vaultdms/internal-mtls
    readOnly: true
env:
  - name: VAULTDMS_INTERNAL_CA_CERT
    value: /etc/vaultdms/internal-mtls/ca.crt
  - name: VAULTDMS_INTERNAL_CLIENT_CERT
    value: /etc/vaultdms/internal-mtls/tls.crt
  - name: VAULTDMS_INTERNAL_CLIENT_KEY
    value: /etc/vaultdms/internal-mtls/tls.key
```

### 3. Declare SAN allowlists

Each service that hosts `/internal/*` must declare which SANs it
accepts. This is a per-service env var, NOT a cluster-wide file — the
values vary (`sweeper.ack.internal` for acknowledgement, worker SANs
for services called by Temporal, etc.).

```yaml
env:
  - name: VAULTDMS_INTERNAL_SAN_ALLOWLIST
    value: "worker.temporal.internal,sweeper.ack.internal"
```

### 4. Flip services into mTLS mode

Start in `both` for every service (default). Callers migrate one at a
time. When all callers to a given service are issuing mTLS:

```yaml
env:
  - name: VAULTDMS_INTERNAL_AUTH_MODE
    value: "mtls"
```

Watch `internal_auth_total{method="hmac"}` drop to zero before flipping
the flag. A non-zero value after flip means a caller still uses the
HMAC path and is now being rejected.

### 5. Trusted-proxy CIDRs

Every service also needs `VAULTDMS_TRUSTED_PROXY_CIDRS` set to the
CIDR(s) of the load balancer or ingress in front. An empty list with
`VAULTDMS_ENV=production` panics at boot. Check by tailing the pod log
for a `trustedproxy:` panic line.

## Rotation

cert-manager rotates leaf certs automatically at 2/3 of their lifetime
(30-day leaves → rotate every ~20 days). A Kubernetes `Secret` update
is visible to the pod via `projected` volumes, but the Go process does
not re-read the cert file after boot. Two options:

- **Rolling restart on Certificate renewal** (recommended, operationally
  simple). Add the `reloader.stakater.com/auto: "true"` annotation on
  the Deployment so it restarts when the Secret version changes.
- **Hot reload via SIGHUP** (future work, tracked as tech-debt T-D-11
  when it's opened).

## Emergency CA rotation

If the internal CA key is suspected compromised:

1. Delete the root Secret in `vaultdms-pki`:
   `kubectl -n vaultdms-pki delete secret vaultdms-internal-ca`.
2. Re-apply `20-ca.yaml`. cert-manager will mint a fresh root.
3. cert-manager will fail to renew leaf certs against the new CA
   because their `issuerRef` is unchanged — force by annotating each
   leaf Certificate:
   `kubectl annotate certificate/<name> -n vaultdms cert-manager.io/issue-temporary-certificate-` (or delete the
   leaf Secret and re-apply).
4. Roll every service pod so they pick up the new `ca.crt` AND
   `tls.crt`. Briefly, old-CA-signed clients will be rejected by
   new-CA-trusting servers — plan the rotation window accordingly.

## Local development

`scripts/run-all-services.sh` defaults to `VAULTDMS_INTERNAL_AUTH_MODE=hmac`
so you don't need mkcert to hack on the repo. To exercise the mTLS
path locally:

```sh
# One-time: install mkcert and its local root (per OS).
mkcert -install

# Then, just running run-all with the mode set bootstraps dev certs
# into ./.dev-certs/ and exports the env vars for you:
VAULTDMS_INTERNAL_AUTH_MODE=mtls ./scripts/run-all-services.sh
```

The dev certs carry all the service SANs in one leaf so every service
in the local process group trusts every caller. Prod leaves each peer
with its own cert.

## Metrics and alerts

Per-service scrape targets now emit:

- `internal_auth_total{method="mtls",outcome="ok"}` — baseline.
- `internal_auth_total{method="mtls",outcome="bad_cert"}` — alert if
  >0 for 5 min (cert expired or CA mismatch).
- `internal_auth_total{method="mtls",outcome="bad_san"}` — alert if
  >0 at all (allowlist misconfiguration).
- `internal_auth_total{method="hmac",outcome="ok"}` — rollout signal;
  expect zero after full cutover.
- `internal_auth_total{method="hmac",outcome="skew"}` — clock drift
  across the cluster; >0 suggests NTP drift >5 min.
