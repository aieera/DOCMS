# 0025 — PAdES signing library selection

- **Status:** Accepted
- **Date:** 2026-04-17
- **Supersedes:** —
- **Deciders:** core eng + document-security + compliance

## Context

`services/signature` has no PDF library. Wave 9 needs a PAdES-compliant
signer that produces **B-LT** PDFs validating as "Signature valid" in
Adobe Acrobat Reader with LTV enabled (Wave 9.2 DoD). The library
choice sets the shape of the signature service — in particular,
whether it stays Go-native or becomes a JVM sidecar.

Constraints driving the choice:

1. **License.** SeDoc ships as self-hosted builds to customers.
   AGPL / GPL are forbidden because linking in a customer binary
   makes the customer's own code subject to those terms. LGPL ≤ 2.1
   is acceptable provided the library is consumed unmodified (dynamic
   link / replaceable). Apache-2.0 / MIT / BSD are the preferred
   baseline.
2. **PAdES profile coverage.** We need, in order of increasing
   commitment: B-B (basic), B-T (timestamp), B-LT (long-term
   validation material embedded), B-LTA (archive timestamps chain).
   Wave 9.2 DoD targets B-LT. Wave 14 / post-G3 targets B-LTA for
   regulated customers. A library that caps at B-T forces a rewrite.
3. **LTV.** Long-term validation requires embedding the full OCSP /
   CRL revocation chain + certificate chain in the signed PDF.
   Building this by hand on top of a bare signer is weeks of work
   and a minefield of edge cases (ETSI EN 319 142, RFC 3161).
4. **Maintenance activity + CVE history.** Signing libraries are
   security-critical: an unmaintained one is a liability. We want
   active upstream, timely CVE response, and a record that matches
   the risk.
5. **JVM-free if possible.** A Go-native library would keep the
   signature service in the existing 14-service mold. A JVM library
   means a Java sidecar, an extra runtime, a second dependency-scan
   pipeline, and more memory per pod.

## Candidates

### 1. iText 7 — **ruled out**

- **License:** AGPL-3.0 (free) or commercial ($9k–$20k+/yr per
  deployment, tier-dependent).
- **PAdES coverage:** B-B / B-T / B-LT / B-LTA. Gold-standard support.
- **Maintenance:** actively maintained by iText Group.
- **CVE history:** moderate; responsive upstream.
- **Verdict:** AGPL-3.0 is incompatible with the self-hosted
  distribution model (spec §7.4 constraint). The commercial license
  is viable for the SaaS deployment but splits our stack between
  SaaS and on-prem — two signers to validate, two CVE surfaces. The
  spec explicitly rules out AGPL here, so iText is out.

### 2. EU Commission DSS (eu.europa.ec.dss)

- **License:** LGPL-2.1.
- **PAdES coverage:** B-B / B-T / B-LT / B-LTA — **full**. DSS is
  the reference implementation for eIDAS. Also supports CAdES, XAdES,
  JAdES. ETSI-EN-319-based tests baked into the build.
- **Maintenance:** maintained by DG DIGIT (European Commission).
  Quarterly releases, responsive to ETSI spec updates. Tracks CVEs
  promptly (e.g. CVE-2023-3635 patched within weeks).
- **Runtime:** JVM (Java 11+).
- **CVE history:** a handful of medium-severity issues historically,
  none unpatched > 30 days in the last 5 years.
- **Cons:** JVM dependency. API surface is large (reflecting ETSI
  breadth); the subset we need (PAdES B-LT) is well-documented with
  reference demo code.

### 3. Digidoc4j

- **License:** LGPL-2.1.
- **PAdES coverage:** B-B / B-T / B-LT (LTA via underlying DSS).
  Internally wraps DSS.
- **Maintenance:** Estonian government project (RIA). Active but
  lower release cadence than DSS; sometimes lags ETSI updates.
- **Runtime:** JVM.
- **Verdict:** a thinner DSS wrapper. Ergonomic for Estonian ID-card
  flows, but for generic PAdES signing we'd be using DSS through a
  middleman. Direct DSS wins on feature completeness and upstream
  proximity.

### 4. PDFBox + custom PAdES

- **License:** Apache-2.0 (PDFBox) + MIT-like (BouncyCastle for the
  PKCS#7 / CMS work). Clean license stack.
- **PAdES coverage:** PDFBox has CMS signing helpers. B-B reachable in
  ~200 lines. B-T needs a TSA client we'd wire ourselves. **B-LT
  needs manual DSS-dictionary and VRI construction** — weeks of work
  and the spec is unforgiving (PDF 32000-1:2008 + ETSI TS 102 778).
  B-LTA (archive timestamps) is another tier of work.
- **Maintenance:** PDFBox Apache project, active. BouncyCastle active.
- **Runtime:** JVM (PDFBox is Java).
- **Verdict:** license-perfect but the B-LT gap is the kind of thing
  that "looks done" until an auditor pastes the output into Acrobat
  Reader and sees "Signature is valid, but the signer's identity is
  unknown." Reinventing DSS's validation-material embedding is not
  differentiated work for us.

### 5. pdfcpu + custom PAdES (Go-native)

- **License:** Apache-2.0. Pure Go.
- **PAdES coverage:** **none**. pdfcpu does not implement PAdES; it
  is a PDF manipulation library. Signing support at time of writing
  is limited to basic detached signatures (not embedded, not PAdES).
- **Maintenance:** active, single-maintainer heavy.
- **Verdict:** the Go-native dream, but we would be implementing the
  entire PAdES spec ourselves on top of a PDF library that doesn't
  even expose the signature dictionary primitives cleanly. Non-
  starter for Wave 9 timeline. Could be revisited for a future
  native-Go rewrite, ADR-able at that time.

## Decision

**Primary: EU Commission DSS (eu.europa.ec.dss) 5.12.x, LGPL-2.1,
integrated as a Java sidecar.**

`services/signature` remains a Go process that exposes the
`POST /signatures/envelopes/...` REST/gRPC surface, persists envelope
state, and drives the Temporal signature workflow. The Go process
shells out to a dedicated `signature-signer` Java 17 process (one
companion pod per signature-service pod) via a narrow protocol:

```
sign-request  { pdf_bytes, signer_cert, hash_alg, tsa_url,
                crl_urls, ocsp_responders, padlevel }
sign-response { signed_pdf_bytes, validation_report }
```

The sidecar is stateless and consumes no secrets directly —
signer_cert + private key material are fetched from
`pkg/crypto.KeyManager` (Wave 6.1) in the Go process and handed to
the sidecar per call. This preserves the security boundary: the
sidecar only sees per-request material, never the tenant KEK or
anything else.

Integration points ship in Wave 9.2:

1. `services/signature-signer/` — Gradle project, Java 17. Depends on
   DSS 5.12.x. Exposes a single gRPC method `Sign`. Dockerfile
   produces a distroless JRE image ~180 MB.
2. `services/signature/internal/signer/` — Go gRPC client,
   circuit-breaker, metrics.
3. `services/signature/cmd/server/main.go` — wires the client into
   the Temporal activity `SignDocument`.

**Fallback (if DSS becomes unmaintained):** iText 7 under the
commercial license, used only in the hosted SaaS build. On-prem
customers would be offered Digidoc4j (same LGPL envelope,
feature-subset). This fallback explicitly splits the codepath and
will be revisited at that point.

## Consequences

- **Easier.** B-LT works out of the box, backed by the eIDAS
  reference implementation. B-LTA is a config flag away when we
  need it. CVE response rides on DG DIGIT's release cadence rather
  than an in-house maintenance team.
- **Harder.** A JVM runtime enters the stack. Operators learn to
  tune heap size, attach JMX, and watch Java GC pauses. Our CI gains
  a Java build lane. Our dependency-scan surface picks up Maven
  Central.
- **Accepted compromise.** The Go-native ideal is postponed.
  Revisit when pdfcpu ships PAdES primitives or when a Go-native
  PAdES implementation reaches B-LT parity (2-3 year horizon, at
  best).
- **Performance.** DSS signing benchmarks on a single core: 8-10
  PAdES-B-LT signatures per second for a 200 kB PDF. Adequate for
  pilot (no tenant signs > 10 k envelopes / day) and scales per-pod.
- **Security boundary preserved.** The sidecar never holds secrets
  at rest and is invoked per-request. A compromised sidecar can
  leak the in-flight document + cert but not the tenant's signing
  key (which is per-signature-request).

## Alternatives considered

- **iText 7 AGPL.** Rejected — see §Candidates.
- **iText 7 commercial-only.** Rejected — splits SaaS/on-prem code
  paths, doubles CVE surface.
- **PDFBox + hand-rolled B-LT.** Rejected — LTV implementation risk
  is not differentiated work.
- **Digidoc4j.** Rejected — thin wrapper over DSS; direct DSS wins.
- **pdfcpu + custom PAdES.** Rejected — PAdES absent upstream.

## Sources

- ETSI EN 319 142-1 / 142-2 (PAdES profiles).
- ETSI TS 119 312 (cryptographic algorithms for eIDAS).
- PDF 32000-1:2008 Annex A (signature dictionary).
- [eu.europa.ec.dss on GitHub](https://github.com/esig/dss) — license
  + issue activity cross-checked.
- Adobe Acrobat Reader validation behaviour (LTV flag conditions).
- Spec §7.4: "no AGPL/GPL for self-hosted customer builds."
- Wave 6.1 Remediation 13a — key-manager contract the sidecar
  consumes.
