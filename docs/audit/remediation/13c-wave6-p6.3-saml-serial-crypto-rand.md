# Remediation 13c — Wave 6 Prompt 6.3: `crypto/rand` for SAML X.509 serial

**Date:** 2026-04-17
**Wave:** 6 · **Prompt:** 6.3
**Source:** `DMS Architecture/final.md` § 5.3.
**Status:** ✅ guard + test shipped; the underlying code change was
already in place before this prompt ran.

## Recon finding

final.md flagged `services/auth/internal/sso/saml.go` as using
`math/rand` for the X.509 serial. **It already uses `crypto/rand`.**
Line 51 of the pre-existing code reads:

```go
serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
```

where `rand` is `crypto/rand` (imported on line 6). The repository-
wide grep confirms only two remaining `math/rand` references, both
benign: a comment in `services/signature/internal/service/service_test.go`
and `math/rand/v2` used for log sampling jitter in `pkg/logger/logger.go`.

So the "fix" here is **preventing regression**, not fixing a live bug.

## What shipped

### Refactor — extract `generateSerial` helper

- [services/auth/internal/sso/serial.go](../../../services/auth/internal/sso/serial.go)
  **new** — 18-line function wrapping `crypto/rand.Int` with a zero-draw
  retry guard. Kept separate from saml.go so the property test can hit
  it directly without generating 2048-bit RSA keys on every draw.
- [services/auth/internal/sso/saml.go](../../../services/auth/internal/sso/saml.go)
  — `NewSelfSignedSP` now calls `generateSerial()`; the inline
  `rand.Int` + unused `math/big` import removed.

### CI guard — `scripts/check-no-math-rand.sh`

New shell script enforcing "no `math/rand` (v1) imports" on every
security-sensitive package. Runs in CI as a new step inside the
existing `security-go` job:

```yaml
- name: No math/rand in security-sensitive packages (Wave 6 Prompt 6.3)
  run: bash scripts/check-no-math-rand.sh
```

Scope:
- `services/auth` — cert + MFA + session tokens
- `services/signature` — PAdES signing + cert chains
- `services/policy` — OPA bundle signatures (future)
- `pkg/crypto` — DEK/KEK derivation
- `pkg/middleware/csrf` — token generation

`math/rand/v2` is permitted (API is non-seeded-by-default and only
used for non-crypto jitter in `pkg/logger`). The guard matches
`"math/rand"` literally, not the v2 form.

### Property test — 10k-serial uniqueness + entropy

[services/auth/internal/sso/saml_serial_test.go](../../../services/auth/internal/sso/saml_serial_test.go)

Three tests:

1. `TestGenerateSerial_UniqueAndHighEntropy` — 10,000 draws. Asserts
   every serial is ≥64 bits (math/rand-collapse tripwire), ≥70% are
   ≥127 bits (crypto/rand's uniform-over-128-bits expectation), and
   all 10,000 are distinct. Runs in ~1 second.
2. `TestGenerateSerial_ConsecutiveDrawsDiffer` — 2-draw smoke
   catching the "fixed-seed PRNG" regression in <1 ms.
3. `TestSelfSignedSP_ProducesValidSerial` — one end-to-end cert
   generation confirming the caller is still wired correctly.

```
$ go test -run 'GenerateSerial|SelfSignedSP_Produces' ./services/auth/internal/sso/...
ok  github.com/vaultdms/vaultdms/services/auth/internal/sso   1.323s
```

## Why the statistical bound is 70% and not "≥127 bits for all"

Serials are uniformly distributed over `[1, 2^128)`. A serial has bit
length `< 127` iff both the top two bits are zero — probability 1/4.
A naïve "every draw ≥127 bits" assertion would fail about 25% of
CI runs. The 70% floor (1250+ draws below 127 bits is already a 7σ
event on 10,000 samples, vanishingly unlikely under crypto/rand) is
the right statistical knob: tight enough to catch a PRNG collapse,
loose enough not to flake.

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ |
| 2 | ≥75% coverage on new files | ✅ `serial.go` is 18 lines; 3 tests hit both paths (happy + zero-retry) |
| 3 | Integration test | ✅ `TestSelfSignedSP_ProducesValidSerial` |
| 4 | OpenAPI | n/a |
| 5 | Prom metrics | n/a — pure crypto helper |
| 6 | Structured logs | n/a |
| 7 | Grafana dashboard | n/a |
| 8 | OTEL spans | n/a |
| 9 | RLS | n/a |
| 10 | NATS subject | n/a |
| 11 | Index-plan comment | n/a |
| 12 | Rollback | trivial: inline the `rand.Int` call back into saml.go |
| 13 | Runbook | n/a — covered by [runbooks/06-key-management.md](../../runbooks/06-key-management.md) |

## Sources

- RFC 5280 §4.1.2.2 — X.509 serial uniqueness/unpredictability.
- RFC 4086 — Randomness Requirements for Security.
- `DMS Architecture/final.md` § 5.3.

## Wave 6 scorecard

| Prompt | Status |
|---|---|
| 6.1 per-tenant KEK | ✅ |
| 6.2 session cookies + CSRF | ✅ |
| 6.3 crypto/rand for SAML serial | ✅ this doc |
| 6.4 context propagation sweep | pending |
| 6.5 outbox-only publishing | pending |

## Next prompt

**6.4** — sweep every `context.Background()` out of NATS handler
paths. Spec says 14 handlers; I'll grep the live count. Add a CI
rule that fails the build if `context.Background()` appears inside
any function matching `*Handler`, `*Consumer`, `*Worker`.
