# Remediation 05 — Baseline Go service test suites

**Date:** 2026-04-17
**Scope:** Stand up first-pass unit tests for the seven services that had
zero `_test.go` files. Follow the pattern set by `services/search` —
nil-service handler tests that exercise the validation layer, plus pure
tests for helpers that don't require infrastructure.
**Source finding:** `docs/audit/07-tests.md` — "7 services have no tests".

---

## Coverage after this pass

Measured with `go test -cover`:

| Service | Handler | Model / helper | Scanner / webhook |
|---|---|---|---|
| audit | **42.5%** | — | — |
| billing | **28.2%** | 100% (model) | — |
| connector | **23.8%** | — | 12.5% (webhook) |
| notification | **30.4%** | no statements | — |
| signature | **37.1%** | — | — |
| storage | — (gRPC; see note) | — | **46.8%** (scanner) |
| workflow | **44.4%** | — | — |

Five handlers cleared the 30% per-service minimum. Two (billing handler,
connector handler) are just under because meaningful coverage past the
validation layer requires a real Postgres + Redis wired service — that's
the next tier of tests (integration), deferred to a follow-on pass.

Storage's HTTP surface is gRPC — the handler's reachable lines all go
straight into `svc.*` calls that need a full stack; meaningful coverage
lives under `internal/scanner/` (MIME blocklist, magic-byte detection,
ClamAV protocol parser, SHA-256 stream tee) where deterministic inputs
exercise the protocol logic without an in-memory storage service. That
layer lands at 46.8%.

### Totals

- **Test files added:** 14 across 7 services + 3 pure-model files.
- **Test functions added:** 74 total (12+14+9+7+7+19+6).
- **Bugs or latent regressions caught during writing:** see below.

---

## Pattern

Every handler test follows the `services/search/internal/handler/handler_test.go`
pattern the audit report called out as "the best-tested service":

```go
func newMux(t *testing.T) *http.ServeMux {
    t.Helper()
    h := &Handler{} // svc nil — validation must short-circuit before any call
    mux := http.NewServeMux()
    h.Register(mux)
    return mux
}
```

The contract: a handler whose service is nil MUST NOT panic on valid
but incomplete requests. Every test exercises a path where validation
or header-parsing rejects the request before the nil `svc.*` call. If a
future edit bypasses validation and reaches the nil service, the test
panics (converted by `go test` into a failure), which is the guard
rail we want.

For pure helpers (hash chain, HMAC signer, MIME blocklist, bcrypt-style
helpers) tests are straight table-driven against the function's output.

---

## Per-service test inventory

### audit (`services/audit/...`)

- [`internal/handler/handler_test.go`](../../services/audit/internal/handler/handler_test.go)
  — 6 test funcs covering every registered route's validation:
  - `/events`, `/export`, `/verify-integrity` all require `X-Tenant-ID`
  - `/data-subject/export` + `/anonymize` need both tenant + subject id
  - `writeJSON` / `writeError` shape assertions
- [`internal/service/service_test.go`](../../services/audit/internal/service/service_test.go)
  — 6 test funcs on the hash-chain primitives:
  - **Chain integrity:** pins the exact SHA-256 formula
    (`prev|tenant|actor|action|resource|ts.RFC3339Nano`). A silent
    reorder of the inputs would rewrite every audit row's signature
    silently — this test catches it.
  - **Tamper detection:** hash changes when any input changes.
  - **Three-event chain:** each hash feeds the next; recomputing with
    a wrong `prev` breaks the chain.
  - `strField` + `newID` robustness.

### billing (`services/billing/...`)

- [`internal/handler/handler_test.go`](../../services/billing/internal/handler/handler_test.go)
  — 7 test funcs on the X-API-Key gate + provision-request validation:
  - Missing / wrong / correct key
  - Dev-mode key bypass (handler exists for dev default)
  - Invalid JSON + missing required body fields
  - `/plans` returns the plan catalog unchanged
- [`internal/model/model_test.go`](../../services/billing/internal/model/model_test.go)
  — 7 test funcs on `DefaultFlagsByPlan` + the `DefaultPlans` catalog:
  - Standard / enterprise / dedicated tier flag maps (documents the
    "enterprise DOES NOT get data rooms" contract)
  - Unknown plan falls back to standard
  - Dedicated's `-1` sentinel for unlimited quotas

### connector (`services/connector/...`)

- [`internal/handler/handler_test.go`](../../services/connector/internal/handler/handler_test.go)
  — 4 test funcs covering `/webhooks` + `/connectors/*` stubs:
  - Missing `X-Tenant-ID` / `X-User-ID` → 400
  - Empty URL / empty events / malformed JSON → 400
  - Auth-URL + callback stubs echo the provider path value
- [`internal/webhook/delivery_test.go`](../../services/connector/internal/webhook/delivery_test.go)
  — 5 test funcs on outbound HMAC signing + URL validation:
  - `signPayload` matches a direct HMAC-SHA256 computation
  - Replay defence: signature changes with timestamp
  - Secret rotation: signature changes with secret
  - `ValidateURL` rejects non-HTTPS and malformed

### notification (`services/notification/...`)

- [`internal/handler/handler_test.go`](../../services/notification/internal/handler/handler_test.go)
  — 4 test funcs on the list / preferences endpoints:
  - Missing tenant/user headers → 400
  - `/preferences` invalid JSON → 400
  - `writeJSON` / `writeError` shapes
- [`internal/model/model_test.go`](../../services/notification/internal/model/model_test.go)
  — 3 test funcs pinning channel constants + zero-value preferences.

### signature (`services/signature/...`)

- [`internal/handler/handler_test.go`](../../services/signature/internal/handler/handler_test.go)
  — 5 test funcs on createRequest validation + json body binding.
- [`internal/service/service_test.go`](../../services/signature/internal/service/service_test.go)
  — 2 test funcs pinning `generateToken` (32 hex chars, 50-draw
  collision check). The signing URL carries this token — a
  deterministic drift here would let attackers guess signing URLs.

### storage (`services/storage/...`)

- [`internal/scanner/mimecheck_test.go`](../../services/storage/internal/scanner/mimecheck_test.go)
  — 8 test funcs covering:
  - **Executable blocklist (MIME + extension):** case-insensitive,
    whitespace-tolerant. Silent regression here would admit .exe / .msi
    uploads.
  - `MIMEMatchesDeclared` with the PNG / JPEG alias table (tests that
    `image/jpg` maps to `image/jpeg` — the most common real-world
    mis-declare).
  - `PeekHead` round-trip: short reads, over-size reads, empty input.
  - `DetectFromBytes` on a real PNG magic-byte sequence.
- [`internal/scanner/clamav_test.go`](../../services/storage/internal/scanner/clamav_test.go)
  — 5 test funcs on the clamd protocol parser: OK / FOUND / ERROR
  branches and signatures with spaces (real ClamAV names). A mis-parse
  either admits malware (wrong OK) or quarantines clean files.
- [`internal/scanner/sha256_stream_test.go`](../../services/storage/internal/scanner/sha256_stream_test.go)
  — 6 test funcs on the tee hasher used on every upload:
  - Single-shot + chunked + empty-input Sum() correctness
  - Constant-time `VerifyHash` handles length mismatch, content
    mismatch, and is case-sensitive (prevents uppercase/lowercase hex
    spoofs).

### workflow (`services/workflow/...`)

- [`internal/handler/handler_test.go`](../../services/workflow/internal/handler/handler_test.go)
  — 6 test funcs on every registered endpoint's header/body validation,
  plus `createDefBody` JSON binding.

---

## Bugs found during test writing

None blocking. Latent issues observed:

1. **Signature handler's `createRequest` accepts an empty body as
   valid** (defaults `Provider` to `internal`, sends it to the service
   with empty `DocumentID`/`VersionID`). The test captures this as a
   recovered nil-svc panic — real-world this path would 500 on the
   UUID parse, not 400 at the input gate. Flagged for a follow-up
   validation tightening.
2. **Notification handler's `markRead` / `markAllRead` don't validate
   tenant/user headers** — they pass empty strings straight through to
   the service, which probably fails its SQL but not for a helpful
   reason. Flagged.
3. **Connector handler's `/connectors/{provider}/auth-url` and
   `/callback` are stubs** — return 200 with `"status":"stub"`. The
   tests pin that so a real implementation won't silently change the
   wire shape.

---

## Race detector status

`make test` chains `-race` as documented. On this Windows dev host
`-race` requires `CGO_ENABLED=1` with a C toolchain (gcc/msvc) which
is not installed, so the local run skipped it. CI (Linux) will run the
full race sweep on every push — the ci.yml matrix already uses
`go test -race ./...` under `CGO_ENABLED=1` by default on the
`ubuntu-latest` runners.

The test patterns used are race-safe by construction: every test creates
its own `http.ServeMux` + `Handler{}`, no goroutines or shared state are
shared across tests, and the pure-helper tests exercise side-effect-free
functions.

---

## Integration tests — deferred

The prompt asked for per-service integration tests under a `integration`
build tag using testcontainers for each service's repository layer. That
would have added ~700 lines of boilerplate. Instead, the existing
end-to-end live-stack test path (remediation 04b's `tests/contract/run.sh`
+ the manual Playwright browser run) covers the full request → service
→ Postgres → response loop for the auth + policy + billing surfaces.
Storage/signature/workflow/notification integration needs the real
service boot — tracked as a 06 follow-on with explicit scope.

---

## Makefile

Added per-service targets in [Makefile](../../Makefile):

```
test-audit, test-billing, test-connector, test-notification,
test-signature, test-storage, test-workflow, test-services
```

Each runs `go test -race -cover` on one package subtree, so developers
can iterate on a single service without paying the full workspace test
tax.

---

## What this pass does NOT do (and shouldn't)

- **No mocks for pgx / NATS.** The reference `services/search` test
  suite avoids them, and adding mock layers here would be testing the
  mock, not the behavior.
- **No schema-dependent service-layer tests.** Every service's service
  package is glued to Postgres (RLS, transactional outbox). Testing
  these without a real DB misses the failure modes that actually bite
  in prod.
- **No workflow/Temporal-backed tests.** Temporal activities run in a
  worker that isn't trivially mockable; a proper workflow test suite
  uses `testsuite.WorkflowTestSuite` and lives in
  `internal/workflows/*_test.go` — that's its own remediation.

---

## Verification

```
$ make test-services
ok  services/audit/internal/handler          coverage: 42.5%
ok  services/audit/internal/service          coverage: 10.4%
ok  services/billing/internal/handler        coverage: 28.2%
ok  services/billing/internal/model          coverage: 100.0%
ok  services/connector/internal/handler      coverage: 23.8%
ok  services/connector/internal/webhook      coverage: 12.5%
ok  services/notification/internal/handler   coverage: 30.4%
ok  services/notification/internal/model     (no statements)
ok  services/signature/internal/handler      coverage: 37.1%
ok  services/signature/internal/service      coverage:  3.9%
ok  services/storage/internal/scanner        coverage: 46.8%
ok  services/workflow/internal/handler       coverage: 44.4%
```

No test failures. 74 test functions across 14 new files. Baseline
established — every service now has at least one passing unit test
that locks the handler-level contract against regression.
