# SeDoc — State of the Project

**Baseline:** 2026-07-03 (full principal-engineer audit; supersedes the
2026-04-17 / 2026-06-28 baselines).
**Method:** code judged against the ADRs (`docs/adr/*`, ~70) and the locked
invariants (`pkg/archtest/phase_c_invariants_test.go`). NOTE: the previously
cited `DMS Architecture/dms-blueprint.md`, `DMS Architecture/final.md`, and
`sedoc-feature-implementation-prompts.md` **do not exist on disk** — the
whole `DMS Architecture/` directory is absent. Architectural claims below
cite ADRs + archtest, not the missing blueprint. No application code was
changed in this audit (read-only except this file).

## Headline

The platform is **far more feature-complete than the old "~60% shipped"
framing** — 13 of 18 major feature areas are DONE with real code, every Go
module builds, all five Phase-C invariants pass, and the ingestion keystone
(`dms.version.uploaded.v1`) works. It is **not production-ready**, and the
blocker is not features — it is a **systemic RLS tenant-context gap**: at
least six services query Postgres on the raw pool without establishing
`app.current_tenant`, which works in dev (bypass role) but **fails closed
under the enforced prod `NOBYPASSRLS` posture** (helm `postgres-cluster.yaml:48`,
ansible; `pkg/database/rls_posture.go` gate; dev override
`SEDOC_ALLOW_BYPASS_RLS=1`, `docker-compose.yml:27`).

## Service truth table

> Regenerated from scratch 2026-07-03. Status reflects prod-posture reality,
> not the dev bypass. "✅ Live" = works end-to-end in prod; "🟡 Partial" =
> real code with a prod-blocking gap or feature stub.

| Service | Status | Reality on disk |
|---|---|---|
| auth | 🟡 Partial (80%) | password/TOTP/email+SMS OTP/WebAuthn/SAML/OIDC/SCIM/API-keys/m365+google exchange all real; SAML+OIDC crypto-hardened. **HIGH:** pre-tenant reads (`GetByHash` apikey.go:150, session Postgres fallback session.go:105, email lookup m365_exchange.go:178) run on raw pool → in prod (NOBYPASSRLS) API-key auth 404s and session-cache-miss spuriously logs users out; **HIGH:** Redis session fast-path never re-checks `revoked_at`/status → revoke/deactivate lags up to 24h; **HIGH:** push-MFA (`mfa_methods.go:373/433`) has no consumer + rubber-stamps any ack = MFA bypass; **MED:** SAML SP key regenerates every boot (`LoadSPKeyMaterial` dead). |
| policy | ✅ Live (90%) | OPA embedded + Redis decision cache; hot-path authz for document/storage. Emits `dms.permission.*` via outbox (bound to POLICY_EVENTS). Minor: cache-invalidation + decision-log gaps (LOW/MED). |
| document | 🟡 Partial | THE reference service — CRUD/versioning/folders/workspaces/lifecycle/tasks(0068)/annotations/comments/ingest/review-queue/sync/templates(0118)/analytics(0119) all real & mostly RLS-correct (`WithTenantTx`). **HIGH:** WOPI `FileResolver` left nil (`main.go:954`) → Collabora/Office-over-WOPI dead; **HIGH:** OnlyOffice callback (`onlyoffice_handler.go:209`) never saves back (status 2/6 unhandled) → edits silently lost. |
| storage | 🟡 Partial (85%) | initiate/PUT/complete, ClamAV fail-closed, per-tenant KEK/DEK envelope encryption, outbox events real. **HIGH:** `CompleteUpload` (`service.go:389`) not actually idempotent — a retry re-runs size check on ciphertext and can corrupt a good upload; **HIGH:** `BlobReaper` uses `SET LOCAL row_security=off` (`content_blobs.go:104`) which **errors** under NOBYPASSRLS → hourly reaper dies every cycle. |
| search | 🟡 Partial (72%) | OpenSearch indexer (consumes doc/version/ocr/classify/entities), hybrid+Qdrant, saved searches + alerts(0085), field-collapse. **CRITICAL:** the saved-search repo (`repository.go:31`) is entirely raw-pool → saved searches/subscribers/smart-folders return 0 rows in prod (alerts silently never fire); **HIGH:** `onDocCreatedOrUpdated` (`indexer.go:108`) full-replaces the index doc from the SPARSE `document.updated` diff → any metadata edit wipes `readable_by`/content and the doc vanishes from search. |
| intelligence | ✅ Live (88%) | Python: OCR (Surya+Paddle)+quality, NER, embeddings→Qdrant (real), RAG end-to-end, translation, classification. **HIGH:** `_on_redaction_requested` (`nats_consumer.py:839`) marks redaction `applied` WITHOUT redacting the PDF → false compliance signal; **MED:** several Python outbox emits target unbound subjects (see event bus). |
| preview | 🟡 Partial (65%) | Real multi-format rasterization → S3; NATS consumer wired; watermark code EXISTS. **HIGH×3 (config/wiring):** `/previews/*` orphaned from the gateway (no Kong/routes entry) → unreachable by clients; S3 creds mis-keyed in helm (`SEDOC_S3_*` vs injected `SEDOC_MINIO_*`) → boto3 client nil; internal watermark endpoints 401 (no `SEDOC_SERVICE_API_KEY` provisioned). |
| workflow | 🟡 Partial (72%) | Real Temporal workflows: approval(+routing 0064), DSR(0024), residency, retention(+schedule), review, saved-search-alert(0085), scheduled-report(0119). **HIGH:** `CompleteTask` (`activities.go:59`) uses `UPDATE … ORDER BY … LIMIT` = Postgres **parse error on every task completion** → workflow tasks never leave 'pending'; **HIGH:** even fixed, it writes raw outcomes ('approved'/'sign'/…) that violate the status CHECK constraint. Signature-orchestration workflow is the one genuine stub. |
| notification | 🟡 Partial | in-app + Redis realtime + digest(0086) + push devices(0117 Expo, session work) real; flat prefs table (`000003`) my-session-added. **HIGH:** the ADR-0086 pref tables (`notification_preferences`/snoozes/dnd/digests, FORCE-RLS) are queried raw (`prefs.go:18`) → snooze/DND/matrix all no-op in prod; **HIGH:** user-id→email delivery is a stub (`service.go:135` logs "lookup pending") → email channel dead for the entire in-app flow (only DSR emails send). |
| audit | 🟡 Partial (80%) | Hash-chain log, GET events, CSV export, SIEM forwarding, `verify-integrity` endpoint EXISTS. **HIGH:** no role check in the handler (`handler.go`) despite `auth:admin` route class → any tenant member can read all audit + redact actor/IP; **HIGH:** repo raw-pool (`repository.go:156`) → in prod all audit reads empty + hash chain never links. (The RLS bug currently *masks* the RBAC bug in prod; both must be fixed together.) |
| signature | 🟡 Partial | Real PAdES B-LT via EU DSS sidecar + Go LTV validator(`pades/`); seal consumer(0025) hardened this session. **HIGH:** `SealVersion`/`SealCeremony` omit `RegionPin` (`seal.go:111/168`) → sealed blob forced to `us-east-1`, a region-pin/residency violation for non-us-east documents. **MED:** per-tenant KMS + prod TSA are config, not code. |
| collaboration | ✅ Live (78%) | Node/Yjs on :8083: CRDT sync + session/tenant auth + Postgres snapshots(0096). **Gap:** only `comment_create` handled (update/delete + persistence missing); **0 tests** (only Node service without any). |
| connector | 🟡 Partial (72%) | Webhook delivery (HMAC/backoff/DLQ), M365/Google/Salesforce OAuth with real token exchange + live API calls, email ingestion(0087). **CRITICAL:** repo + background cross-tenant scans raw-pool (`repository.go:214`, email/intake) → connector config/webhook reads empty in prod; **HIGH:** Google Drive download `io.LimitReader(…,100MiB)` silently truncates larger files and reports "imported". |
| billing | 🟡 Partial | Stripe webhook signature verify + usage metering code present. **CRITICAL:** entire repo raw-pool (`repository.go:106`) → every write to FORCE-RLS `subscriptions`/`usage_records` fails-closed in prod; **CRITICAL:** `stripe_customer_id`/`subscription_id` never persisted (`provisioner.go:114`, no `checkout.session.completed` handler) → all webhook handlers are no-ops even in dev; **HIGH:** webhook handlers discard every persistence error then ack 200 → Stripe never retries; **0 tests on the money path** (metering/webhook/service). |
| mcp-server | 🟡 Partial | ADR 0091 service exposing tools to LLM clients. **Gap:** the ONLY Go service with **zero tests** — tool dispatch/arg-validation/tenant-scoping unverified. |
| graphql-gateway | ✅ Live | Read-façade resolvers over service gRPC; degrades not-found/denied to null. |
| add-ins (Office+Google) | 🟡 Partial | Outlook/Word(0112/0113) + Google Workspace(0116) shipped this session (exchange endpoints JWKS-verified). Gaps: placeholder manifest GUIDs/icons; UI review-verified only; m365 email-ingest blob persistence deferred. |
| mobile (Expo) | 🟡 Partial | ADR 0117: SecureStore session + biometric, viewer, comments/annotations, tasks+approve, capture+offline, Expo push. Gaps: UI review-verified only (no host/CI typecheck); no `/previews/*` thumbnails. |
| workspace templates | ✅ Live | ADR 0118 (this session): JSONB tree provisioning, atomic tenant-tx, per-entity events; web gallery+editor. Gap: not idempotent-by-key. |
| analytics reports | ✅ Live | ADR 0119 (this session): governed query API (parameterized SQL, golden-tested, RLS-correct) + Temporal-scheduled delivery + builder UI. |
| accessibility (web) | ✅ Live | ADR 0120 (this session): axe AA gate (8 flows) + keyboard gate, in CI; contrast/label/keyboard fixes. Gap: dark-mode + doc-detail/admin routes unscanned. |

## Keystone & event bus — verification (section C)

- **`dms.version.uploaded.v1` PUBLISHES via the outbox** from the document
  service's `CreateVersion` (`documents.go:1102`, ADR 0021) — **not** from
  storage's `CompleteUpload` (which has no `version_id` in its tx). The old
  "fix it in storage / pipeline halts" framing is a stale premise; the
  pipeline is intact. `DOC_EVENTS` binds `dms.version.>`; consumers (search,
  intelligence, preview, audit) subscribe. ✅ PASS.
- **Streams cover the Go subjects** the prompt worried about: `dms.user.*`
  (USER_EVENTS), `dms.policy.*` (POLICY_EVENTS), `dms.billing.*`
  (BILLING_EVENTS) all bound (`publisher.go:81-84`). ✅ PASS.
- **CRITICAL — Python emits to UNBOUND subjects on the shared outbox.**
  `services/intelligence/app/tasks/{compliance_scan.py:632, ocr_quality.py:612}`
  write `dms.notification.send.v1`, `translate.py:438` writes
  `dms.translation.completed.v1`, `training_collector.py:281` writes
  `dms.training_example.collected.v1` into the SHARED `sedoc.outbox`. **No
  stream binds `dms.notification.>`, `dms.translation.>`, or
  `dms.training_example.>`.** The Go `OutboxPublisher.drainBatch` publishes
  each row's `event_type` as a NATS subject and **stops on first failure**;
  an unbound subject returns "no responders" and **wedges the entire shared
  outbox drain**, blocking all events behind it. `training_collector` is wired
  to the common `dms.classify.corrected.v1` correction path, so it is
  reachable. **This is the single most dangerous defect.**
- **CRITICAL — the coverage guardrail is blind to Python.**
  `pkg/events.PublishedSubjects` + `coverage_test.go` + the runtime
  `AssertLiveCoverage` harvest only Go emitters, so the three Python subjects
  above escape every guardrail (`coverage.go:80`).

## Locked invariants — Phase C (section D)

`go test ./pkg/archtest/` → **PASS** (all five). Verified per-invariant:

| # | Invariant | Pinned by | Result |
|---|---|---|---|
| C1 | (schema/route mirror) gateway↔kong | `gateway_routes_test.go` | ✅ PASS |
| C2 | No auth token in browser/insecure storage (web + mobile) | `TestPhaseC2_*` | ✅ PASS |
| C3 | `crypto/rand` in SAML signer | `TestPhaseC3_*` | ✅ PASS |
| C4 | Request-scoped ctx in handlers/consumers | `TestPhaseC4_*` | ✅ PASS |
| C5 | Outbox-only publishing (2 whitelisted JS→JS forwards) | `TestPhaseC5_*` | ✅ PASS |
| — | Per-tenant KEK (no shared default) | migration + `dms-admin kms rotate` | ✅ (storage envelope enc real) |

**But note:** C5 passes for *Go* code; the invariant does not cover the
Python shared-outbox emitters, which is how the unbound-subject CRITICAL
slipped through. The archtests are green; the gaps are in areas the tests
don't reach (Python event emission, prod RLS posture).

## Migration blocker — still OPEN (was flagged 2026-06-28)

`services/document/migrations/000021_ner_pipeline.up.sql:14` runs
`ALTER TABLE document_entities`, but the document schema creates `entities`
(`000001:819`), never `document_entities` — that table is created ONLY by
`services/intelligence/migrations/000002`. So `make migrate-up
SERVICE=document` and every document integration test fail on a clean DB
(`relation "document_entities" does not exist`); 000023/000024 depend on it
too. Masked only where a shared Postgres has intelligence migrate first.
**Blocks clean-stack CI and a clean deploy.** Fix: guard 000021 with
`CREATE TABLE IF NOT EXISTS document_entities (...)` or own the base table in
the document schema.

## Build / contract / test health (sections A, H)

- `go build` across all 18 `go.work` modules: **all pass** (2026-07-03).
- `buf generate`: Go stubs are byte-current with the `.proto`; the OpenAPI
  swagger has cosmetic formatting/ordering drift vs HEAD only (LOW).
- Unit-test density: document 47/163, search 14, auth 11, signature 10,
  workflow 8, notification 6, audit 5, policy/storage/connector 4, billing 3;
  intelligence(py) 27, preview(py) 8; web 30 e2e + 23 vitest.
  **Zero-test services: `collaboration` (Node), `mcp-server` (Go), and the
  billing money path** (metering/webhook/service).
- **Observability gap (HIGH):** `pkg/tracing` exists but **no service imports
  it** — zero distributed tracing across all 13 Go services. Structured
  logging (zerolog) + prometheus metrics are broadly present.

## Security debt — Phase-C items still locked; NEW systemic gap opened

The 2026-04 Phase-C security items remain pinned by archtests (per-tenant
KEK, httpOnly cookie, crypto/rand, request-scoped ctx, outbox-only). The
**new** systemic issue this audit surfaces is the RLS tenant-context gap
above (auth pre-tenant reads, search, connector, billing, notification,
audit, storage reaper) — masked by `SEDOC_ALLOW_BYPASS_RLS=1` in dev,
fail-closed in prod. Plus: audit RBAC missing, push-MFA bypass, session
revocation lag.

## Gate verdict

- **G1 pilot-ready:** **Conditionally met — DEV posture only.** Under the
  dev bypass role features work; under the prod `NOBYPASSRLS` posture that a
  real pilot would run, billing/notification-prefs/audit/search-alerts and
  auth fallbacks break. Also blocked for any clean stack by migration 000021.
- **G2 production:** **NOT met.** Blockers: (1) RLS tenant-context across ≥6
  services, (2) shared-outbox jam (Python unbound subjects), (3) migration
  000021, (4) workflow `CompleteTask` syntax + CHECK, (5) OnlyOffice/WOPI
  save-back dead, (6) audit RBAC, (7) storage reaper dies under NOBYPASSRLS,
  (8) session-revocation lag + push-MFA bypass, (9) Stripe subscription state
  never persists, (10) zero distributed tracing.
- **G3 feature-complete:** breadth ~90% (only the signature-orchestration
  workflow is a genuine feature stub); "complete" still gated on the G2 list.

## Completion map (revised)

- **Feature breadth:** ~90% (13/18 areas DONE, 5 PARTIAL, 0 not-started).
- **Production readiness:** ~55% — the delta is the RLS-context gap +
  the ~26 HIGH correctness/security defects, not missing features.

## Running on this machine

Compose stack up (document/auth/storage/search/signature healthy).
`SEDOC_ALLOW_BYPASS_RLS=1` in compose — **so local runs do NOT exercise the
prod RLS posture; the RLS bugs are invisible here.** Playwright browsers need
system libs extracted to `LD_LIBRARY_PATH` (no root); web a11y/keyboard gates
(e2e/70–71) pass.
