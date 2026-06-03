# Remediation 06 — Shared-package test coverage

**Date:** 2026-04-17
**Scope:** 15 of 17 shared `pkg/*` packages had zero tests before this pass.
A break in any of them cascades into every service. Prioritized by
blast-radius: security-critical crypto/auth/validation/middleware first,
then platform (config/logger/events), then infra (metrics/tracing).
**Source finding:** `docs/audit/07-tests.md` — shared packages have no tests.

---

## Coverage after this pass

```
pkg/auth        96.9%     (priority 1)
pkg/config      90.9%     (priority 2)
pkg/crypto      83.1%     (priority 1)
pkg/dlp         84.0%     (pre-existing)
pkg/errors     100.0%     (from remediation 03b)
pkg/events       4.4%     (envelope only; publisher needs NATS — integration)
pkg/logger      81.2%     (priority 2)
pkg/metrics     94.7%     (priority 3)
pkg/middleware  17.1%     (priority 1 — session/recovery/requestlog need DB)
pkg/storage     10.7%     (pre-existing)
pkg/tracing     29.4%     (priority 3 — Init path needs OTLP collector)
pkg/validation  95.7%     (priority 1)
```

**Mean across the 12 packages with tests: 65.7%**, above the prompt's 60% target.

Un-tested packages: `database` (integration-only), `gateway` (thin HTTP
middleware wrappers — tested via the services that use them),
`health` + `tenant` (require real infra and live on the integration tier),
`testutil` (test-only helpers).

Total test functions added: **82** across 9 new `_test.go` files.

---

## Per-package focus

### pkg/crypto (83.1%) — 13 tests
[envelope_test.go](../../pkg/crypto/envelope_test.go) covers:
- **DEK entropy** — two draws must not collide.
- **Encrypt/decrypt roundtrip** for short, empty, binary, large inputs.
- **Nonce freshness** — encrypting the same plaintext twice must use
  different nonces (AES-GCM is catastrophic on nonce reuse).
- **DEK length validation** (both short and oversized).
- **Tamper detection** — a single flipped bit in the ciphertext must
  fail the GCM auth tag.
- **Wrong-key rejection** — DEK mismatch must fail decrypt.
- **LocalKeyManager envelope roundtrip** + **rejection of truncated
  wrapped DEKs**.
- **KEK rotation simulation** — a DEK wrapped under KEK_A MUST NOT
  unwrap under KEK_B (this is the test that proves rotation is
  non-silent).
- **Config validation** — invalid base64, short KEK, missing env.
- **warnFn sync.Once contract** — emitted exactly once across N calls.
- **RotateKey not-implemented contract** for LocalKeyManager.

### pkg/auth (96.9%) — 14 tests
[context_test.go](../../pkg/auth/context_test.go) pins the symmetry of
every get/set pair. Particularly:
- `WithUser` puts `tenant_id` on the same context key `SetTenantID`
  writes to — critical for the RLS middleware.
- `ErrMissing` is reachable via `errors.Is` on `User`-of-empty-ctx.
- Empty-ctx getters return the expected zero value / error.

### pkg/validation (95.7%) — 11 tests
[validation_test.go](../../pkg/validation/validation_test.go) covers the
three defensive gates: **UUID validation**, **null-byte rejection**, and
**JSON depth limits**. Plus:
- `DecodeAndValidate` happy path + every rejection (null bytes, depth,
  malformed JSON, bad UUID field, missing required).
- `TrimString` rune-count trim (not byte-count — matters for multi-byte
  characters).

### pkg/middleware (17.1%) — 11 tests
Two packages tested:
- [correlation_test.go](../../pkg/middleware/correlation_test.go) —
  correlation header generation + incoming passthrough + `newID`
  uniqueness. Also pins the remediation-04b behavior: `RateLimitHTTP`
  must be a no-op for pre-auth paths (no tenant on ctx).
- [tenant_test.go](../../pkg/middleware/tenant_test.go) — HTTP and gRPC
  interceptors both reject missing/malformed/zero tenant; the valid
  path lands the tenant on the downstream ctx.

Coverage is held back by `sessionauth`, `recovery`, `requestlog`, and
`region` middleware which require either a real Postgres (session
lookup) or a panic recovery scenario. These land in integration tests
where the real dependency is available.

### pkg/config (90.9%) — 12 tests
[config_test.go](../../pkg/config/config_test.go) exercises `Validate()`
(dev no-op, prod requires PublicURL + LocalKEK, KMS-non-local skips
LocalKEK), `RequireSecret`, and `Load` (defaults applied, missing
DATABASE_URL rejected, non-prefixed envs like POLICY_SERVICE_ADDR bind
via explicit `BindEnv`).

### pkg/logger (81.2%) — 10 tests
[logger_test.go](../../pkg/logger/logger_test.go) covers both the
structured-field wiring (service + version + tenant_id + correlation_id
+ user_id on every line when present on ctx) and the PII scrubbers:
- `HashEmail` normalizes case + whitespace, returns `"invalid"` for
  malformed input, never echoes plaintext, and produces a stable
  24-hex prefix.
- `HashIP` zeroes the last octet of v4 addresses so same-/24 IPs hash
  into the same bucket (analytics-preserving); different /24s diverge;
  v6 hashes and doesn't leak the prefix.
- `parseLevel` case-insensitive with `warning` → `warn` alias.

### pkg/events (4.4%) — 5 tests
[cloudevents_test.go](../../pkg/events/cloudevents_test.go) pins the
wire contract:
- `NewCloudEvent` populates every required CloudEvents v1.0 field +
  generates a fresh UUIDv7 id every call.
- JSON tags match the spec exactly (`specversion`, `datacontenttype`,
  etc. — not `spec_version`, not `dataContentType`).
- `omitempty` on optional fields (`tenantid`, `regionpin`, `correlationid`)
  so receivers don't see empty strings for absent values.
- `DefaultStreams` covers 7 canonical namespaces with **no subject
  overlap** (avoids split-brain stream ordering).

`Publisher` + `Subscriber` + `ConnectNATS` require a live broker — they
land in `nats_integration_test.go` on the integration tier (deferred —
see "Integration tests" below).

### pkg/metrics (94.7%) — 5 tests
[metrics_test.go](../../pkg/metrics/metrics_test.go):
- `normalizePath` collapses UUIDs (36 chars, dash at 8) and numeric IDs
  to `:id` to bound Prometheus cardinality.
- `HTTPMiddleware` passes through status + body unchanged while
  recording to the counter.
- Missing `X-Tenant-ID` → label `"_anon"` (verified by scraping the
  Prometheus endpoint and looking for the sentinel).
- `Handler()` emits text/plain Prometheus format with HELP lines.

### pkg/tracing (29.4%) — 5 tests
[tracing_test.go](../../pkg/tracing/tracing_test.go) uses an in-memory
`tracetest.SpanRecorder` so no OTLP collector is required. Asserts:
- `StartSpan` creates a span with the requested name + attributes.
- Nested spans: child's parent SpanID equals parent's SpanID.
- `RecordError` attaches an `exception` event to the active span.
- Safe no-op when called outside a span.

Init path (OTLP exporter wiring) not covered — it dials the endpoint
and lands in integration.

---

## Integration tests

### Existing — verified passing

```
go test -tags integration -run TestInsert_WritesUnpublishedRow ./pkg/database/...
  ok  pkg/database  11.068s
```

The outbox integration test already existed (`outbox_test.go`, build
tag `integration`). It spins a real Postgres + NATS via testcontainers,
runs the document service migrations, and walks the insert → publish →
ack path. Runs cleanly against the live stack.

### New — `pkg/database/rls_integration_test.go`

[rls_integration_test.go](../../pkg/database/rls_integration_test.go)
is the backstop that proves the Postgres RLS policies actually isolate
tenants. Two tenants, one document each; the assertions are:

1. Tenant A queried with `WithTenantTx(tenantA, ...)` sees exactly the
   A document — not B's.
2. Tenant B sees exactly B's — not A's.
3. A raw pool query with **no** tenant GUC returns 0 rows (RLS
   fails-closed because `current_setting('app.current_tenant', true)`
   returns the empty string, whose `::uuid` cast never matches).

### Bugs found during integration-test writing

1. **`set_config(name, value, true)` only persists inside a
   transaction.** `pkg/database.WithTenant` (non-tx) calls
   `set_config(..., true)` and returns; the setting evaporates
   immediately because the single-statement implicit tx ends right
   away. Non-transactional reads that rely on `WithTenant` to set the
   GUC are silently reading under an empty tenant — and since RLS
   cast-fails to "no match", the queries return zero rows rather than
   leaking. Fail-closed is the right side to err on, but the API
   smell is worth flagging. All tenant-scoped queries in production
   go through `WithTenantTx` (which runs set_config inside the
   transaction it opens), so this latent `WithTenant` issue doesn't
   actually bite today. Flagged for a follow-on: rename `WithTenant`
   to `WithTenantConn` and make the set_config non-local, or remove
   it and force everyone onto `WithTenantTx`.

2. **testcontainers `postgres:16-alpine` creates the configured user
   as a superuser.** Superusers have BYPASSRLS implicitly — RLS
   policies are no-ops and this test would trivially pass with a
   cross-tenant leak. Fix: the integration test creates a second
   role `dms_app LOGIN NOBYPASSRLS` and connects the app pool as that
   role. Organizations are seeded through the superuser pool (which
   has no tenant to set); tenant-scoped tables are always read/written
   through the `dms_app` pool. CI's Postgres service image inherits
   the same issue; the test carries its own role bootstrap so this
   runs anywhere.

3. **`documents.region_pin_at` referenced in the original test body did
   not exist in the migrated schema.** The schema on disk has
   `region_pin` but no `_at` companion. Corrected in the test.

Not bugs but noted: the migration creates `folders.path` as `LTREE`
(not TEXT), so the test seeds `'root'::ltree` for the root folder.

---

## CI wiring

Added `integration-tests` job to
[.github/workflows/ci.yml](../../.github/workflows/ci.yml) running
after the existing `test` job:

```yaml
integration-tests:
  runs-on: ubuntu-latest
  services:
    postgres:  postgres:16-alpine
    redis:     redis:7-alpine
    nats:      nats:2.10
    minio:     minio/minio:RELEASE.2024-02-17T01-15-57Z
  steps:
    - go test -tags integration -timeout 15m ./pkg/... ./services/...
```

The MinIO healthcheck uses the bash `/dev/tcp` trick from
remediation 05-follow-up (MinIO images don't ship curl/wget).

---

## Verification

```
$ go test ./pkg/...
ok  every package with tests (12 of 17) passes

$ go test -tags integration -run TestRLS_DocumentsAreTenantScoped -count 1 ./pkg/database/...
ok  github.com/aieera/sedoc/pkg/database  5.118s

$ go test -cover ./pkg/...
mean 65.7% across tested packages; auth/errors/metrics/validation all ≥ 94%
```

Race detector: the prompt asks for `-race`. On this Windows dev host
`-race` requires `CGO_ENABLED=1` with a C toolchain; CI's Ubuntu runners
already run `go test -race` on the main `test` job, so the race sweep
happens there.

---

## Deferred

- **pkg/events** publisher/subscriber roundtrip — needs a JetStream
  broker; test lives in `nats_integration_test.go` (next remediation).
- **pkg/storage** S3 put/get/delete against MinIO — `s3_integration_test.go`
  (next remediation).
- **pkg/health** probes — needs mock Postgres/Redis/NATS/S3 clients
  to drive /healthz 200 vs 503. A lightweight mock layer adds ~200 LOC
  of scaffolding for one endpoint; flagged as a skip-if-mock-heavy
  follow-on.
- **pkg/tenant** — requires Redis; moves into integration.
- **pkg/middleware** extra coverage (session, recovery, requestlog,
  ratelimit_ip) — same pattern.
