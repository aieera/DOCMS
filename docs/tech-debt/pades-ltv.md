# T-D-7 — PAdES Tier-1 marker validator

**Status:** Closed (2026-05-09)
**Closed by:** [ADR 0072](../adr/0072-pades-ltv.md)

## Original debt

The Wave 9 signer interface shipped with `MockSigner.Verify` — a
regex over `%SeDoc-MockSig-` marker blocks — wired in as the
`/api/v1/signatures/verify` backend. Two production-impact gaps:

1. **Forgeable**: any PDF with the marker text typed in by hand
   verifies as "signed and valid".
2. **No LTV**: even a real PAdES-B-LT input would report
   `LTVEnabled=false` because the marker validator never looks at
   `/DSS`.

## What replaced it

`services/signature/internal/pades/` ships a pure-Go validator that
parses the PDF byte-range, verifies the CMS signature against the
signed digest, walks the certificate chain, fetches OCSP + CRL,
and reports a structured `Report` with `LTVEnabled`, `LTVAge`,
per-signature `CertStatus` (Valid / Indeterminate / Revoked), and
explicit error codes for the unsupported-PDF cases (encrypted /
object-streamed).

Sign-side: `pades.Embedder` runs after the CMS lands and before
the new version is persisted, attaching a `/DSS` dictionary +
`/VRI` entries via incremental update. RFC 3161 timestamps come
from a tenant-configured qualified TSA.

## Why it took until now

The marker validator was deliberate scaffolding — Wave 9 wanted
the REST surface + Temporal workflow + envelope state machine
exercised end-to-end, and a real PAdES validator would have
required either the DSS Java sidecar (not yet shipped per ADR 0025)
or a non-trivial pure-Go implementation. The placeholder unblocked
upstream work and the tech-debt ledger captured the risk.

## What's still pending

- **PAdES-B-LTA**: archive-timestamp re-stamping every 1-2 years.
  ADR 0072 explicitly defers this.
- **EU LOTL** trust-anchor polling. Per-tenant trust-anchor file
  is the v1 surface; LOTL is a follow-up.

Both are tracked separately; neither is in scope for closing T-D-7.
