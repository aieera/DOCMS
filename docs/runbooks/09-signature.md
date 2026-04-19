# Signature service — operator runbook

Wave 9 Prompt 9.2.

## Architecture recap

- Go service `services/signature` owns envelope state, Temporal
  workflow dispatch, REST / gRPC surface.
- Signer selected by `VAULTDMS_SIGNER` env:
  - `mock` (default) — Go, in-process, **not** PAdES-valid. Dev /
    CI only.
  - `dss` — gRPC client to the Java DSS sidecar (ADR 0025).
- Sidecar lives at `services/signature-signer/` (Wave 9.2b).

## Daily operations

### Starting a test signing flow locally

```bash
VAULTDMS_SIGNER=mock go run ./services/signature/cmd/server/
```

Submit an envelope:

```bash
curl -X POST http://localhost:8090/api/v1/signatures/requests \
  -H 'X-Tenant-ID: <uuid>' -H 'X-User-ID: <uuid>' \
  -d '{"document_id":"<uuid>","version_id":"<uuid>","provider":"internal",
       "signers":[{"email":"a@x","name":"Alice","role":"signer","order":1}]}'
```

### Validating a signed PDF

```bash
# With the mock signer, our own Verify endpoint round-trips markers:
curl http://localhost:8090/api/v1/signatures/verify/<document_id> \
  -H 'X-Tenant-ID: <uuid>'

# With the DSS sidecar (Wave 9.2b onward), also validate in Adobe:
pdfsig --show-signatures signed.pdf
```

Adobe Reader: open the file, click the "Signatures" panel. Every
signer should show "Signature is valid" with an LTV tick.

## Alerts and their playbooks

### `ErrTSAUnavailable` spike

- **What**: timestamp authority isn't responding. Sidecar has
  already retried 3 times.
- **Impact**: envelopes pause at the signing step. Existing signed
  PDFs unaffected.
- **Action**:
  1. `curl -I <TSA_URL>` from a signer pod — is it DNS? TLS? 5xx?
  2. If the TSA is down, failover by overriding `VAULTDMS_TSA_URL`
     to the secondary (DigiCert if FreeTSA is primary, or vice
     versa). Bounce the sidecar pods: `kubectl rollout restart
     deployment/signature-signer -n vaultdms`.
  3. If the TSA is up but latency is high: check TSA provider's
     status page; raise an incident if > 15 min.

### `ErrKMSUnavailable` spike

- **What**: the backend KMS / Vault / PKCS#11 HSM is unreachable or
  returning errors.
- **Impact**: **all** server-HSM signing pauses across all tenants
  on that KMS backend.
- **Action**:
  1. Check the KMS control plane — `vault status` / AWS KMS
     dashboard / HSM cluster.
  2. If rotation just happened, verify the new alias exists for
     every tenant that was on the old one (`dms-admin kms list`).
  3. For regional KMS: confirm the per-region cluster is healthy;
     fail over to the secondary region's KMS if the outage is
     sustained (> 30 min). Note this means new signatures in that
     region pause until failback.

### `ErrSidecarUnreachable` spike

- **What**: gRPC to the sidecar failing.
- **Impact**: all signing pauses.
- **Action**:
  1. `kubectl get pods -l app=signature-signer` — are pods up?
     Crashlooping?
  2. Check resource limits — the sidecar's 512 MB JVM heap was
     sized for 200 kB PDFs; an outlier tenant with 50 MB PDFs can
     OOM it. Temporarily bump heap: `JAVA_OPTS=-Xmx2048m`.
  3. If the sidecar is wedged on a single request (deadlock in
     DSS's PDF parser), kill the pod; Temporal replays the signing
     activity.

### "Signature valid but LTV not enabled" (Adobe Reader warning)

- **What**: signature is structurally correct but missing validation
  material.
- **Impact**: downstream auditors may reject the document.
- **Action**:
  1. Confirm the signer used `Level: PAdES-B-LT`, not B-T.
  2. Confirm OCSP / CRL URLs in the issuing chain are reachable
     from the sidecar pod (`curl -I <ocsp_url>`).
  3. If OCSP is consistently unreachable, fall back to CRL
     (`VAULTDMS_LTV_PREFER=crl`).
  4. For already-signed PDFs: re-sign with a second cosigner using
     B-LT to append the missing DSS dictionary.

## Emergency pause

To stop all new envelopes from starting signing (operator error,
key compromise, etc.):

```bash
kubectl scale deployment/signature-signer --replicas=0 -n vaultdms
```

Existing envelope state is preserved; signing steps enter the
Temporal retry loop until the sidecar comes back. For tenant-scoped
pause, flip the tenant's `signature_config.is_signing_enabled=false`
row (Wave 12.3 admin endpoint).

## Key compromise response

If a tenant's signing private key is suspected compromised:

1. Revoke the leaf cert at the issuing CA (they do this).
2. `dms-admin kms revoke --tenant <id> --alias <alias>`.
3. Provision v2 cert + alias (§ "Certificate provisioning" in the
   HSM doc).
4. Audit every envelope signed between compromise window and
   revocation; contact affected counterparties.

OCSP will start returning `revoked` for the leaf within 1 hour of
revocation at the CA. PDFs signed before revocation remain
LTV-valid because the embedded OCSP response is frozen in time.

## Metrics (Wave 13.6)

Planned Prom counters:

- `signature_envelopes_total{status}`
- `signature_sign_duration_seconds{level,mode}`
- `signature_tsa_errors_total{tsa_url}`
- `signature_kms_errors_total{backend}`
- `signature_ltv_missing_total` (alerting SLO: 0 in prod)

## Related

- [ADR 0025 — PAdES library](../adr/0025-pades-library.md)
- [HSM integration](../integrations/hsm-signing.md)
