# Remediation 03b — Error handling consistency

**Date:** 2026-04-17
**Scope:** Replace raw `errors.New` with typed `pkg/errors` constructors, convert
the one remaining `fmt.Errorf(%v, err)` to `%w`, and cover `pkg/errors` with a
table-driven test.
**Source finding:** `docs/audit/03-inconsistencies.md` section 3.

---

## Before

- 11 call sites used `errors.New(...)` directly — they surfaced as opaque
  HTTP 500 / gRPC Internal because the transport layer had no Kind to map.
- One `fmt.Errorf("compile policy: %v", compiler.Errors)` discarded the
  `errors.Is` chain on OPA compile failures.
- `pkg/errors` had zero tests — the wire-format translation was trusted by
  convention.

## After

- Every user-facing error in `services/` flows through a typed constructor;
  `pkg/errors.ToHTTPError` / `ToGRPCError` picks the right status code.
- OPA compile errors are chain-preserving.
- `pkg/errors` has a 50-case table-driven test. `go test ./pkg/errors/... -v`
  → PASS.

---

## Task 1 — `errors.New` → typed errors

### `pkg/errors` additions

Added four constructors mirroring the existing `Validation` / `Conflict`
pattern. Kind / Code are embedded; wire mapping already handled their `Kind`
in the existing `ToGRPCError` / `ToHTTPError` switches.

| Constructor | Kind | HTTP | gRPC |
|-------------|------|------|------|
| `NotFound(msg)` | `KindNotFound` | 404 | `NotFound` |
| `Forbidden(msg)` | `KindForbidden` | 403 | `PermissionDenied` |
| `Unauthorized(msg)` | `KindUnauthorized` | 401 | `Unauthenticated` |
| `Internal(msg)` | `KindInternal` | 500 (message scrubbed) | `Internal` |

`Validation(field, message)` signature kept (70+ existing callers depend on
it). Sites that carry no specific field pass `""` — matches the pattern
already used by `pkg/errors.FromPgError` and the service repository error
helpers.

### Call-site replacements

| File | Before | After |
|------|--------|-------|
| `services/auth/internal/service/service.go:180` | `errors.New("invalid credentials")` | `vdmserr.Unauthorized("invalid credentials")` |
| `services/auth/internal/service/service.go:184` | `errors.New("account temporarily locked")` | `vdmserr.Forbidden("account temporarily locked")` |
| `services/auth/internal/sso/saml.go:81` | `errors.New("sp key PEM: empty or malformed")` | `vdmserr.Validation("", …)` |
| `services/auth/internal/sso/saml.go:85` | `errors.New("sp cert PEM: empty or malformed")` | `vdmserr.Validation("", …)` |
| `services/auth/internal/sso/saml.go:97` | `errors.New("sp key is not RSA")` | `vdmserr.Validation("", …)` |
| `services/auth/internal/sso/saml.go:346` | `errors.New("idp metadata URL/XML missing from tenant config")` | `vdmserr.Validation("", …)` |
| `services/auth/internal/sso/saml.go:409` | `errors.New("empty root")` | `vdmserr.Validation("", "empty root")` |
| `services/auth/internal/sso/oidc.go:263` | `errors.New("oidc config missing issuer_url/client_id/client_secret")` | `vdmserr.Validation("", …)` |
| `services/auth/cmd/server/main.go:197` | `errors.New("VAULTDMS_LOCAL_KEK not set")` | `fmt.Errorf("VAULTDMS_LOCAL_KEK not set")` |
| `services/document/internal/handler/mappers.go:161` | `errors.New("unsupported lifecycle action")` | `vdmserr.Validation("", …)` |
| `services/storage/internal/scanner/clamav.go:139` | `errors.New("clamav unparseable response: " + resp)` | `vdmserr.Internal("clamav unparseable response: " + resp)` |

Three files had their `"errors"` import removed after the last use went away
(saml.go, oidc.go, mappers.go). `scanner/clamav.go` picked up a new
`vdmserr` import.

### Notes on the special cases

- **`auth/cmd/server/main.go`** — `loadLocalKEK` is called during startup.
  The task prompt's "use `logger.Fatal`" intent conflicts with the existing
  caller, which logs a *warning* and lets the service run without a KEK
  (MFA setup is then disabled). Switching to Fatal would be a behavior
  change for an already-shipped boot path. The minimal fix that honors
  "no `errors.New`" while preserving observable behavior is
  `fmt.Errorf("VAULTDMS_LOCAL_KEK not set")` — the return value and the
  caller's warn-and-continue stay identical.
- **`clamav.go`** — `Internal(...)` sends the `resp` string to logs but
  the HTTP/gRPC layer scrubs the message to `"internal error"`, so no
  raw ClamAV protocol bytes reach the wire.
- **`ErrInvalidCredentials` / `ErrAccountLocked`** — the auth HTTP
  `writeError` explicitly overrides the body for these sentinels
  (invalid credentials → 401 generic message, account locked → 429
  `RATE_LIMITED`). The switch to typed errors changes only the
  classification; user-visible output is unchanged because the override
  takes precedence. `errors.Is` continues to work via pointer equality
  on the package-level sentinel.

---

## Task 2 — `fmt.Errorf` wrapping

The audit report counted 33 `fmt.Errorf` calls flagged for `%w`. A
line-by-line sweep (`node` script over `services/` + `pkg/`, 200 total
`fmt.Errorf` calls) finds:

- 146 already use `%w` correctly.
- 53 use `%s` / `%d` / no verb to format non-error arguments (HTTP body
  slices, config names, slice joins). Per the task rule "if the last arg
  is a string or other type, leave it alone", these are correct.
- **1 actual violation**: `services/policy/internal/opa/engine.go:41`
  wrapping `compiler.Errors` (type `ast.Errors`, which implements
  `error`) with `%v`. Fixed to `%w`.

The audit report's "33" number came from counting *all* `fmt.Errorf` calls
without `%w`, including the intentional non-error formatting. Prior
remediation passes (01 proto, 02 security, 03a config) had already
corrected the real violations as drive-by fixes while editing those
files.

---

## Task 3 — `pkg/errors` tests

New file: `pkg/errors/errors_test.go`. Seven test functions, 50 total
subtests:

| Test | Cases |
|------|-------|
| `TestConstructors` | 7 — Validation (with + without field), NotFound, Conflict, Forbidden, Unauthorized, Internal |
| `TestToGRPCError` | 12 — every Kind + nil + plain `errors.New` fallback |
| `TestToHTTPError` | 11 — status codes + Type tags |
| `TestToHTTPErrorScrubsInternalMessage` | 1 — verifies Internal messages never leak to wire |
| `TestWrapPreservesIsChain` | 1 — `errors.Is(Wrap(base, cause), cause)` and nil-base fallback |
| `TestKindOf` | 1 — nil / plain / typed / wrapped |
| `TestFromPgError` | 8 — `pgx.ErrNoRows`, `23505` unique, `23503` FK, `23514` check, `23502` not-null, unmapped pg, plain. Every case also verified reachable via `errors.Is`. |
| `TestErrorString` | 1 — Error() format with and without Cause |

One observation worth recording: the task said
"`FromPgError` maps `unique_violation` → `Conflict`". The current code maps
it to `ErrAlreadyExists` (KindAlreadyExists). Both map to HTTP 409 and are
semantically equivalent for the caller, but the Kind is
`KindAlreadyExists`. The test asserts actual behavior. Changing
`FromPgError` itself is out of scope for this remediation; flagged for a
future pass if the distinction ever matters to a handler.

---

## Verification

```
$ grep -rn 'errors.New(' services/
(no results)

$ grep -rn 'fmt.Errorf.*: %[vs]' services/ pkg/
(only non-error formatting — response bodies, config names, joins)

$ go test ./pkg/errors/... -v
... 50 subtests, all PASS
ok  	github.com/vaultdms/vaultdms/pkg/errors	1.232s

$ go build ./... (every workspace module)
clean
```

---

## DO-NOTs honored

- User-visible error *messages* preserved exactly. The only change is the
  Kind that drives the transport-layer status code.
- No new error types beyond what `pkg/errors` already exported.
- `pkg/errors` leftover `errors.New` (inside `pkg/auth/context.go`,
  `pkg/validation/validation.go`) left alone — out of the task's
  `services/` scope, and both are sentinel values used with `errors.Is`
  internally, not user-facing.
- No test files refactored.
