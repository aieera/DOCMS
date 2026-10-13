# 2026-10-12 codebase review — remediation hand-back

Branch `fix/review-2026-10` (base `neu-ui` @ e9e74eec). Companion to
`2026-10-12-codebase-review.md` and `-addendum.md`, whose
`Status (2026-10-12 remediation)` column maps all 180 finding IDs to the fixing
commits (aliases: C-9→CB-9, C-10→CB-10, C-11→CB-11, C-12→CB-12, L-13→R-6/R-7,
S-10→S-3, S-14→S-13, M-33→C-6, L-9→R-8, M-34→R-5/S-5/H-22).

## What landed

21 workstreams (PKG, DB, AUTH, DOC1–3, SIG1–2, STORAGE, PLAT, CONN, WF, PY,
COLLAB, MISC, XSVC, WEB, CI, DEPLOY, OPS, DEPS) plus post-review rounds
(doc1-lifecycle, doc3-salt, doc3-ediscovery, PKG2, LINTFIX), each implemented in
its own worktree, reviewed by an independent reviewer with fix rounds (cap 5;
regression rounds beyond), merged `--no-ff` behind per-merge gates (build / vet /
race, tagged vet, archtest, pkg/database RLS tests, prod-posture lane). Three
whole-branch reviews (security, integrity, deployability) ran after the merges;
every Important finding was fixed before hand-off.

Design decisions DEC-1..DEC-19 are in the plan; the rulings below are the
judgement calls made during execution (each with the cost if wrong).

## Verification on the final tip

| Gate | Result |
|---|---|
| `make build` | ok |
| `make test` (race) | 96/96 packages ok, 0 FAIL, 0 data races |
| `go test ./pkg/archtest/` | ok |
| `go vet -tags "integration prodposture"` every module | ok |
| lint ratchet `--new-from-merge-base=neu-ui` | 0 findings (was 352–362; full `make lint` backlog of ~2,248 pre-existing findings untouched) |
| `scripts/prod-posture-lane.sh` (NOBYPASSRLS) | GREEN, 0 allow-listed failures |
| `make security-check` | gosec 0 HIGH (all taint sinks annotated with reasons), govulncheck: no vulnerabilities in any module |
| web `tsc --noEmit` / vitest / lint / build / `npm audit` | ok / 863 passed / ok / ok / 0 vulns |
| collaboration (Node) | 94/94 |
| intelligence / preview (Python) | 416 pass (2 known) / 43 pass (2+1 known) |
| compose config dev / prod | dev ok; prod fails closed without the required secrets (intended) |
| Helm lint + `deploy/helm/sedoc/tests/render-checks.sh` | 0 failed / all pass |
| `scripts/check-migrations.sh` / `check-metrics` | OK (19 new migrations) |

The full suite, posture lane, security check and web gates ran on c85fa724; the
final commit on top (d9aa79b7) changes one test-harness request body
(`nil` → `http.NoBody`), two `#nosec` comments and gofmt whitespace, and was
re-verified with build, the affected modules' race tests, archtest and the lint
ratchet.

## Operator / owner action items (outside the branch)

1. Branch protection: require exactly "Core Gate", "Security Gate", "Quality Gate" (old per-job required checks are superseded; names in .github/workflows/ci.yml header).
2. After merging to neu-ui: `cd web && npm ci` in the main checkout (package-lock changed; the systemd Vite server runs from it); existing host-mode `.env` files need `SEDOC_ENVIRONMENT=dev` (blank is no longer dev).
3. Dev stack restart picks up: SEDOC_SEAL_LEVEL=B-B + SIGNER_ALLOW_DEV_KEY=1, SEDOC_ALLOWED_ORIGINS, SEDOC_REGION, SEDOC_STORAGE_INTERNAL_URL=storage:8081 (compose already carries them).
4. Every real deployment (docs/runbooks/upgrade.md): migrations before binaries (000107 → storage; 000104 → outbox publisher; 000114 backfill); delete the JetStream durables once (M-10) and preview-svc; run the audit re-baseline once with --cutover (docs/runbooks/audit-chain-rebaseline.md) — GDPR erasure returns 409 until then; set the new required env/secrets (trusted proxies, allowed origins, seal level/TSA, KEK, DSR salt, e-discovery key, license, S3 public base).
5. Lint backlog: 2,248 pre-existing findings outside the ratchet (document 811, auth 236, pkg 210, …) need an owner.
6. Product gaps recorded, not built: password-reset email consumer; extension OAuth endpoints; Temporal worker deployed nowhere; metadata edits under legal hold allowed by the model allow-list; multi-region same-content storage.
7. Follow-ups queued (minor, in ledger): auth should stop returning session_token in browser login JSON; MFARecovery response; gRPC interceptors trust raw metadata (in-cluster only); Origin allow-list for /yjs behind the proxy; pkg/metrics never wired into HTTP chains; Celery /metrics; GOTOOLCHAIN pin is in the Dockerfiles now.

## Rulings made during execution

- S-5 cap for QES start / e-sign send lowered to 1 MiB (DEC-17 removed bytes; webhook keeps 64 MiB) — spec's S-5 asks for a body limit, the 64 MiB figure assumed embedded PDFs — cost if wrong: current web client gets 413 on QES start until WEB lands its contract change.
- DB's posture test applies all service chains with an explicit pending-offender list (push_devices, notification_user_prefs→PLAT; stripe_customer_map→MISC) that fails on unknown or stale entries; no allow-list in the production gate; coordinator empties the list as PLAT/MISC merge — cost if wrong: integration branch cannot boot under dms_app until PLAT and MISC merge.
- vendor e-sign ingest gates on request status only; SeDoc expires_at applies to in-app signing only — vendor owns envelope expiry — cost if wrong: an envelope completed long after SeDoc's 14-day window is still ingested.
- no password-reset email consumer exists anywhere (forgot-password UI is a stub); PLAT must not build one — N-1 notification half recorded as a product gap — cost if wrong: password reset stays unavailable, as it is today.
- AUTH implementer merges the integration tip into fix/rv-auth and swaps clientIP to middleware.ClientIP before the coordinator merges — keeps conflict resolution with the implementer.
- DOC2 also owns the DEC-12 review-queue fix in ingestion.go and the bulk import + BulkUpdateMetadata blob-claim/lock fixes (files DOC1 didn't own) — same defect class, one owner — cost if wrong: DOC2 scope grows.
- DEC-7 gaps flagged minor by reviewer (UpdateDocument lacks assertNotHeld; CreateVersion hold TOCTOU) go into fix round 1 — DEC-7 explicitly says every mutating path — cost if wrong: slightly larger fix round.
- storage keeps encryption ON when SEDOC_LOCAL_KEK is set even if SEDOC_STORAGE_ENCRYPT_AT_REST=false (boot warning) — DEC-10's "dev runs unencrypted" premise was false (live dev already encrypts) and fail-safe direction wins — cost if wrong: an operator who wants plaintext must also unset the KEK.
- storage InitiateUpload rejects any region other than the deployment's SEDOC_REGION with REGION_VIOLATION; CheckBucket at complete stays as defence in depth — dev compose and Helm provision only dms-<SEDOC_REGION>-* buckets so other regions never worked — cost if wrong: a future single-deployment multi-region setup needs a served-regions list.
- STORAGE fix round 1 also takes two minors — late-dedup duplicate delete deferred until after commit, and a scanning-grace on expiry — both are data-loss-adjacent and cheap.
- PLAT fix round 1 also takes three minors — dead unlocked audit append paths, O-5 NAK-on-final-delivery loss, and the pre-existing red notification fixture (touched package must be green).
- N-11 scope is the NATS credentials files only (finding row: "Event-stream NATS credentials are unusable by construction and unrevocable"); the HTTP polling bearer token stays, 501 only for nats_creds — cost if wrong: none today (creds were unusable anyway).
- DOC3 fix round 1 also takes four minors (D-12 user status/current role recheck, zt telemetry revocation gate, 1-year expiry cap, de-fragile the 000113 test) and applies DEC-19 MaxBody to DOC3-owned mounts only; the rest of document DEC-19 stays with DOC2.
- under a legal hold, description/tag edits are refused along with everything else — DEC-7 says every mutating path; metadata-under-hold is a product question for later — cost if wrong: users cannot retag held documents.
- CONN fix round 1 also takes four minors (overlap check on resolved paths + the allocated host_path unique index, IMAP Collect() failure recorded, parse cap uses b.maxBytes(), non-regular claimed file terminal rename).
- C-7 fast path reorders to lock → DeleteBlobRow → S3 delete → commit, per-blob WithoutCancel context with fixed op timeout, budget checked between blobs only, WORM bucket skipped like the reaper — residual (commit failure after a successful S3 delete) equals the reaper's existing residual — cost if wrong: a leaked object, never a lost one.
- audit chains written before the canonical-hash fix are recovered by an explicit operator re-baseline (services/audit/cmd/rebaseline, owner/BYPASSRLS role only, dry-run default, appends audit.chain_rebaselined marker + signed checkpoint) and the anonymize handler returns 409 audit_chain_unverifiable; the hash check is NOT weakened for old events — old hashes were never verifiable, so re-baselining loses no assurance — cost if wrong: every existing deployment needs a one-time operator step before GDPR erasure works (→OPS/DEPLOY runbook).
- subagent-authored commits keep the trailer of the model that wrote them (Opus 5.5 implementers); only coordinator-authored merges/commits carry Fable 5.1 — attribution should be truthful — cost if wrong: mixed trailers on the branch.
- rebaseline --apply hard-refuses without the signing key (exit 4) before touching any tenant; dry-run may run without it — an uncertified re-baselined chain defeats H-12 — cost if wrong: operators must have the signing key at hand for the cutover.
- connector 000002's dms_app GRANT stays guarded on role existence (roles are cluster-wide; an unconditional GRANT re-couples the chains) — cost if wrong: on a cluster where dms_app is created after the connector chain, the grant must be applied by the deploy tooling.
- SEDOC_SEAL_LEVEL (B-B|B-T|B-LT, default B-LT) validated at startup; B-T/B-LT without SIGNER_TSA_URL fails boot, not per seal; dev compose sets B-B until a TSA exists (→DEPLOY/OPS) — cost if wrong: dev seals are B-B (no timestamp) by explicit config.
- SIG2 may extend into the vendor-ingest gate for Adobe only — completion must be confirmed via the provider's status endpoint before ingest (public client id is not authentication) — cost if wrong: one extra Adobe API call per completion.
- rebaseline --apply requires --cutover and refuses (exit 5) if any differing event is at/after it; dry-run lists the differing range — operator judgement needs evidence, not counts — cost if wrong: operators must look up the deploy time of 07f13d71 before the cutover.
- a defect introduced by a fix round gets a regression-fix round beyond the 5-round budget (round 6) rather than final triage — leaving a known signing hole on the branch is worse than one more round — cost if wrong: PLAT takes longer to merge.
- COLLAB fix round 1 also takes four minors (/ws subscription re-check, room-id canonicalization, finalizeRoom failure accounting, comment_delete belongsTo).
- the post-rebaseline checkpoint is signed inside the rewrite transaction and the repair exception is deleted; a tenant with no checkpoint is signed only with --allow-first-checkpoint (else exit 5, no_checkpoint=true in output) — only signed material may authorise a signature — cost if wrong: an operator recovering a tenant whose checkpoints were deleted must consciously opt in.
- the coordinator adds `SEDOC_SEAL_LEVEL: B-B` (signature) and `SIGNER_ALLOW_DEV_KEY: "1"` (signer) to docker-compose.yml in the SIG2 merge commit — a merge must not break the dev stack's next restart; DEPLOY still owns Helm — cost if wrong: dev seals are B-B by explicit config.
- a new /DSS may reference prior objects only if the earlier (signed) catalog's /DSS already listed them as Certs/OCSPs/CRLs — DSS 6 reuses those for B-LTA extension; the exception grants nothing new — cost if wrong: none identified (the earlier DSS is covered by the earlier signature).
- no stream may ever be redefined without a byte-identical body and equal dict (except /Length); signed /Metadata may not be redefined (XMP updates are new objects); the DSS-dict arm requires the prior object to be a pure DSS/VRI-shaped dict and allows only additive references to new objects — nothing derived from a catalog the attacker could have authored may waive the byte comparison — cost if wrong: an LTA tool that rewrites XMP in place is reported not tamper-evident (acceptable; DSS/PDFBox write new objects).
- intelligence /internal/v1/* must require the internal key (DEC-4 — an unauthenticated internal surface is the finding); the search and connector callers get a small follow-up to send X-Internal-Service-Key before PY merges — cost if wrong: intelligence internal calls 401 until the callers land.
- PY fix round 1 also takes five minors (ready manifest preserved on failed regenerate, Go-produced envelope fixture, NAT64/6to4 unwrap, intelligence MAX_IMAGE_PIXELS, and the pre-existing preview NATS consumer drop).
- a redefined stream is allowed only if its entire raw object span (dict+stream+body+endstream) is byte-identical — no /Length or key exceptions; for signed-range streams an indirect or non-landing /Length is ErrParse, never a scan fallback — the tamper decision must never depend on how a reader resolves a length — cost if wrong: a tool that re-emits an unchanged stream with reformatted whitespace inside the dict is reported not tamper-evident (acceptable; nothing legitimate re-emits signed streams).
- a legal hold freezes content, versions, lifecycle and deletion but permits title and custom_metadata edits (the existing IsLegalHoldBlocked allow-list) — DEC-7's "every mutating path calls assertNotHeld" is satisfied; which ops the hold blocks is the model's allow-list, a product choice — cost if wrong: a held document's title/metadata can still change (supersedes the earlier "description/tag edits refused" wording, which was accurate but incomplete).
- the symlink gap is fixed before merge (short round) together with billing requireAPIKey fail-closed + constant-time (allow-all when unconfigured is an arbitrary-org write path given 000111) — cost if wrong: one more round for MISC.
- the audit DLQ park is whitelisted by moving it into a DB-free package (services/audit/internal/dlq) and listing that path in archtest C5 with the SIEM-park rationale — whitelisting internal/service would also cover DB-writing code — cost if wrong: a small refactor of merged code.
- release.yml is fixed by CI using build/Dockerfile.go-service for all Go images, web/Dockerfile (DEPLOY adds it), one ghcr.io/aieera/sedoc namespace, image list identical to the Trivy matrix — H-21 is joint; CI owns the workflow, DEPLOY the Dockerfiles — cost if wrong: release.yml stays red until DEPLOY merges.
- new CI jobs are routed through a quality-gate with if: always() + result checks; the required-check names branch protection must list are documented in the workflow header (GitHub settings are outside the repo → final report action item).
- /ws authenticates from the dms_session cookie on the gateway-signed upgrade (like /yjs); the browser sends a cookie-mode auth frame and never retains session_token; the Bearer frame stays for non-browser clients — a session secret must not be readable by page JS — cost if wrong: one round each for COLLAB and WEB. Follow-up (final triage): AUTH should stop returning session_token in the browser login JSON body.
- /yjs 4403 → stop with access_revoked (no re-auth loop) is the intended behaviour; the COLLAB addendum's re-auth+reconnect applied to /ws only.
- document migration 000114 allocated to DOC2 for a batched data backfill of `_sedoc.source="m365"` on legacy rows carrying only `email.source` — the marker change would otherwise reap storage-less legacy ingests after 24 h — cost if wrong: one more migration in the chain.
- cookie-bearing /ws and /yjs upgrades require an exact-match Origin from SEDOC_ALLOWED_ORIGINS (required env; 403 at upgrade), Bearer clients unaffected — SameSite=Lax is not a WebSocket guarantee in every browser and cookie mode is now the primary browser path — cost if wrong: every deployment must list its web origin(s). Task COLLAB: round 4 (tiny) dispatched to a0118565 (FIX_BASE 30570fc1); DEPLOY told.
- the O-6 role change and the test-fixture LOGIN grant land in the same DEPLOY branch (explicit permission on pkg/testutil, pkg/database tests, task tests) — a merge must not leave the posture lane red — cost if wrong: DEPLOY edits outside its row.
- migrations run as the database superuser (Ansible: local socket; Helm migrate Job: the migration secret must be superuser-capable) because the chains create/alter BYPASSRLS roles, which only a superuser can grant; the runtime role remains dms_app NOBYPASSRLS — refines DEC-1's "owner role" wording — cost if wrong: the migrate credential is a superuser (already the case in every deployment today).
- ec2 sets SEDOC_ENVIRONMENT=dev explicitly because those are documented dev/HTTP test boxes; its README must say so — C-2's point was "never valid/armed", not "always prod" — cost if wrong: an ec2 box used as prod runs dev defaults (documented).
- L-9 is fixed by deleting the dead `rotate-secrets` subcommand from cmd/dms-admin (nothing reads jwt_signing_key / session_enc_key / kek:new / system_secrets; it prints 64 bits of keys and the full API secret on a DB-error path) — assigned to DEPS as a one-commit addendum since it already owns cmd/* module changes — cost if wrong: an operator loses a command that never did anything.
- the dev license key is trusted only when SEDOC_ENVIRONMENT == "dev" explicitly; unset means no trust root (non-fatal without a license, fatal with REQUIRE_LICENSE or a license present) — "unset" must never silently mean "dev" for a trust decision — cost if wrong: a box that relies on the implicit dev default must now set SEDOC_ENVIRONMENT=dev (DEPLOY sets it everywhere per C-2).
- DEPLOY fixes the two posture fixtures in the same branch (explicit permission on those two test files) — cost if wrong: DEPLOY edits outside its row again.
- Helm trusted-proxy values become required with no default — "trust all private networks" is not a proxy list — cost if wrong: every Helm install must set the ingress/Kong CIDR before it renders.
- confirmed whole-branch findings are fixed before hand-off (plan §6) — post-merge rounds dispatched: CI core-gate (a2da4c25), DEPLOY round 3 (a36cedd6: license+extraEnv, salt secret, in-cluster URLs/keys, NOTES+upgrade runbook, chart-file symlinks), DOC3 salt fail-closed on new branch fix/rv-doc3-salt (ae0c03f6).
- whole-branch Important findings are fixed before hand-off (plan §6): DOC1-lifecycle, DOC3-salt(+3 secrets), PKG2 dispatched on branches from c6c18611; minors recorded.
- the LINTFIX branch history is rewritten (unpushed) to drop the accidentally committed 9 MB dms-sync binary rather than `git rm` it in a follow-up — a binary in history is permanent once pushed — cost if wrong: none; nothing had been shared.

## Minor findings deferred by workstream (recorded, not fixed)

Each line is a reviewer-confirmed minor that was consciously left for a later
round; the per-task reports under the SDD ledger carry the detail.

- **PKG** — ToGRPCError validation text keeps "CODE: " prefix (web humanizer depends on it).
- **PKG** — idempotency release/complete not owner-scoped after 5-min reclaim — long request can delete reclaimer's row.
- **PKG** — apiKeyTouches map never evicts.
- **PKG** — API keys of suspended users still authenticate (auth_lookup_api_key_by_hash ignores users.status) → sent to AUTH.
- **PKG** — SessionAuth now ~4 round-trips per request — load-test later.
- **PKG** — rolling deploy flaps stream config until fleet upgraded — release notes.
- **PKG** — tenant.go import grouping; commit 0d7bdc24 uses test(pkg) prefix.
- **SIG1** — orphaned vendor envelope when two sends race; HandleReturn expired-path race can drop a paid QTSP signature; ConcurrentReturn test comment stale and slow; SetSignerQESEvidenceTx unconditional; vendor/DB error text in 502 body and OAuth redirect, unescaped reason=; 120 s WriteTimeout on /internal/seal*; duplicated repeatReader, unused UpdateSigners, OAuth state sweep per-tenant only.
- **DB** — real nats.go maps ErrNoResponders→ErrNoStreamResponse (counted) — document trade-off; tests over-grant dms_app; DLQ and replay drop trace_context; ForEachTenant cleanup stops at first failing tenant, soft-deleted tenants never cleaned; no concurrent-publisher test; app-vs-DB clock in backoff; cmd/dms-admin go.mod drift (→DEPS); outbox_claim_batch returns full rows (plan-mandated by DEC-6).
- **SIG1** — version created if request closes between gate and CompleteTx (warn log only); concurrent deliveries can each create a version.
- **SIG1** — newIngestFixture lost its 2-minute context timeout; abandoned vendor envelopes polled until the vendor expires them.
- **AUTH** — clientIP comment overstates trust until swap; 000111 definers not bound to current tenant (any dms_app caller can change any org); TestChangeRole_LastOwnerProtected never reaches the last-owner guard; email/SMS factor swap without step-up; no rate limit on enroll routes (SMS pumping); TOTP replay key survives re-setup; weak EXECUTE check in TestProdPosture_OrgMutationDefiners.
- **DB** — transient_failures not reset by counted failures (comments say consecutive); retry at 2m cap may land just outside JetStream duplicate window.
- **DOC1** — wrong header comment in holds.go; UploadRegionFor existence oracle before permission check; claimBlobTx up to 25 policy calls inside an open tx and FOR UPDATE held across policy RPC; refcount leak when two versions of one doc share a blob; M-3 test lacks declared-record case; no TestProdPosture for purge UPDATE/DELETE grants; regionToProto maps me-central-1 to UNSPECIFIED (cross-workstream).
- **WF** — saved-search alert sends hardcoded role member (viewer owner gets member ACL); authorizeServiceCall sends empty X-Gateway-Signature when SEDOC_GATEWAY_SECRET unset; notify_test duplicates consumer decode logic; H-10 check and write in separate txs; chunked oversized bodies reach handler (400 not 413) — pkg MaxBody behaviour.
- **STORAGE** — cross-region identical content hard-rejected (UNIQUE(tenant_id,sha256) product limitation — document/ticket); reaper fixed tenant order + WORM zero-ref re-listing starves later tenants; D-17 relies on S3 refusal and maps transient S3 errors to 409 (needs PKG GetObjectRetention).
- **PLAT** — M-10 rollback to old build crash-loops (delete durables first — runbook →OPS) and ensure() never reconciles FilterSubject; jsconsumer copied into 4 modules + search re-implements internal-key check (→pkg); audit 000006 down restores inconsistent partition posture; redaction only scrubs actor keys (resource_id/title survive); TestCreateSavedSearch_PersistsFilters is unit-only; implementer used bare git stash once (no damage).
- **STORAGE** — abort→reclaim guard depends on wall-clock across replicas (harden with '-infinity' expires_at or explicit aborted-state refusal); abandoned scanning sessions wedge up to 15 extra minutes (accepted).
- **DOC3** — D-9×D-13 — a deploy that minted zt links with the dev pepper loses them when a real pepper is set (72 h links; config note →DEPLOY); TestDBInfo_MemberForbidden uses a fake checker (real platform_admins SQL untested under dms_app); WOPI policy stub proves plumbing only; vacuous replay assertion in TestOnlyOfficeCallback_ReplayRejected; view consumed in a tx separate from the byte read (blob failure burns a view).
- **CONN** — IMAP high-water mark stalls when all fresh UIDs are legacy; ssrf.IsInternalIP lacks NAT64/6to4/Teredo/198.18/15 (→ netguard adoption); role-gate tests use invalid roles editor/viewer, guest untested; TestProdPosture_IngestCreatorRoleLookup builds its own users table; N-8 per-part re-file residual needs document-side idempotency key (→DOC2/final triage). DEC-13 ingest-route needs_review half lives in document ingestion.go (→DOC2, already assigned).
- **DOC1** — fast path holds a pool connection + row lock across an S3 call; purge response can block up to the budget.
- **PLAT** — erasure marker is service-enforced, not definer-enforced (dms_app can call the definers directly and skip it; checkpoint only notices if the tail was checkpointed); parkOnDLQ blocks the subject's push callback ~100 s per poison message during a DLQ outage.
- **CONN** — 60 s ReadTimeout also bounds a 64 MiB capture upload (add SetReadDeadline on capture routes); MaxBody sits outside RequireGatewaySignature (unsigned oversized → 413 not 401, harmless); TestOAuthCallback_NoSessionForbidden exercises legacy POST path.
- **DOC3** — filtered keyset feed never re-surfaces a later-granted document until it changes (document for dms-sync →MISC); TestOwnedRoutes_MaxBody covers declared Content-Length only.
- **DOC1** — two comments overstate the C-7 guarantee (commit failure after S3 success still leaves a bytes-gone row; 20 s per-blob timeout can expire mid-S3 if the lock wait ate the window — SET LOCAL lock_timeout ~5 s would harden); WORM bucket naming duplicated in document + storage (→ pkg/storage helper); WORM zero-ref blobs never reaped (pre-existing).
- **SIG2** — proto/gen/go/signerv1 regeneration + Go Signer.Verify/DSSSidecarSigner.Verify removal (→DEPS/MISC); parse-phase context checks (10 s budget only between steps); DocuSign dedup key format change across deploy; netguard Proxy:nil breaks egress-proxy deployments (document); B-T/B-LT on DSS 6.3 untested offline.
- **PLAT** — ListTenantIDs-sourced tenant walk skips audit rows of deleted organizations.
- **PLAT** — backdated tampered event with no later post-cutover event escapes the cutover check; commit-to-sign window lets canonical appends be signed unwalked; tenants of deleted organizations skipped.
- **COLLAB** — CB-10 reconnect pre-claim of a clientID (cosmetic); no Origin allow-list on upgrades (SameSite=Lax mitigates; predates task → final triage); README stale; /auth/me call per upgrade has no rate limit in front when :8083 is reached directly.
- **PLAT** — check-then-sign race after unlock (canonical appends only); after key rotation tenants are refused until the service signs a fresh checkpoint (no _PREV in audit).
- **SIG2** — first-signing exception lets a field gain /V + any /AP (matches DocMDP fill permission); unconfirmed forged Adobe event still writes dedup row + audit outbox before the confirm check (noise); TSA-check-applies-to-mock only in report notes.
- **COLLAB** — no rate limit on relay ask-back and every 30 s resync republishes the delete set per room (bandwidth only); commentInDoc list unbounded (document GET /comments/{id} would fix); onRelayFrame applies relay updates while stopping (harmless, untested); document-service outage unsubscribes every /ws doc (fail closed; WEB retries with backoff); /ws doc_id not canonicalized.
- **PLAT** — pre-cutover tampering undetectable by the rewrite path (cheap hardening: verify prior checkpoint signature + stored hash at cp.EventCount-1 before rewriting); mayResign check-then-sign window after unlock; key rotation fails closed (documented).
- **PY** — /auth/me has no group_ids → cookie identities lose group-scoped retrieval grants (→AUTH follow-up, final triage); worker-loss counter double-increment on visibility-timeout redelivery; cryptography floor not pin; litellm 1.53.1 / httpx 0.26.0 not reviewed; redaction ON CONFLICT DO NOTHING without refcount bump (→DEC-8 triage); /qa/history not owner-scoped; CUDA image build + torch 2.6 model loads unverified (final verification).
- **DOC2** — ReasonAmbiguousMatch reused for "uploader lacks edit" (needs CHECK migration); m365 prefix grants 64 MiB to every m365 route; fixed_date cutoff = Jan 1 of the following year + period is the implementer's interpretation (no column in schema).
- **MISC** — 000012 EXECUTE grant guarded on dms_app existence (deploy ordering →DEPLOY); MCP tools now role api_key upstream — smoke-test start_workflow/create_document at final verification; TestSSE_CloseDuringSendNoPanic sequential (being strengthened).
- **SIG2** — update trailers' /Prev not checked against previous startxref (reader xref-repair steering, reader-dependent); extractStream takes the first `N 0 obj` not the xref-current definition (pre-existing; material still issuer-verified); third-party signers rewriting XMP in place now read as not tamper-evident (accepted fail-closed).
- **PY** — preview durable DeliverPolicy.ALL replays the whole DOC_EVENTS backlog on first deploy (→DEPLOY/OPS note: consider NEW or plan the backfill); preview falls back to the event's storage_uri undecrypted when the blob row is missing (defence-in-depth); _cache_manifest read-then-write race; settings.nats_stream unused; CloudEvent tenant extension not compared with data.tenant_id; bomb-warning filter is process-global.
- **MISC** — dms-sync check-then-write race needs a local process with the user's own permissions (accepted for a single-user desktop tool); Windows junctions refused via !IsDir + containment but untested; billing webhook accepts unverified events only when the secret is empty (prod boot requires it).
- **CI** — integration/migrations not in pathspec; format()-arg dynamic SQL; SET NOT NULL / ATTACH PARTITION; service containers tag-pinned; helm v3.14.4 vs v3.22.0; signer tests run twice (harmless).
- **WEB** — recordSignature token in query string (link contract; ask SIG about header); extension OAuth endpoints do not exist (product gap); extension/mobile/add-ins not built (no installs).
- **CI** — ingress step tests -x (use -f + bash, exit 1 once script lands); integration/ linted twice (harmless); 3 pre-existing continue-on-error/|| true at ci.yml:319,706,864 (final triage).
- **COLLAB** — no Origin allow-list on cookie-authenticated upgrades (SameSite=Lax mitigates; final triage).
- **DOC2** — if the migration owner role lacks BYPASSRLS on a target deployment the backfill silently updates 0 rows (DEC-1 says migrations run as the owner — confirm per deployment; runbook note →OPS); reviewer did not run tests (coordinator post-merge run covers).
- **WEB** — MFARecovery response still carries session_token server-side (web never calls it; wrap if added); mobile stores the bearer in SecureStore (pre-existing, appropriate); add-ins keep the M365 exchange token in memory (ADR 0112); raw login body transiently in axios memory until AUTH stops returning session_token (follow-up).
- **OPS** — vaultdms_require_license default true breaks license-less Ansible installs (release notes); users.deleted_at / organizations-no-RLS assumptions in soc2 query unverified; Init error handling in 5 non-document services not opened.
- **OPS** — check-metrics skips labels fed via helper params / struct-field metric assignment (skipped, never falsely passed); ec2 scripts carry 8 old shellcheck info findings; vaultdms_require_license default → release notes.
- **DEPLOY** — offline renderer uncommitted (CI runs real helm/lint); subcharts never rendered; 64 MB cap also on signatures/connectors/intelligence/mcp (documented, services' MaxBody authoritative).
- **DEPLOY** — trusted-proxy breadth guard is CI-only (template enforces non-empty; an operator can still set a /9 or a superset) — either `fail` in the template on RFC1918-overlapping prefixes ≤ /12 or reword values comment; /wopi inherits the global per-IP rate limit (editor server single IP → route-level class); a values override can drop /wopi from longRunning.paths (comment); Helm side of SEDOC_S3_PUBLIC_BASE not checked.
- **DEPS** — pkg/testutil/containers.go uses testcontainers 0.44-deprecated RunContainer (SA1019; unchanged lines, so the lint ratchet won't flag — follow-up to postgres.Run/tcredis.Run); .golangci.yml run.go (CI set it to 1.26 — verify); markerRE lives in mock.go.
- **DEPLOY** — extraEnv can silently override chart-owned env (add fail guard or values warning); NOTES should state no Go pod starts without sedoc-license unless license.enabled/requireLicense=false; ediscovery empty-key fallback is a Go fix (→ DOC3 follow-up dispatched).
