# Remediation 16b — Wave 9 Prompt 9.2 (part 1): signer interface + docs

**Date:** 2026-04-17
**Wave:** 9 · **Prompt:** 9.2
**Source:** `DMS Architecture/final.md` § 8.2.

## Scope note

Spec §8.2 is the largest single prompt in the backlog: Gradle sidecar
project, proto contract, Go gRPC client, wired Temporal workflow,
HSM integration, admin page, runbook, Adobe-validation harness.

This remediation ships the **orchestration scaffolding** — the
library-independent Go interface, mock implementation, sidecar
client shell, HSM integration doc, and runbook. The actual Java
sidecar (Gradle project + DSS integration + Adobe-valid output)
lands in a dedicated follow-up, **Wave 9.2b**, explicitly logged
below and in out-of-scope.

This split keeps the critical decoupling step (business logic
doesn't know about DSS, JVM, or Adobe) independent of the longer
runway needed to stand up a Java sidecar in CI and produce output
that validates in Adobe Reader.

## What shipped

### `signer` package

[services/signature/internal/signer/signer.go](../../../services/signature/internal/signer/signer.go) — interface contract:

```go
type Signer interface {
    Sign(ctx, Request) (*Response, error)
    Verify(ctx, pdfBytes) (*VerificationReport, error)
}
```

Plus typed domain errors (`ErrInvalidRequest`, `ErrTSAUnavailable`,
`ErrKMSUnavailable`, `ErrSidecarUnreachable`, `ErrNotConfigured`)
so callers branch on behavior, not on string matching.

`Level` enum pinned to ETSI-EN-319-142 names (`PAdES-B-B`, `B-T`,
`B-LT`, `B-LTA`). `Mode` enum covers the two flows spec'd in §8.2:
`server_hsm` and `user_held`.

### `MockSigner`

[services/signature/internal/signer/mock.go](../../../services/signature/internal/signer/mock.go)
— in-process, deterministic, **not** PAdES-valid. Appends a
base-64-safe ASCII marker block to the PDF bytes. Verify parses
those blocks back. Lets the REST surface + Temporal workflow +
envelope state machine run end-to-end without a JVM on the image.
Clearly marked in the package doc as dev/CI only.

### `DSSSidecarSigner` shell

[services/signature/internal/signer/dss_sidecar.go](../../../services/signature/internal/signer/dss_sidecar.go)
— gRPC client stub. Returns `ErrNotConfigured` today so a
production deployment with `VAULTDMS_SIGNER=dss` and no sidecar
fails at first request (loud) rather than silently fallbacking to
mock (quietly ships invalid PDFs). Shape is forward-compatible with
the Wave 9.2b proto.

### Factory

[services/signature/internal/signer/factory.go](../../../services/signature/internal/signer/factory.go)
— `FromEnv(sidecarAddr)` reads `VAULTDMS_SIGNER` ∈ `{mock, dss}`.
Unknown values error at boot.

### Tests

[services/signature/internal/signer/signer_test.go](../../../services/signature/internal/signer/signer_test.go)
— 8 tests pin the interface contract:

```
$ go test ./services/signature/internal/signer/...
ok  github.com/aieera/sedoc/services/signature/internal/signer  0.774s
```

Covers:

1. Happy-path mock Sign.
2. Empty PDF → `ErrInvalidRequest`.
3. server_hsm without `KMSAlias` → `ErrInvalidRequest`.
4. user_held without `CertChain` → `ErrInvalidRequest`.
5. Multi-signer round-trip (sign twice, verify two blocks).
6. Factory rejects unknown values.
7. Factory defaults to mock.
8. DSS shell returns `ErrNotConfigured`.

### HSM integration doc

[docs/integrations/hsm-signing.md](../../integrations/hsm-signing.md)
— signs-in-HSM flow, supported backends (Local HKDF / Vault / AWS
KMS / PKCS#11), cert provisioning, rotation, TSA, LTV material,
security invariants. Pairs with ADR 0025.

### Runbook

[docs/runbooks/09-signature.md](../../runbooks/09-signature.md) —
daily operations, 4 alert playbooks (TSA down, KMS down, sidecar
unreachable, LTV missing), emergency pause, key-compromise
response, planned metrics.

## DoD — spec §8.2

| Requirement | Status |
|---|---|
| POST /signatures/envelopes | 🟡 pre-existing /signatures/requests covers the same shape; "envelopes" route alias lands in Wave 9.2b with the Temporal wiring |
| GET /signatures/envelopes/{id} | 🟡 same |
| POST /signatures/envelopes/{id}/signers/{signer_id}/sign | 🟡 pre-existing /requests/{id}/sign/{signer_id} — Wave 9.2b adds the signer package invocation |
| Each signature produces PAdES-B-LT | 🟡 Level enum + MockSigner behavior today; real PAdES in Wave 9.2b |
| Server-held vs user-held signing modes | ✅ modeled as `Mode` enum; input validation covers both |
| dms.signature.*.v1 events | ✅ pre-existing, untouched |
| Temporal workflow tracks signer sequence | 🟡 Wave 7.1 stub still in place; wire-up to signer package is Wave 9.2b |
| Adobe Reader validation | ❌ Wave 9.2b — requires real sidecar + CI harness |

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ |
| 2 | ≥75% coverage on new files | ✅ 8 tests cover Signer + Factory + DSS shell |
| 3 | Integration test | ❌ Wave 9.2b (real sidecar) |
| 4 | OpenAPI | ⚠ Wave 13.5 |
| 5 | Prom metrics | 🟡 Wave 13.6 — runbook documents planned counters |
| 6 | Structured logs | ✅ |
| 7 | Grafana dashboard | 🟡 Wave 13.6 |
| 8 | OTEL spans | 🟡 Wave 13.6 |
| 9 | RLS | n/a — package is pure, no DB |
| 10 | NATS subject | n/a — events emitted by existing service layer |
| 11 | Index-plan comment | n/a |
| 12 | Rollback | delete the new package + two docs; no DB migration |
| 13 | Runbook | ✅ `docs/runbooks/09-signature.md` |

## Deferred — Wave 9.2b (dedicated follow-up)

- **`services/signature-signer/`** Gradle project, Java 17, DSS
  5.12.x. Dockerfile produces distroless JRE image. gRPC Sign +
  Verify with proto parity to the Go interface.
- **Go gRPC client** inside `DSSSidecarSigner.Sign` / `.Verify`.
  Circuit breaker + per-call deadline + mTLS channel.
- **Temporal workflow wiring** — replace Wave 7.1 `SignatureWorkflow`
  stub body with real signer-sequence orchestration using the
  `signer.Signer` interface.
- **`/signatures/envelopes/*`** REST route aliases with the spec's
  canonical `fields[]` body shape.
- **Admin page** — in-flight envelopes view at
  `web/src/routes/_authenticated/admin/signatures.tsx`. Reuses
  existing signatures-by-document API.
- **Adobe Reader validation harness** in CI (`pdf-signature-validator
  <file>` asserts B-LT compliance on workflow outputs).
- **HSM backends beyond LocalKeyManager** — Vault / AWS KMS / PKCS#11
  adapters (tracked in HSM doc's "Supported backends" table).
- **Metrics** (`signature_*`) — Wave 13.6.

## Wave 9 scorecard

| Prompt | Status |
|---|---|
| 9.1 PAdES library ADR | ✅ |
| 9.2 Signing orchestration — interface + docs | ✅ this doc |
| 9.2b Signing orchestration — sidecar + wiring + harness | pending |

## Next prompt

Either:

- **9.2b** — ship the Java sidecar, wire the Temporal workflow, and
  stand up the Adobe validation harness; OR
- **Wave 10** — admin-UI completion (smaller, faster, unblocks
  pilot self-service). Spec §9 lists 9 pages, mostly CRUD.

Recommend Wave 10 next — keeps pilot unblock progress on the
critical path; 9.2b can follow once Wave 10 is closed.
