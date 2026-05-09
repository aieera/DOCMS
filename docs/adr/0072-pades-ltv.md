# ADR 0072 — PAdES-LTV: Long-Term Validation

Date: 2026-05-09
Status: Accepted
Closes: tech-debt T-D-7 ("Tier-1 marker validator passing for any PDF")

## Context

[ADR 0025](0025-pades-library.md) picked DSS (Java) as the eventual
PAdES B-LT signer + verifier. To unblock the upstream wave the
signature service shipped a **MockSigner** + a marker-based
"Tier-1 smoke validator" — a `regexp.MustCompile` that scans for
`%VaultDMS-MockSig-` blocks and reports anything matching as
"signed and valid". That has two problems we're now closing:

1. **Any document with our marker text** verifies as signed —
   including a marker block hand-typed in. Tier-1 doesn't actually
   verify a CMS signature against the PDF bytes.
2. **No LTV.** The mock never embeds OCSP/CRL responses or the DSS
   dictionary; Adobe Reader will always report "long-term
   validation: unknown" even for a real PAdES-B-LT input.

§11.3 wants:

- A real parser that locates `/Sig` dictionaries, verifies the CMS
  signature over the signed `ByteRange`, walks the embedded
  certificate chain, fetches CRL + OCSP for each cert, and emits a
  structured report.
- DSS embedding on the **finalize** side — when a vendor returns a
  signed hash (ADR 0070 / 0071) or our own signer produces a
  signature, we attach the validation material before persisting
  the new version.
- An **RFC 3161 timestamp** from a tenant-configured qualified TSA.
- Graceful **cert-expired-since-signing** handling — a B-LT
  signature stays valid past cert expiry IF the embedded OCSP
  response was issued during the cert's validity window. Without
  the LTV material a verification turns into "indeterminate", not
  "invalid".
- A **Tier-2 harness** — Adobe Reader + the EU Commission DSS demo
  validator. Manual where required, automated where possible.

## Decision

### Package layout

```
services/signature/internal/pades/
  ltv.go        — Verifier + Sign-side DSS embed orchestration.
  parser.go     — Pure-Go PDF parser: trailer, xref, ByteRange,
                  /Sig dict, /DSS dict, /VRI entries.
  cms.go        — CMS verification over the ByteRange digest.
  ocsp.go       — OCSP request/response (golang.org/x/crypto/ocsp).
  crl.go        — CRL fetch via cert.CRLDistributionPoints.
  tsa.go        — RFC 3161 client, parses TimeStampToken into a
                  trust-walked SignerInfo.
  dss.go        — Incremental-update writer that appends a /DSS
                  dictionary + /VRI entries to a signed PDF.
```

The verifier is a `*Verifier` with `Validate(ctx, pdfBytes) → Report`.
The Signer interface in `services/signature/internal/signer` swaps
its `Verify` body to delegate to this package; the marker-based
mock stays for the in-process dev path but is gated behind
`VAULTDMS_SIGNER=mock`.

### Parser

PDF parsing is scoped to **what PAdES needs**, not full PDF 1.7.
We need:

- **xref + trailer** of every revision (incremental updates stack;
  each `%%EOF` ends one). Detect them by scanning for `%%EOF` from
  the back, then for each, walk back to the most recent `xref`
  keyword + `trailer << ... >>` block.
- For the latest revision: locate any `/Type /Sig` dict via the xref
  table — these carry `ByteRange [a b c d]`, `Contents <…>`,
  optional `/Cert`, and the signed-attributes side of the CMS.
- For each signature: digest the bytes covered by ByteRange,
  decode the hex `Contents`, parse the CMS via
  `crypto/x509 + ietf-cms` (we vendor only the SignedData parser
  shape — we don't need to verify CMS encryption modes, just
  detached signatures over a digest).
- Locate `/DSS` (if any) in the document catalog. Pull `/Certs`,
  `/CRLs`, `/OCSPs` byte streams + the `/VRI` map keyed on the
  hex digest of the signature.

We do **not** support:

- Encrypted PDFs (signed PDFs must be visible to validators).
- Object streams (PDF 1.5 compressed objects). Real-world signed
  PDFs are flat by convention; if we encounter one we surface
  `Report.Errors = ["unsupported: object streams"]` rather than
  fail-closed (the bytes might still verify; the user just gets
  Tier-1 only).

### CMS verification

Detached SignedData over a SHA-256/384/512 digest of the signed
byte range. Steps:

1. Decode `Contents` from hex (PAdES strips the trailing zeros that
   pad to fixed width).
2. Parse the SignedData ASN.1 — `crypto/x509` doesn't expose this
   directly; we use a small in-package parser that pulls
   `signerInfo`, `digestAlgorithm`, `signedAttrs`, `signature`, and
   the cert chain.
3. Hash the ByteRange ourselves; compare to `messageDigest` in
   signed attrs.
4. Verify the signedAttrs hash → signature using the signer cert's
   public key.
5. Walk the cert chain to a configured trust anchor (per-tenant or
   the OS bundle).

### LTV embedding (Sign side)

After CMS lands but before we persist the new version, run:

```go
emb := pades.NewEmbedder(verifier, tsaClient)
withLTV, err := emb.Embed(ctx, signedPDF, EmbedOptions{
    UpgradeToBT:  true,   // wrap a TSA timestamp around the sig
    UpgradeToBLT: true,   // attach OCSP + CRL into /DSS
})
```

`Embed` is **incremental** — it appends a new revision to the file
rather than rewriting. That preserves the original signature's
ByteRange (which would otherwise break) and matches what Adobe
Acrobat does.

### TSA

Tenants configure a TSA URL + optional client cert via
`VAULTDMS_PADES_TSA_URL_<tenant>` (or the default
`VAULTDMS_PADES_TSA_URL` for the system-wide value). On Embed:

1. Hash the existing signature's CMS bytes (the signature, not the
   signed document — that's how PAdES-B-T chains).
2. POST a TimeStampReq to the TSA (`Content-Type: application/timestamp-query`).
3. Receive a TimeStampResp; pull out the TimeStampToken (RFC 3161).
4. Embed it as the `/Type /DocTimeStamp` revision the EU spec
   requires for B-T → B-LT promotion.

Adapter pattern matches `pkg/signing/tsp`: interface + concrete
HTTP client + mock for tests.

### Validator output

```go
type Report struct {
  SignatureCount int
  Signatures []SignatureInfo
  // LTV evidence summary
  LTVEnabled    bool      // /DSS present + every signature has VRI
  LTVAge        time.Duration  // now - youngest LTV material
  TamperEvident bool
  Errors []string  // structured codes; UI maps to copy
}

type SignatureInfo struct {
  SignerName string
  SignedAt   time.Time
  Issuer     string
  Level      Level     // PAdES-B-B / B-T / B-LT / B-LTA
  // CertStatus encodes the "graceful expiry" semantics:
  //   Valid      — cert was valid at sign time AND still valid
  //   Indeterminate — cert was valid at sign time, expired since
  //   Revoked    — cert was revoked at sign time
  CertStatus  string
  ChainValid  bool
  TimestampValid bool
  TamperEvident bool
}
```

The frontend reads LTVAge to render "LTV material from N days ago"
in the badge — so users see when a 3-year-old signature is still
verifiable purely on what's embedded.

### Tier-2 harness

Two checks:

1. **Adobe Reader**: documented manual procedure in the runbook.
   No automation; Adobe doesn't expose a CLI we can drive.
2. **EU Commission DSS demo validator**: HTTPS POST of the signed
   PDF to https://ec.europa.eu/digital-building-blocks/DSS/webapp-demo/services/rest/validation/validateSignature
   returns a JSON ETSI VR (Validation Report). We wrap that in
   `pades_tier2_test.go` with `//go:build tier2` so it's opt-in
   from the same nightly workflow that runs the sandbox tests.

### Graceful certificate expiry

Three timelines matter for a PAdES signature:

```
| sign time | now |
| cert valid              |        — case A: cert still valid
| cert valid     | cert expired |  — case B: B-LT material exists
| cert valid | cert revoked |     — case C: revocation predates sign
```

Case A: standard verify. Case B: report `CertStatus = Indeterminate`
when the OCSP/CRL embedded in /DSS predates the cert's expiry —
that's "the signature was valid at signing time and remains
verifiable". Case C: `CertStatus = Revoked`, regardless of LTV.

`Indeterminate` is the eIDAS Validation Report term; we propagate
it to the UI rather than collapsing into Valid/Invalid.

## Consequences

- **Correctness over coverage**: we parse what PAdES needs and hard-
  fail with a structured error on what we don't (encrypted PDFs,
  object streams). A signed PDF the user can't validate via us
  shouldn't silently fail validation — it should fail loudly so
  the user can fall back to Adobe Reader.
- **No JVM in the validator path**. ADR 0025's DSS sidecar stays
  the *signer* for production; we own the *verifier* in pure Go so
  the read path doesn't depend on the sidecar at all.
- **OCSP+CRL fetch is an outbound call**. Verifier caches OCSP
  responses for `nextUpdate` minutes (typical: ~1 hour); CRL
  responses for the CRL's `nextUpdate` (~7 days). Reduces hot-path
  latency on repeated verifications and keeps us under any
  TSP/CA rate limits.
- **TSA cost**: each sign that promotes to B-T issues one TSA
  request. Configurable per tenant; we don't enforce a default
  TSA URL because the choice has legal / pricing implications
  per jurisdiction.

## Out of scope

- **PAdES-B-LTA** (archive timestamps). The schema + embedder
  already handle multiple `/DocTimeStamp` revisions; the upgrade
  ticker is a follow-up (re-stamp every 1-2 years).
- **Validation against the EU LOTL** (List of Trusted Lists). The
  per-tenant trust-anchor file is the v1 surface; LOTL polling is
  a follow-up.
- **CAdES / XAdES**. Container formats out of scope for §11.3.

## Tech-debt closure

This ADR closes **T-D-7 — "marker-text Tier-1 validator"**. The
entry in the tech-debt ledger
([docs/tech-debt/pades-ltv.md](../tech-debt/pades-ltv.md)) is now
marked `closed` with a pointer back here.
