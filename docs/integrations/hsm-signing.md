# HSM integration for server-held PAdES signing

Wave 9 Prompt 9.2.

This document describes how the signature service acquires and uses
signing keys held in an HSM (hardware security module) or cloud KMS
when `signing_mode = server_hsm` on an envelope. It pairs with
ADR 0025 (library selection) and the `signer.Signer` interface.

## Scope

Two trust domains collaborate:

1. **VaultDMS signature service (Go)** — owns envelope state, calls
   the signer per request.
2. **DSS sidecar (Java)** — produces the PAdES-B-LT bytes. Stateless.

The HSM / KMS is the third party. Private key material NEVER leaves
the HSM; the signer asks the HSM to perform a `Sign(hash)` operation
over a PKCS#1 v1.5 or PSS primitive. The signed hash returns,
embeds in the PDF's signature dictionary.

## Flow (server-held mode)

```
signature-svc (Go)                   signer sidecar (Java/DSS)        HSM / KMS
      │                                       │                          │
      ├─ Sign(request, mode=server_hsm, ─────▶│                          │
      │      kms_alias=vaultdms/tenant/XYZ)   │                          │
      │                                       ├─ GetCertificate(alias) ─▶│
      │                                       │◀── cert + chain ─────────┤
      │                                       ├─ Compute byte-range hash │
      │                                       ├─ Sign(hash, alias) ─────▶│
      │                                       │◀── signed hash ──────────┤
      │                                       ├─ Request RFC 3161 TSA    │
      │                                       ├─ Embed CMS + chain +     │
      │                                       │   OCSP/CRL (LTV)         │
      │◀── PAdES-B-LT bytes ──────────────────┤                          │
```

The signature service never sees the private key; the sidecar never
sees it either. Both only observe the hash + the signed hash.

## Supported backends

| Backend | Alias format | Status |
|---|---|---|
| Dev / on-prem local | `vaultdms/tenant/<uuid>` | ✅ via `pkg/crypto.LocalKeyManager` — HKDF-derived per-tenant key. **Not** production-grade for signing; intended for dev. |
| HashiCorp Vault Transit | `transit/keys/<key-name>` | 🟡 Wave 12 |
| AWS KMS (asymmetric) | `alias/vaultdms-signing-<tenant>` | 🟡 Wave 12 |
| PKCS#11 (on-prem HSMs: Thales, Utimaco, SoftHSM) | `pkcs11:slot=<n>;token=<label>;object=<label>` | 🟡 Wave 12 |

Dev/on-prem today uses `LocalKeyManager`; prod-grade Vault / AWS KMS /
PKCS#11 adapters are scheduled with the broader KMS prod-readiness
push (Wave 12). The sidecar's key-resolution interface is designed
so adding a backend is a 50-line class, not a sidecar rewrite.

## Certificate provisioning

For each tenant opting into server-held signing, the ops team
generates:

1. A signing certificate (X.509, 3-year validity, `Key Usage = digitalSignature, nonRepudiation`, `Extended Key Usage = id-kp-emailProtection, pdfSigning`).
2. An issuing chain that terminates in a trust anchor Adobe / macOS /
   Windows already trust. For pilot we use Entrust / Sectigo / EU
   Qualified TSP per customer region.
3. A KMS-resident private key; the certificate is bound to the alias
   so `GetCertificate(alias)` returns the chain verbatim.

## Key rotation

Signing certificates rotate every 3 years. Rotation procedure:

1. Issue a new cert under a new alias
   (`vaultdms-signing-<tenant>-v2`).
2. Update the tenant's `signature_config.active_alias` row.
3. New envelopes use v2. Old envelopes remain signed under v1 —
   their chain is still valid because v1's cert isn't revoked.
4. Revoke v1 only if the private key is suspected compromised.
   OCSP / CRL entries propagate automatically once revoked.

## Timestamp authority (TSA)

PAdES-B-T / B-LT require an RFC 3161 timestamp. Default TSA URLs:

- Dev / pilot: `https://freetsa.org/tsr` (free, non-commercial; rate
  limited).
- Prod: `https://timestamp.digicert.com` (commercial account) or the
  customer-operated TSA for regulated tenants.

The sidecar retries the TSA up to 3 times with jittered backoff; on
exhaustion it returns `ErrTSAUnavailable` and the envelope signer
step flips to `failed_transient` so a retry can succeed later.

## LTV material

For PAdES-B-LT we embed, per signer:

- Full certificate chain (leaf to trust anchor).
- OCSP responses for every cert in the chain (fetched at signing
  time).
- CRL fallback when OCSP is unreachable.

All land in the PDF DSS dictionary as per ETSI TS 102 778-4. The
Wave 9.2b sidecar delegates this to DSS's `PAdESLevelBLT` service —
the mechanism is built-in, not hand-rolled.

## Operational limits

- Sidecar memory: JVM heap 512 MB; PAdES-B-LT signing peaks at ~180
  MB for a 200 kB PDF.
- Concurrency: the sidecar serves up to 16 concurrent requests per
  pod. Exceed this and clients see backpressure via gRPC DEADLINE.
- TSA round-trip: typically 200-800 ms. Envelopes with N signers
  complete in ~N×1 s assuming sequential signing.

## Security invariants

1. Private key material never traverses the network in cleartext.
   HSM/KMS `Sign(hash)` is the only crypto operation the sidecar
   invokes.
2. The sidecar holds no tenant-scoped configuration at rest. Every
   request carries its own KMS alias + TSA URL.
3. The sidecar and signature service share a mTLS channel (Wave 12
   certs); in dev the channel is localhost-only and plain gRPC.
4. Sidecar logs NEVER include PDF bytes or key material — audit
   breadcrumbs use the `Fingerprint` field (SHA-256 of signed bytes)
   only.

## Related

- [ADR 0025 — PAdES library](../adr/0025-pades-library.md)
- [Runbook — Signature service](../runbooks/09-signature.md)
- `services/signature/internal/signer/` — Go interface
