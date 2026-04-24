# Tech-debt ledger

Active register of items the engineering team has consciously
deferred and owes follow-up on. **Not a synonym for the
out-of-scope ledger** (`docs/backlog/out-of-scope.md`) — that one
captures "features noticed mid-prompt but not in the current wave's
scope." This one captures "shipped with known debt."

Format: `id | sub-wave | impact | summary | owner queue | fix
pointer`. Every row ends life with either a fix PR or a promotion
to a dedicated follow-up wave.

## Active entries

### T-D-1 — `/internal/*` sweeper endpoints relied on shared gateway secret (RESOLVED 2026-04-24)

- **Opened:** 2026-04-23 · Wave 15.1 — **Resolved:** 2026-04-24 (ADR 0031).
- **Impact:** Medium (auth, lateral).
- **What was broken:** The worker → ack-service handshake
  authenticated via `RequireGatewaySignature + TenantHTTP`. Any
  in-cluster service holding `VAULTDMS_GATEWAY_SECRET` could POST to
  `/internal/v1/acknowledgement/sweep-reminders` with an arbitrary
  `X-Auth-Tenant-ID` and trigger reminder/escalation side-effects
  for that tenant.
- **Fix:** `pkg/internalauth` is a new single auth plane for
  `/internal/*`. mTLS validates the peer cert chain against the
  internal CA and requires the client cert's DNS SAN to appear in a
  per-service allowlist. HMAC (dual-mounted during rollout) binds
  method + path + timestamp + body with a 5-minute skew window.
  `internalauth.Mux` is path-aware: `/internal/*` goes through the
  verifier, `/api/*` keeps `RequireGatewaySignature`, kube probes
  bypass both. Wired into acknowledgement, audit, auth,
  notification, policy, search, signature, workflow. See ADR 0031
  and `docs/runbooks/internal-mtls-bootstrap.md`.

### T-D-2 — `realClientIP` trusted right-most XFF hop (RESOLVED 2026-04-24)

- **Opened:** 2026-04-23 · Wave 15.2 — **Resolved:** 2026-04-24 (ADR 0031).
- **Impact:** Medium (geofence bypass, rate-limit bypass, audit-log
  spoofing).
- **What was broken:** Four middlewares (geofence, request log, rate
  limiter, ack handler) each reimplemented `realClientIP` with
  incompatible, all-wrong logic; none consulted a trusted-proxy
  allowlist. Rate limiter and request log took the left-most XFF
  hop, which is attacker-controlled; geofence and ack handler took
  the right-most hop unconditionally.
- **Fix:** `pkg/trustedproxy.RealClientIP` walks XFF right-to-left,
  only believing a hop whose immediate predecessor is in
  `VAULTDMS_TRUSTED_PROXY_CIDRS`. `VAULTDMS_ENV=production` with an
  empty CIDR list panics at boot. All four prior callers now
  delegate. See ADR 0031.

### T-D-3 — `services/acknowledgement/internal/service/service.go` is a god object (RESOLVED 2026-04-24, commit 768e678)

- **Opened:** 2026-04-23 · Wave 15.1 — **Resolved:** 2026-04-24
  (commit `768e678`).
- **Impact:** Low (maintainability).
- **What was:** Campaign CRUD + Acknowledge + HMAC + chain + sweeper +
  notification fan-out all in one 829-LOC file.
- **Fix:** split into `campaign_service.go`, `assignment_service.go`,
  `attestation_service.go`, `sweeper_service.go`, `event_service.go`.
  Tests moved to matching `_test.go` files. Receivers stay on
  `*Service` so the public surface is byte-identical.

### T-D-4 — Profile-delete split-tx orphans S3 objects on crash (RESOLVED 2026-04-24, commit 29abe8f)

- **Opened:** 2026-04-23 · Wave 15.4 — **Resolved:** 2026-04-24
  (commit `29abe8f`).
- **Impact:** Low (operability, storage leak).
- **What was:** tx1 Revoke → S3 delete → tx2 HardDelete. A process
  kill mid-flight left `revoked_at IS NOT NULL` + `image_ref` still
  set + S3 object still present.
- **Fix:** daily 03:00 UTC Temporal schedule per tenant
  (`signature-profile-orphan-sweep-<tenant>`). Workflow invokes
  `POST /internal/v1/signature/orphan-sweep` on the signature service
  which: `ListOrphans` (revoked_at < now - 24h AND image_ref set,
  LIMIT 500) → per row S3 DeleteObject → ClearImageRef (keeps
  revoked_at for audit) → `dms.signature.profile.orphan_swept.v1`
  outbox. Chaos drill at
  `docs/chaos/scenarios/09-signature-orphan-sweeper-kill.md`.

### T-D-5 — Workflow activity HTTP calls share DefaultClient (RESOLVED 2026-04-24, commit 0938f52)

- **Opened:** 2026-04-23 · generic — **Resolved:** 2026-04-24
  (commit `0938f52`).
- **Impact:** Low (resilience).
- **What was:** `http.DefaultClient` with no `Timeout`; under slow
  downstream, a goroutine + socket pinned until the workflow-level
  StartToCloseTimeout fired.
- **Fix:** `services/workflow/internal/activities/httpclient.go`
  declares a package-local `httpClient` with `Timeout: 10s` +
  `otelhttp.NewTransport(http.DefaultTransport)`. All six call sites
  across `wave15.go` + `dsr_crossservice.go` migrated. `.golangci.yml`
  enables `forbidigo` with a pattern banning `http.DefaultClient`
  references (test files excluded).

### T-D-6 — `StaticRecipientResolver` silently replaces `nil` (RESOLVED 2026-04-24, commit 2e047d8)

- **Opened:** 2026-04-23 · Wave 15.1 — **Resolved:** 2026-04-24
  (commit `2e047d8`).
- **Impact:** Low (operational-error visibility).
- **What was:** `New()` installed `StaticRecipientResolver{}` when
  `cfg.Resolver` was nil; production misconfigs only surfaced when
  an admin created a group/role campaign.
- **Fix:** `New()` now returns `(*Service, error)`; nil Resolver is a
  fail-fast boot error. `cmd/server/main.go` reads
  `VAULTDMS_ACK_RESOLVER` and picks `static` (dev) or `auth`
  (reserved for Wave 16 — currently returns an explicit "not
  implemented" error). Unset → fatal.

### T-D-7 — PAdES validator uses regex over raw bytes

- **Opened:** 2026-04-23 · Wave 15.4
- **Impact:** Low (correctness, low surface)
- **Where:** `services/signature/internal/pades/validator.go`
- **What:** Regexes are sufficient for CI smoke but can match
  inside string objects or miss encrypted xref streams. Explicitly
  scoped as "structural, not cryptographic" in the package
  docstring.
- **Mitigation (shipped):** 2026-04-24 (commit `9755ba0`) —
  validator.go now carries a prominent "SMOKE CHECK ONLY" header;
  `make test-pades-strict` (`scripts/check-pades-prod-accept.sh`)
  fails the build if any source file tagged `//go:build prod_accept`
  imports the package. That is the containment perimeter until
  T-D-7b lands.
- **Owner queue:** Signature service owner.
- **Exit criterion:** T-D-7b below.

### T-D-7b — PAdES full-parser replacement (pdfcpu)

- **Opened:** 2026-04-24 · follow-up to T-D-7.
- **Impact:** Low today (prod_accept gate keeps the regex out of
  release qualification); Medium once the acceptance gate relies
  on anything other than Adobe Reader + EU DSS.
- **Where:** `services/signature/internal/pades/validator.go`.
- **What:** Replace the regex-over-bytes implementation with a real
  PDF parse pass. pdfcpu is the preferred dep — pure Go, actively
  maintained, already reads the xref + object streams we care
  about. Alternative: delegate the structural checks to the EU DSS
  sidecar and keep only a 10-line caller here.
- **Owner queue:** Signature service owner.
- **Scope:** Phase 8. Not earlier — the current perimeter
  (`test-pades-strict` + "SMOKE CHECK ONLY" header) is sufficient
  until we need structural checks inside a release-qualification
  gate.
- **Exit criterion:** validator.go uses pdfcpu (or the sidecar) for
  `/Type /Sig` + `/ByteRange` + `/DSS` + `DocTimeStamp` detection;
  the SMOKE CHECK header is removed; `test-pades-strict` is kept
  as a defence-in-depth gate but becomes a no-op.

### T-D-8 — Schedule bootstrap string-matches Temporal error text (RESOLVED 2026-04-24, commit fcf8a4b)

- **Opened:** 2026-04-23 · Wave 15.1 — **Resolved:** 2026-04-24
  (commit `fcf8a4b`).
- **Impact:** Low (robustness).
- **What was:** `scheduleErrIsBenign` fell back to
  `strings.Contains(err.Error(), "already exists")` after its typed
  `errors.As` check — brittle across SDK versions and over-broad on
  unrelated errors.
- **Fix:** typed-only check against `*serviceerror.AlreadyExists`
  (the SDK v1.26.1 pinned in `services/workflow/go.mod` exposes it).
  New `wave15_schedule_err_test.go` pins the contract with wrapped
  typed errors (benign) vs plain "already exists" text + NotFound +
  Internal (not benign).

### T-D-9 — `dms.notify.*` namespace outside the standard subject shape (RESOLVED 2026-04-24, commit 5370ef9)

- **Opened:** 2026-04-23 · Wave 15.1 — **Resolved:** 2026-04-24
  (commit `5370ef9`).
- **Impact:** Low (hygiene, discoverability).
- **What was:** Notification subjects landed under
  `dms.notify.<source>.<event>.v1` rather than the standard
  `dms.<aggregate>.<event>.v1`; the carve-out wasn't documented
  anywhere.
- **Fix:** ADR 0032 (`docs/adr/0032-event-subject-namespacing.md`)
  codifies both shapes and the ownership rule. New
  `scripts/lint-nats-subjects.sh` walks `services/` + `pkg/`,
  extracts every `dms.*` string literal, and fails CI if a complete
  publish subject matches neither shape. Wired into the existing
  `security-go` job in `.github/workflows/ci.yml`.

## Closed entries

### T-D-10 — User identity was missing from cross-service gRPC context (opened + closed 2026-04-24)

- **Impact:** HIGH — silent 403s on every OPA-backed permission check, even for the seeded `owner` user.
- **Where:** every service's `cmd/server/main.go` gRPC interceptor chain, plus the document service's policy client wiring.
- **What was broken:** the gRPC server chain only had `TenantInterceptor`, which plumbed `x-tenant-id` onto the ctx but ignored `x-user-id` and `x-user-role`. Inside handlers, `auth.GetUserID(ctx)` returned `uuid.Nil` and `auth.GetUserRole(ctx)` returned empty string. OPA rules 5 (workspace-member admin) and 6 (org owner/admin) thus never fired → everything 403'd.
- **Fix:** added `middleware.UserIdentityInterceptor` in `pkg/middleware/tenant.go` that reads `x-user-id` + `x-user-role` from incoming gRPC metadata and populates `auth.WithUser`. Chained after `TenantInterceptor` in all ten services (audit, billing, connector, document, notification, policy, search, signature, storage, workflow). Also added `withCallerMetadata` on the document service's policy client so role/tenant/user flow on outgoing calls too.
- **Follow-up:** future cross-service gRPC clients should centralise the outgoing-metadata injection instead of hand-rolling `metadata.AppendToOutgoingContext`. Only `services/document`'s policy client uses the pattern today.
