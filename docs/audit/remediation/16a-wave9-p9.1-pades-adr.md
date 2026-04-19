# Remediation 16a — Wave 9 Prompt 9.1: PAdES library selection ADR

**Date:** 2026-04-17
**Wave:** 9 · **Prompt:** 9.1
**Source:** `DMS Architecture/final.md` § 8.1.

## What shipped

[docs/adr/0025-pades-library.md](../../adr/0025-pades-library.md) —
Michael-Nygard-format ADR evaluating five PAdES candidates against the
spec's selection criteria (license, profile coverage, LTV, maintenance
activity, CVE history, JVM-free preference).

- **Decision:** EU Commission DSS (eu.europa.ec.dss) 5.12.x,
  LGPL-2.1, integrated as a Java 17 sidecar. The Go signature
  service stays in the main mold and calls the sidecar per request.
- **Fallback:** commercial iText 7 for the hosted SaaS path +
  Digidoc4j for on-prem, activated only if DSS becomes
  unmaintained. Explicit split accepted as a revisit trigger.
- **Ruled out:** iText 7 AGPL (spec §7.4 constraint), PDFBox + hand-
  rolled B-LT (LTV implementation risk, non-differentiated work),
  pdfcpu + custom PAdES (no upstream PAdES primitives).

Spec's requested filename was `0024-pades-library.md`; we used
**0025** because slot 0024 was already allocated to the GDPR DSR
strategy ADR (Wave 8.3). The ADR index redirects anyone searching
for "PAdES" to the correct slot.

## DoD — spec §8.1

| Requirement | Status |
|---|---|
| ADR merged | ✅ 0025 |
| Library pinned in go.mod (or as a sidecar service if JVM) | 🟡 sidecar path chosen; pin lands in Wave 9.2 when the Go client + Gradle project are added |
| AGPL/GPL explicitly ruled out | ✅ § Candidates §1 |
| PAdES B-B / B-T / B-LT / B-LTA coverage documented | ✅ § Candidates per library |
| LTV criterion called out | ✅ § Context #3 |
| Maintenance activity + CVE history considered | ✅ § Candidates |
| JVM-free preference stated + trade-off accepted | ✅ § Consequences |
| Fallback documented | ✅ § Decision |

## Deferred (logged in out-of-scope.md)

- **Actual sidecar + Go client** land in Wave 9.2. This prompt is
  pure selection.
- **Performance validation** (spec doesn't require, but quoted
  "8-10 PAdES-B-LT / sec / core" in §Consequences needs its own
  benchmark run in Wave 13.3 chaos/load suite).
- **Threat model** for the sidecar boundary — the ADR states
  secrets flow per-request, not at rest; a dedicated threat model
  lands with the Wave 13.4 security review.

## Wave 9 scorecard

| Prompt | Status |
|---|---|
| 9.1 PAdES library ADR | ✅ this doc |
| 9.2 Signing orchestration (endpoints + workflow body + sidecar) | pending |

## Next prompt

**9.2** — Signing orchestration. Big: Gradle project for the DSS
sidecar, gRPC contract, Go client in `services/signature`, wire the
Temporal `SignatureWorkflow` stub (Wave 7.1) to the real signer,
HSM integration doc, admin in-flight-envelopes page, runbook.
Adobe Reader validation harness is the DoD.
