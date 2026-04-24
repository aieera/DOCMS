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

### T-D-1 — `/internal/*` sweeper endpoints rely on shared gateway secret

- **Opened:** 2026-04-23 · Wave 15.1
- **Impact:** Medium (auth, lateral)
- **Where:** `services/acknowledgement/internal/handler/handler.go:39`
  + `services/workflow/internal/activities/wave15.go:53,104`
- **What:** The worker → ack-service handshake authenticates via
  `RequireGatewaySignature + TenantHTTP`. Any in-cluster service
  holding `VAULTDMS_GATEWAY_SECRET` can call this path with an
  arbitrary `X-Auth-Tenant-ID` and trigger reminder + escalation
  side-effects for that tenant (bumps `reminded_count`, stamps
  `escalated_at`, emits notifications and hash-chain events).
- **Not yet fixed because:** the same trust-model exists on
  several other services' internal endpoints. Solving for one
  without solving for all is a patch; we need an ADR + a shared
  `pkg/middleware/internal_auth.go` design pass.
- **Owner queue:** Platform-security (cross-service).
- **Exit criterion:** internal routes mount on a second listener
  with mTLS OR `X-Internal-Worker-Signature` HMAC middleware.

### T-D-2 — `realClientIP` trusts right-most XFF hop

- **Opened:** 2026-04-23 · Wave 15.2
- **Impact:** Medium (geofence bypass)
- **Where:** `pkg/middleware/geofence.go:176-190` (and
  `services/acknowledgement/internal/handler/handler.go:366-381`).
- **What:** Spoofable when the middleware is ever mounted without
  a normalising proxy in front. Today correct behind Kong, but
  there's no code invariant that prevents a service owner from
  adding `Geofence(...)` to an internal listener and taking the
  hit.
- **Not yet fixed because:** requires a trusted-proxy-CIDR config
  + an ADR on the trust-boundary shape (where the TLS terminator
  is, what normalises XFF, etc.). pkg/gateway/waf.go already has
  a `RealIP` middleware — consolidating on that is cleanest.
- **Owner queue:** Platform-security.
- **Exit criterion:** `Geofence` refuses to parse XFF unless
  `TrustedProxies` is configured, OR delegates to `RealIP`
  exclusively.

### T-D-3 — `services/acknowledgement/internal/service/service.go` is a god object

- **Opened:** 2026-04-23 · Wave 15.1
- **Impact:** Low (maintainability)
- **Where:** file is 800+ LOC.
- **What:** Campaign CRUD + Acknowledge + HMAC + chain + sweeper +
  notification fan-out all live in one file. Reference
  implementation `services/document` splits these seams.
- **Owner queue:** Ack service owner.
- **Exit criterion:** split into `campaign_service.go`,
  `acknowledge_service.go`, `attestation.go`, `sweeper.go`,
  `events.go`.

### T-D-4 — Profile-delete split-tx orphans S3 objects on crash

- **Opened:** 2026-04-23 · Wave 15.4
- **Impact:** Low (operability, storage leak)
- **Where:** `services/signature/internal/service/profile_service.go:219-249`
- **What:** Soft-delete in tx1 → S3 delete → hard-delete in tx2.
  A process kill between tx1 and the S3 delete leaves a row with
  `revoked_at IS NOT NULL` and the `image_ref` still present.
- **Owner queue:** Signature service owner.
- **Exit criterion:** sweeper job `SweepRevokedProfiles` that
  finds `revoked_at < now() - interval '24h' AND image_ref IS
  NOT NULL`, retries S3 delete + hard-delete.

### T-D-5 — Workflow activity HTTP calls share DefaultClient

- **Opened:** 2026-04-23 · generic
- **Impact:** Low (resilience)
- **Where:** `services/workflow/internal/activities/wave15.go:58,106`
- **What:** `http.DefaultClient` has no `Timeout`. Activities
  inherit only the caller's context budget. Under backend-service
  slowness we'd keep a goroutine + socket pinned.
- **Owner queue:** Workflow service owner.
- **Exit criterion:** per-activities `http.Client{Timeout: 30s,
  Transport: configured with connection caps}`.

### T-D-6 — `StaticRecipientResolver` silently replaces `nil`

- **Opened:** 2026-04-23 · Wave 15.1
- **Impact:** Low (operational-error visibility)
- **Where:** `services/acknowledgement/internal/service/service.go:122`
- **What:** `New()` replaces a nil `Resolver` with
  `StaticRecipientResolver{}`, so a production misconfig (missing
  auth-service resolver) yields "groups/roles silently rejected as
  validation errors" rather than boot-time failure.
- **Owner queue:** Ack service owner.
- **Exit criterion:** `New()` panics if `Resolver` is nil; the
  Static resolver becomes opt-in via a named constructor.

### T-D-7 — PAdES validator uses regex over raw bytes

- **Opened:** 2026-04-23 · Wave 15.4
- **Impact:** Low (correctness, low surface)
- **Where:** `services/signature/internal/pades/validator.go`
- **What:** Regexes are sufficient for CI smoke but can match
  inside string objects or miss encrypted xref streams. Explicitly
  scoped as "structural, not cryptographic" in the package
  docstring.
- **Mitigation (shipped):** 2026-04-24 — validator.go now carries a
  prominent "SMOKE CHECK ONLY" header; `make test-pades-strict`
  (`scripts/check-pades-prod-accept.sh`) fails the build if any
  source file tagged `//go:build prod_accept` imports the package.
  That is the containment perimeter until T-D-7b lands.
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

### T-D-8 — Schedule bootstrap string-matches Temporal error text

- **Opened:** 2026-04-23 · Wave 15.1
- **Impact:** Low (robustness)
- **Where:** `services/workflow/internal/workflows/wave15.go:191`
- **What:** `scheduleErrIsBenign` falls back to
  `strings.Contains(err.Error(), "already exists")`. Brittle
  across Temporal SDK versions.
- **Owner queue:** Workflow service owner.
- **Exit criterion:** remove the substring fallback once we pin
  a single Temporal SDK version and rely solely on
  `errors.As(&serviceerror.AlreadyExists{})`.

### T-D-9 — `dms.notify.*` namespace outside the standard subject shape

- **Opened:** 2026-04-23 · Wave 15.1
- **Impact:** Low (hygiene, discoverability)
- **Where:** `services/acknowledgement/internal/service/service.go:53-59`
- **What:** Notification subjects land under `dms.notify.<source>.<event>.v1`
  rather than `dms.<aggregate>.<event>.v1`. Works because the
  notification consumer subscribes with a `>` wildcard, but the
  naming convention isn't documented anywhere.
- **Owner queue:** Event-schema owner.
- **Exit criterion:** ADR documenting the `dms.notify.*` carve-out,
  or rename to fit the existing convention.

## Closed entries

### T-D-10 — User identity was missing from cross-service gRPC context (opened + closed 2026-04-24)

- **Impact:** HIGH — silent 403s on every OPA-backed permission check, even for the seeded `owner` user.
- **Where:** every service's `cmd/server/main.go` gRPC interceptor chain, plus the document service's policy client wiring.
- **What was broken:** the gRPC server chain only had `TenantInterceptor`, which plumbed `x-tenant-id` onto the ctx but ignored `x-user-id` and `x-user-role`. Inside handlers, `auth.GetUserID(ctx)` returned `uuid.Nil` and `auth.GetUserRole(ctx)` returned empty string. OPA rules 5 (workspace-member admin) and 6 (org owner/admin) thus never fired → everything 403'd.
- **Fix:** added `middleware.UserIdentityInterceptor` in `pkg/middleware/tenant.go` that reads `x-user-id` + `x-user-role` from incoming gRPC metadata and populates `auth.WithUser`. Chained after `TenantInterceptor` in all ten services (audit, billing, connector, document, notification, policy, search, signature, storage, workflow). Also added `withCallerMetadata` on the document service's policy client so role/tenant/user flow on outgoing calls too.
- **Follow-up:** future cross-service gRPC clients should centralise the outgoing-metadata injection instead of hand-rolling `metadata.AppendToOutgoingContext`. Only `services/document`'s policy client uses the pattern today.
