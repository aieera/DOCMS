# Internal mTLS plane (ADR 0031)

Cert-manager manifests that issue a self-signed internal CA and one
client certificate per service permitted to call `/internal/*`. The
leaf certs carry DNS SANs matching the service's
`VAULTDMS_INTERNAL_SAN_ALLOWLIST`.

## Apply order

```
kubectl apply -f 00-namespace.yaml       # creates the `vaultdms-pki` namespace
kubectl apply -f 10-issuer-bootstrap.yaml   # self-signed Issuer used to sign the root
kubectl apply -f 20-ca.yaml              # the root Certificate (vaultdms-internal-ca)
kubectl apply -f 30-issuer-internal.yaml    # CA-backed Issuer consumed by leaf certs
kubectl apply -f 40-leaf-*.yaml          # one leaf per service
```

A successful apply yields a `Secret` per leaf (containing `tls.crt`,
`tls.key`, and `ca.crt`) that the service pods mount at
`/etc/vaultdms/internal-mtls/` and reference via:

```
VAULTDMS_INTERNAL_CA_CERT=/etc/vaultdms/internal-mtls/ca.crt
VAULTDMS_INTERNAL_CLIENT_CERT=/etc/vaultdms/internal-mtls/tls.crt
VAULTDMS_INTERNAL_CLIENT_KEY=/etc/vaultdms/internal-mtls/tls.key
```

See `docs/runbooks/internal-mtls-bootstrap.md` for the rotation runbook.
