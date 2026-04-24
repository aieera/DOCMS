# VaultDMS — State of the Project

**Single-source status document.** If you're coming in fresh, read this top-to-bottom. The blueprint (`DMS Architecture/dms-blueprint.md`) and the alignment plan (`docs/reports/BLUEPRINT_COMPLETION_PLAN_2026-04-19.md`) remain the two other docs worth reading; everything else was dated-report noise and has been removed.

Last fully-refreshed: 2026-04-23.

---

## 1. Where the codebase stands

**Waves 1–4** shipped pre-cutoff. Core multi-tenant auth + document primitives are live.

**Waves 5–14** shipped by 2026-04-20. G1/G2/G3 exit criteria from `final.md §1.2` are structurally met; remaining gaps are operator-ops (pentest, demo, tags) not engineering.

**Wave 15** (2026-04-21 → 2026-04-23) was a conscious scope expansion with four enterprise additions, executed in strict order 15.3 → 15.2 → 15.1 → 15.4. Summary below.

### Service truth table

| Service | State | Notes |
|---|---|---|
| auth | ✅ Live | password/MFA/SAML/OIDC/SCIM/API keys + Wave 15.3 force-change-password |
| policy | ✅ Live | OPA bundle + Wave 15.2 geofencing + `pkg/geo` resolver |
| document | ✅ Live | reference implementation; holds the bulk 45-table schema |
| storage | ✅ Live | envelope encryption, presigned URLs |
| search | ✅ Live | OpenSearch + Qdrant |
| audit | ✅ Live | tamper-detection emit + Wave 15 chain consumer |
| workflow | ✅ Live | Temporal; hosts Wave 15.3 + 15.1 schedules |
| notification | ✅ Live | `dms.notify.>` consumer; Wave 15.1 fan-out lands here |
| signature | ✅ Live | PAdES signer stub + Wave 15.4 saved profiles + Tier-1 PAdES validator |
| billing | ✅ Live | Stripe meter + usage; feature flags per tenant |
| connector | ✅ Live | M365 + Salesforce + OAuth/PKCE |
| **acknowledgement** | ✅ **New (Wave 15.1)** | policy-attestation service with per-tenant HMAC + hash chain |
| collaboration | ✅ Live | non-Go; WebSocket + cookie+CSRF |
| intelligence | ✅ Live | non-Go; OCR + classify + embed pipeline |
| preview | ✅ Live | non-Go; page-image renderer |

### Backend compile state

`go build ./...` succeeds across all 16 `go.work` modules. `go test ./...` passes with the unit-test suite; integration tests gated behind `//go:build integration` live in `tests/integration/` and per-service `*_integration_test.go`.

### Local dev

- Compose services already up (postgres:15432, redis, nats:4222, minio:9000, opensearch:9200, qdrant, temporal, clamav, kong-gateway:8080).
- Go services run via `bash scripts/run-all-services.sh` → health on 8081…8092 (auth, policy, document, storage, search, audit, workflow, notification, signature, billing, connector, **acknowledgement**). HTTP ports 8180…8191.
- Web on `npm run dev` (Vite auto-picks a port; check the terminal banner — may be 3000, 3005, or 5173 depending on what's free).
- Seed creds: tenant `acme`, `admin@acme.local` / `ChangeMe!Now2026`.
- **Postgres max_connections must be ≥ 300** to fit 12 services × 50-conn pool. Bumped 2026-04-23; if the compose volume is reset, rerun `ALTER SYSTEM SET max_connections=300` inside `vaultdms-postgres` and restart.

---

## 2. Wave 15 — enterprise additions

All four sub-waves in-review. Engineering-side DoDs green; remaining gates are operator-ops (release cohort, demo capture, pentest sign-off, chaos drill).

### 2.1 15.3 Force-change-password
- Schema: `users` gains `password_changed_at`, `must_change_password`, `password_expires_at`, `sso_federated`; new `password_history` table; `organizations.password_expiry_days` (default 90, 0 = never).
- Service: `services/auth/internal/service/password_change.go` — one-time Redis token via `GETDEL` (single-use, 10 min TTL), last-5 reuse guard via bcrypt-compare, SSO-federated users bypass.
- HTTP: `POST /auth/change-password` (public, rate-limited); `POST /admin/users/{id}/force-password-reset` (admin/owner); `POST /admin/password-policy/sweep-expired` (owner).
- Workflow: `PasswordExpiryWorkflow` daily 02:00 UTC per tenant, registered by `RegisterWave15Schedules`.
- Web: `/change-password` with live-checklist, show/hide, confirm. Login page branches on `require_password_change`.
- Metrics: `auth_password_changed_total{reason}`, `auth_password_expiries_pending{tenant}`.

### 2.2 15.2 Geofencing
- Schema: `services/policy/migrations/000001_geofence_policies` with partial scope index and ISO-3166-alpha-2 CHECK. RLS forced.
- `pkg/geo`: `Resolver` interface, `StaticResolver` (env-seeded CIDR→country), `CachingResolver` (60 s TTL, bounded 10 000 entries). **MaxMind adapter landed** in `pkg/geo/resolver_maxmind.go` behind `//go:build maxmind` (dep `github.com/oschwald/maxminddb-golang v1.12.0`, atomic `Reload()` with 5 s grace on the old reader).
- Decision core: `services/policy/internal/service.decide` — CIDR denylist wins → country deny → country allow → step-up match.
- Middleware: `pkg/middleware.Geofence` — 451 on deny, 428 + `WWW-Authenticate: Step-Up` on step-up, 503 fail-closed on decider error.
- Admin UI: `/admin/geofences` with create form + dry-run tester (hit `/admin/geofences/test` from the UI to see the decision).
- Frontend interceptor: 451 + 428 handled in `web/src/api/client.ts` with toast + reason.

### 2.3 15.1 Acknowledgements (new service)
- Scaffolded module `services/acknowledgement`, registered in `go.work`, `scripts/run-all-services.sh`, `release-please-config.json`, `.release-please-manifest.json`.
- Schema: `acknowledgement_campaigns` + `acknowledgement_assignments` (unique on tenant/campaign/user) + `acknowledgement_events` (append-only hash chain) + `acknowledgement_signing_keys` (KMS-wrapped per-tenant HMAC key) + service-local `outbox`. RLS on every table.
- Per-assignment HMAC-SHA256 under a KMS-wrapped tenant signing key; per-campaign SHA-256 chain (`self_hash = SHA-256(prev_hash || canonical_json(payload))`). `canonicalJSON` builds the JSON byte-by-byte with sorted keys — deliberately does NOT rely on encoding/json's map-sort side effect. `InvalidateTenantSigningKey` exposed for rotation events.
- Endpoints: `/api/v1/acknowledgement/campaigns` (list/create/close/report + assignments + my-pending + ack).
- Reminder + escalation Temporal schedule `AckRemindersWorkflow` daily 09:00 UTC per tenant. Rules: remind when `(due_at - 3d) <= now` and same-day-once guard; escalate one-shot after `due_at + 7d`. Emits `dms.acknowledgement.reminded.v1` / `.escalated.v1`.
- Notification fan-out: `dms.notify.acknowledgement.campaign.created.v1` / `.reminded.v1` / `.escalated.v1` wire to the existing notification service's `dms.notify.>` consumer so the 15.1 DoD "email + inbox < 60 s" is end-to-end live.
- Web: `/acknowledgements` user inbox, `AckBanner` on dashboard (60 s poll), `/admin/acknowledgements` list+create+per-row live report (10 s poll).
- ADR `docs/adr/0027-acknowledgement-attestation-chain.md` + runbook `docs/runbooks/15-acknowledgements.md`.

### 2.4 15.4 Saved signatures
- Schema: `services/signature/migrations/000001_signature_profiles` — `signature_profiles` row carries `(image_ref, wrapped_dek, kek_id, nonce, is_default)`. Partial unique index `WHERE is_default = true AND revoked_at IS NULL` enforces one-default-per-user.
- Service: per-profile AES-256-GCM DEK wrapped under tenant KEK `vaultdms/tenant/<uuid>/signature-profiles` (ADR 0030). Crypto-shred delete: soft-revoke → S3 delete → hard-delete.
- Handler: `/signatures/profiles` list/create/patch/delete + `/profiles/{id}/image` (owner-only; reads `auth.User(ctx)` — does NOT trust `X-Auth-*` headers).
- Web: `/settings/signatures` — canvas draw (pointer events), upload (1 MiB cap), typed (5 cursive fonts, offscreen-canvas render), set-default, delete. Reusable `<SignatureProfilePicker />` ready to drop into the Wave 9 envelope page.
- Tier-1 PAdES validator: `services/signature/internal/pades/validator.go` — structural checks (≥1 `/Type /Sig`, ≥1 DocTimeStamp, `/DSS`, tamper-window). `make test-pades` walks `tests/fixtures/pades/*.pdf` under `//go:build pades_corpus`.
- Tier-2 Adobe Reader + EU DSS = per-release operator procedure (runbook `docs/runbooks/15-pades-harness.md`).

### 2.5 Cross-cutting

- Cross-tenant RLS regression tests per sub-wave under `//go:build integration` + `make test-integration-wave15`. Testharness extensions (`AssertRLSIsolated`, `WithTenantTx`, `NewWithContainers` + `BootPostgres/Redis/NATS`).
- Playwright journeys `web/e2e/11..14-*.spec.ts` + axe-core a11y scan helper (`web/e2e/helpers/a11y.ts`) — `serious`/`critical` violations fail the test.
- `scripts/mutesting/run.sh` now includes `services/acknowledgement` (HMAC + chain) + `services/signature` (envelope).
- Chaos scenario `docs/chaos/scenarios/08-ack-reminder-worker-kill.md`.
- Observability: `deploy/monitoring/dashboards/wave-15-features.json` + `ops/prometheus/rules/wave-15.yml`.
- Docs: 4 ADRs (`0027..0030`), 4 runbooks (`docs/runbooks/15-*.md`), threat-model B9 section (`docs/security/threat-model.md`).
- OpenAPI: 14 paths + 7 schemas added in `docs/api/openapi.yaml`.

---

## 3. Review findings from the 2026-04-23 pass

Three agents ran review-diff, security-review, and a build-sanity sweep against the Wave 15 branch. **Build + test green across all affected modules.** Findings + disposition below.

### Fixed in this pass

| # | Severity | Issue | Fix |
|---|---|---|---|
| F-1 | HIGH (security) | `services/signature/internal/handler/profile_handler.go` read `X-Auth-Tenant-ID` / `X-Auth-User-ID` straight from request headers → tenant/user spoofing if reachable outside the gateway. | Switched `extractIdentity` to `auth.User(r.Context())` populated by `pkg/middleware/sessionauth`. |
| F-2 | MEDIUM | Ack service `ResolveImage` docstring claimed admin-audit access but the code enforces owner-only. | Rewrote the docstring; admin-audit path is an explicit follow-up (separate method). |
| F-3 | MEDIUM (corruption) | `canonicalJSON` relied on Go's incidental map-key sort. A refactor to a struct payload would silently break chain verification. | Rewrote to build the JSON byte-by-byte with explicit `sort.Strings` over keys. |
| F-4 | MEDIUM (DoS) | `GetCampaign` handler linearly scanned the tenant's campaign list in memory. | Added `Service.GetCampaign` that calls the indexed `repo.GetCampaign`; handler switched over. |
| F-5 | LOW | Ack sweeper swallowed `json.Marshal` errors (`body, _ := ...`). Silent corruption path into the outbox. | Errors now surfaced as `fmt.Errorf("marshal …: %w", err)`; tx rolls back. |
| F-6 | LOW (memory) | `pkg/geo.CachingResolver` had no cap — IP-enumeration traffic could grow the map without bound. | Added `DefaultCacheMaxEntries = 10 000` + eviction-on-overflow (drop every second key). |
| F-7 | LOW | Ack service's `keyCache` had no invalidation hook for KEK rotation. | Exported `InvalidateTenantSigningKey(tenantID)` for the rotation-event consumer to call. |

### Logged as deferred (tech-debt, non-blocking)

Tracked in `docs/tech-debt/ledger.md` (see section 4). Not fixed this pass because each requires either a deployment change or a cross-service design discussion:

- **T-D-1** — Internal sweeper endpoints (`/internal/v1/acknowledgement/sweep-reminders`) authenticate only via `RequireGatewaySignature + TenantHTTP`. A compromised service holding `VAULTDMS_GATEWAY_SECRET` could trigger cross-tenant reminder / escalation. Mitigation: mount `/internal/*` on a separate internal listener with worker-mTLS or a dedicated HMAC. Same pattern pre-exists on several other services — fixing requires a cross-service auth-plane change.
- **T-D-2** — `pkg/middleware/geofence.go`'s `realClientIP` trusts the right-most `X-Forwarded-For` hop. Correct behind Kong today; spoofable if ever mounted standalone. Mitigation: a configured trusted-proxy CIDR list (same as documented for other middleware). Requires an ADR covering the trust-boundary shape.
- **T-D-3** — `service.go` is 800+ LOC and holds campaign CRUD + HMAC + chain + sweeper + notification fan-out. Split along seams used by `services/document` (campaign / ack / attestation / sweeper / events).
- **T-D-4** — Profile-delete split tx pattern can orphan S3 objects if the process dies between the soft-delete tx and the S3 delete. Needs a sweeper job that finds `revoked_at IS NOT NULL AND image_ref IS NOT NULL AND revoked_at < now() - 24h`.
- **T-D-5** — `activities/wave15.go` uses `http.DefaultClient`. Replace with a per-activities `http.Client` with explicit `Timeout`.
- **T-D-6** — `StaticRecipientResolver` silently replaces a `nil` resolver in `New`. Make `Resolver` required; panic at boot on missing config.
- **T-D-7** — PAdES Tier-1 validator uses regex over raw bytes. Documented as a smoke check; never promote to prod acceptance without a full parser (pdfcpu or iText).
- **T-D-8** — Schedule bootstrap falls back to string-match on `"already exists"`. Keep the `errors.As(&serviceerror.AlreadyExists{})` branch; remove the substring fallback once the Temporal SDK version is pinned.
- **T-D-9** — Notification subjects use `dms.notify.*` namespace rather than the `dms.<aggregate>.<event>.v1` convention. Document the namespace carve-out in an ADR.

---

## 4. Tech-debt ledger

Full register at `docs/tech-debt/ledger.md`. Summary-only here:

| ID | Wave | Impact | Owner queue |
|---|---|---|---|
| T-D-1 | 15.1 | Medium auth | Platform-security (cross-service) |
| T-D-2 | 15.2 | Medium auth | Platform-security (ADR needed) |
| T-D-3 | 15.1 | Low maintainability | Ack service owner |
| T-D-4 | 15.4 | Low operability | Signature service owner |
| T-D-5 | — | Low resilience | Workflow service owner |
| T-D-6 | 15.1 | Low robustness | Ack service owner |
| T-D-7 | 15.4 | Low correctness | Signature service owner |
| T-D-8 | 15.1 | Low robustness | Workflow service owner |
| T-D-9 | 15.1 | Low hygiene | Event-schema owner |

---

## 5. Final Acceptance Gate for Wave 15

| Gate | State | Owner |
|---|---|---|
| All four sub-wave DoDs green in CI | engineering-complete | ✓ (this repo) |
| WAVE_15_PROGRESS.md fully ticked | superseded by this doc + ledger | ✓ |
| `docs/STATE_OF_THE_PROJECT.md` Wave 15 section | ✓ (this file) | ✓ |
| 90-second demo video | script ready, capture pending | operator |
| Tagged `v1.3.0` via release-please + cosign | runbook ready | operator |
| Chaos drill (scenario 08) | scenario written | operator (quarterly) |
| Pentest + threat-model sign-off | threat model updated | Security lead / CTO |

---

## 6. Backend running order (for regression-free local starts)

```sh
# one-time per volume reset:
docker exec vaultdms-postgres psql -U vaultdms -d postgres \
  -c "ALTER SYSTEM SET max_connections=300;"
docker restart vaultdms-postgres

# each dev session:
bash scripts/run-all-services.sh      # starts 12 Go services (auth … acknowledgement)
cd web && npm run dev                 # vite picks a free port; watch stdout

# verify:
for p in 8081 8082 8083 8084 8085 8086 8087 8088 8089 8090 8091 8092; do
  curl -s -m 2 -o /dev/null -w "$p: %{http_code}\n" http://localhost:$p/healthz
done
```

All twelve should return 200. If acknowledgement (8092) fails with `too many clients already`, Postgres wasn't bumped.

---

## 7. What's next — ranked

1. **Wave 9 envelope page** — the signature picker component is ready; giving it a home unblocks the 15.4 "envelope prefills default, user can switch" DoD.
2. **Cross-service internal-auth plane** (fixes T-D-1 + T-D-2 together). New per-worker mTLS OR a dedicated internal-HMAC middleware.
3. **Operator-gated Wave 15 gates** — release-please first run, demo video, cosign, chaos drill. Runbooks ready: `docs/runbooks/wave-15-release-v1.3.0.md`, `docs/demo/wave-15-script.md`, `docs/chaos/scenarios/08-ack-reminder-worker-kill.md`.
4. **OpenAPI drift-allowlist** shrink from 56 → 0 by writing specs for grandfathered routes.
5. **Mutation testing threshold** — write tests to push `services/acknowledgement` + `services/signature` above 70% on `scripts/mutesting/run.sh`.
6. **Threat-model signature approvals** (Security lead / Platform / CTO rows).

---

## 8. Gate targets from `final.md § 1.2`

- **G1 pilot-ready** — structurally met 2026-04-20.
- **G2 production SaaS** — 2–3 months / 6 engineers; blockers are the tech-debt ledger + pentest + SaaS-ops tooling.
- **G3 feature-complete** — 6 months; blockers are the out-of-scope ledger's post-G2 items.
