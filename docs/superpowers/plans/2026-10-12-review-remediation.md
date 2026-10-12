# Codebase Review Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix every verified finding of the 2026-10-12 review and every round-2 finding that survives verification, each pinned by a test.

**Architecture:** Seventeen workstreams, each owning a disjoint set of files, each on its own branch in its own worktree, merged into one integration branch. Shared design decisions (identity, RLS bypass mechanism, outbox drain, client IP) are pinned once in this document so parallel workstreams agree on interfaces.

**Tech Stack:** Go 1.26 workspace (pgx v5, chi, grpc-gateway, NATS JetStream, Temporal, OPA), Postgres 16 RLS, Python 3.12 FastAPI/Celery, Node 22 (ws/Yjs), React 18/Vite, Kotlin/Gradle signer, Helm, Kong.

**Spec:** [docs/audit/2026-10-12-codebase-review.md](../../audit/2026-10-12-codebase-review.md) (IDs C-, H-, M-, L-) and [docs/audit/2026-10-12-codebase-review-addendum.md](../../audit/2026-10-12-codebase-review-addendum.md) (IDs R-, S-, D-, N-, W-, C-, O-). Note the addendum reuses `C-n` for collaboration; in this plan collaboration items are written `CB-n` (addendum C-n) to avoid clashing with the report's critical `C-n`.

## Global Constraints

- Integration branch `fix/review-2026-10`, created from `neu-ui` at `e9e74eec`. Each workstream branches from the integration branch's tip at its start: `fix/rv-<ws>` in worktree `/home/sedoc/DOCMS/.worktrees/rv-<ws>`.
- Never push, never commit to `neu-ui` or `main`, never touch the main worktree (`/home/sedoc/DOCMS`) — a systemd Vite server runs from it. Never `docker compose up/down/restart` the live dev stack.
- A workstream edits only the files it owns (table in §2). A needed change outside ownership goes in the hand-back under "Cross-workstream needs", never into the diff.
- New migrations only with the numbers allocated in §3.
- No `go.mod`/`go.sum` edits except in WS-DEPS. No new third-party Go dependencies anywhere. npm dependency changes only in WS-WEB and WS-COLLAB; Python requirements only in WS-PY; Gradle only in WS-SIG2.
- Unverified IDs (S-, D-, N-, W-, CB-, O-) are verified in code before fixing. A refuted item gets no code change and is reported as refuted with the evidence.
- One commit per finding (or tightly coupled pair). Message: `fix(<area>): <summary> [<ID>]`, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Go verification per touched module: `cd <module> && go build ./... && go vet ./... && go test -race -count=1 ./...`. Integration tests only for touched packages: `go test -tags integration -count=1 -p 1 ./<pkg>/...`. RLS work adds a `TestProdPosture_*` test using `pkg/testutil.NewProdPostureDB`.
- Python verification runs inside the existing images with the worktree mounted, e.g. `docker run --rm -v <wt>/services/intelligence:/app -w /app --entrypoint python sedoc-dev-intelligence-worker-misc:latest -m pytest -q tests/<file>` (preview: `sedoc-dev-preview:latest`; `pip install pytest` inside the throwaway container if missing).
- Web verification: symlink `web/node_modules` from the main worktree unless the workstream changes `package.json`; run `npx tsc --noEmit`, `npx vitest run <files>`, `npm run lint`.
- Hand-back lists, per ID: fixed / refuted / partial / blocked, commit SHA, test name; then cross-workstream needs; then config changes needed (env vars, Kong routes, Helm values, compose).

## Review Focus

- Requests that arrive through the Vite host-mode proxy or a published backend port with forged `X-User-*`/`X-Tenant-ID` headers and no cookie must get 401, on every service. Owner: WS-PKG (test `TestIdentityHeadersIgnoredWithoutInternalKey`) plus each service workstream's wiring test.
- Every RLS fix must pass under the NOBYPASSRLS `dms_app` role, not just under the dev superuser. Owner: every workstream touching SQL (a `TestProdPosture_*` per fix).
- Existing data must survive: upload sessions created before migration 000107 still download; legal holds created through the compliance API keep working after 000106; outbox rows queued before 000104 still drain. Owners: WS-STORAGE (`TestGetDownloadURL_LegacySessionUsesCreatedAtMonth`), WS-DOC1, WS-DB (`TestProdPosture_OutboxDrainPublishesPreexistingRows`).
- A purge, copy or dedup on a blob shared by two documents must never remove bytes the other document still references. Owner: WS-DOC1 (`TestPurgeDocument_SharedBlobSurvives`).
- Every webhook, event-stream or SSE fan-out must exclude auth/session/api-key events and secret-bearing payloads. Owner: WS-CONN (`TestCreateWebhook_RejectsAuthEvents`, `TestFanout_NeverDeliversAuthSubjects`) and WS-AUTH (`TestPasswordResetEvent_HasNoToken`).

---

## 1. Cross-cutting design decisions

These are fixed; implementers do not revisit them.

**DEC-1 Runtime DB role.** All deployments connect services as `dms_app` (NOBYPASSRLS); migrations run as the owner role. Helm and Ansible switch their runtime role from `vaultdms` to `dms_app` (WS-DEPLOY, WS-OPS). Migrations stop creating LOGIN roles with a fixed password: new installs get `NOLOGIN` roles from the migration and deployment tooling sets `LOGIN PASSWORD` from secrets (`scripts/init-db.sql` keeps dev working) [O-6].

**DEC-2 Pre-tenant lookups.** Anything that must find a row before the tenant is known goes through a `SECURITY DEFINER` SQL function owned by a dedicated `NOLOGIN BYPASSRLS` role, `SET search_path = pg_catalog, public`, `REVOKE ALL FROM PUBLIC`, `GRANT EXECUTE TO dms_app`, returning the minimum columns, exact-match only. Pattern: `services/auth/migrations/000001_pretenant_lookup_functions.up.sql`. Everything after the tenant is known runs in `database.WithTenantTx`.

**DEC-3 Identity.** Every user-facing HTTP surface authenticates with `SessionAuth`, `SessionAuthOptional`+handler 401, `SessionOrAPIKey` or `APIKeyAuth`. `IdentityHeadersHTTP` honours `X-Auth-Tenant-ID`/`X-Tenant-ID`/`X-User-*` only when the request also carries a valid `X-Internal-Service-Key` (constant-time) and no session identity is present; otherwise it ignores them. `TenantHTTP`, `TenantInterceptor` and document `grpcGatewayInject` take identity from context first; a header tenant that differs from the context tenant returns 403. The Vite host-mode proxy strips identity headers; the web client stops sending them.

**DEC-4 Python services.** A FastAPI dependency `require_identity` accepts a request only if `X-Gateway-Signature` matches `SEDOC_GATEWAY_SECRET` (or `_PREV`) in constant time, then resolves identity either from the `dms_session` cookie via `GET http://auth:8080/api/v1/auth/me` (forwarding cookie and signature, cached in Redis 30 s by `sha256(cookie)`), or from identity headers when `X-Internal-Service-Key` is valid. The preview REST API becomes internal-only (internal key required; Kong route removed by WS-DEPLOY) because the web client already goes through the document service.

**DEC-5 Client IP.** New `middleware.ClientIP(r *http.Request) string`: if `RemoteAddr` is in `SEDOC_TRUSTED_PROXIES` (comma-separated CIDRs, default empty), return the right-most `X-Forwarded-For` hop not in the trusted set, else `RemoteAddr`'s host. Used by correlation, request log, rate limiter and auth `clientMeta`. Kong gets `trusted_ips`/`real_ip_header`, and removes `True-Client-IP` (WS-DEPLOY); chi `RealIP` is removed from the auth router (WS-AUTH).

**DEC-6 Outbox drain.** Migration document 000104 adds role `dms_outbox_drain NOLOGIN BYPASSRLS` and function `outbox_claim_batch(p_limit int) RETURNS SETOF outbox` that selects due unpublished rows `ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT p_limit` (locks stay with the caller's transaction). The publisher then sets `app.current_tenant` per row (`set_config(..., true)`) before each `UPDATE`/`DELETE`. Cleanup runs through `ForEachTenant`. `BaseBackoff` defaults to 2 s; connection/timeout publish errors do not count toward `MaxAttempts`.

**DEC-7 Legal holds.** One function `func (s *Service) assertNotHeld(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID, op string) error` checks both `lifecycle_state`/`IsLegalHoldBlocked` and the `legal_hold_documents` binding table; every mutating path calls it. In-band `apply_hold`/`release_hold` write the real schema (`legal_holds` row + binding row); migration 000106 only if a column is genuinely missing.

**DEC-8 Purge.** In one tenant transaction: lock the document, re-check `deleted_at IS NOT NULL` and `purgeGuard`, delete version rows, decrement each referenced blob's `reference_count`, delete `content_blobs` rows that reach 0 and have no remaining version reference, collect their keys, delete the document. After commit, delete those S3 objects only. Shared blobs are never touched.

**DEC-9 Upload object keys.** Migration 000107 adds `storage_bucket`, `storage_key` to `upload_sessions`, written at initiate. Complete/Abort use them. `GetDownloadURL` uses `content_blobs.storage_key` via the session's `content_blob_id`, then the persisted session key, then (legacy rows only) the key recomputed from `upload_sessions.created_at`.

**DEC-10 Storage encryption.** Storage refuses to start without a KeyManager unless `SEDOC_STORAGE_ENCRYPT_AT_REST=false` is set explicitly. Dev compose keeps `false` until WS-PY's preview/redaction decrypt lands; production manifests require the KEK.

**DEC-11 Region pin.** A new document inherits its workspace's `region_pin`, else the tenant's, else `SEDOC_REGION`. The storage proxy derives `region_pin` for initiate from the target folder/document, ignoring the body. `CreateVersion` rejects a blob whose `storage_region` differs from the document's `region_pin` with `vdmserr.RegionViolation`. `allowedRegions` gains `me-central-1`.

**DEC-12 Blob reuse.** A blob id or checksum supplied by a caller (initiate dedup, `CreateVersion`, ERP `blob_checksum`) is accepted only if the caller completed the upload session that produced it, or holds `view` on a document that already has a version referencing it. Otherwise dedup falls through to a real upload and `CreateVersion` returns NotFound.

**DEC-13 System principals.** Retention and ingest-route commits never fake a role. Retention marks the context with an unexported key set only by the sweep, and `UpdateLifecycle` skips `requirePermission` for that marker. Ingest-route replays the uploader's real role and requires `edit` on the matched document; a deny routes the item to `needs_review`.

**DEC-14 API keys on the auth service.** `api_key` principals are rejected with 403 on every `/api/v1/auth/*` route except `GET /api/v1/auth/me`. Credential-management routes (MFA, WebAuthn, sessions, API keys) additionally require a session.

**DEC-15 M365 exchange.** Map by `preferred_username` (UPN). Use `email` only when `xms_edov == true`. Require `user.Status == active`. When the tenant MFA policy is `required`, require `amr` to contain `mfa`, else 403.

**DEC-16 Externally subscribable events.** One allow-list, `connector/internal/eventpolicy.Subscribable(subject string) bool`, true only for prefixes `dms.document.`, `dms.version.`, `dms.folder.`, `dms.workflow.`, `dms.signature.`, `dms.task.`, `dms.tag.`, `dms.comment.`, `dms.record.`. It gates webhook create, fan-out and the event-stream mirror. The password-reset event carries no token (WS-AUTH).

**DEC-17 Signature bytes.** QES start and e-sign send ignore any client-supplied bytes, hash or signer identity; the server loads the request in a tenant transaction and reads bytes from the request's `(DocumentID, VersionID)`. The web sign page forwards `?token=` (WS-WEB).

**DEC-18 Shared SSRF client.** New package `pkg/netguard` (WS-SIG2): `func NewClient(opts Options) *http.Client` with a dial-time deny of loopback, RFC1918, link-local, CGNAT and IPv6 equivalents (unless `opts.AllowPrivate`), redirect cap 3, timeout default 10 s; plus `func LimitedBody(r io.Reader, max int64) ([]byte, error)`. Signature OCSP/CRL uses it; other Go callers may adopt it later.

**DEC-19 Body limits.** New `middleware.MaxBody(n int64) func(http.Handler) http.Handler` (WS-PKG) wraps `http.MaxBytesReader`; services wrap JSON routes with 1 MiB and document-carrying routes with 64 MiB; servers set `ReadTimeout` 60 s, `WriteTimeout` 120 s, `IdleTimeout` 120 s unless streaming.

## 2. Workstreams and file ownership

| WS | Branch suffix | Owns | IDs |
|---|---|---|---|
| PKG | pkg | `pkg/middleware/**`, `pkg/errors/**`, `pkg/health/**`, `pkg/events/**`, `pkg/gateway/**`, `pkg/dlp/**`, `pkg/storage/s3.go`, `pkg/metrics/**`, auth migration 000003, `services/document/cmd/server/main.go` (only `grpcGatewayInject` and the share/zt limiter lines), `services/mcp-server/internal/tools/tenant_isolation_prodposture_test.go` | C-3 (middleware half), C-6 (TenantHTTP/interceptor/grpcGatewayInject/IdentityHeadersHTTP), M-8 (SessionAuth status), M-17, M-33, R-1, R-2, R-3, R-4, R-9, R-10, CB-8, CB-9, CB-11, O-5 (stream retention), DEC-3, DEC-5, DEC-19 |
| DB | db | `pkg/database/**`, `services/policy/internal/service/{service.go,tx.go}`, `cmd/dms-admin/**`, document migrations 000104 and 000108, intelligence migration 000006 | C-4, C-5, H-13, R-8, O-7, M-28 (`outbox_dlq`, `privacy_ledger`), L-10 (FORCE on six tables), DEC-6 |
| AUTH | auth | `services/auth/**` (except migration 000003), `pkg/notifications/**`, document migration 000111 | H-5, H-6, H-7, M-8 (auth paths), M-9, M-27, L-1, L-2, L-3, L-4, L-5 (auth half), N-1 (reset-token half), CB-4, inventory auth raw-pool sites, DEC-14, DEC-15 |
| DOC1 | doc1 | `services/document/internal/service/{documents.go,folders.go,external_key.go,copy*.go}`, `repository/{document_repo.go,folder_repo.go,misc_repo.go}`, `handler/storage_proxy.go`, `internal/compliance/**`, document migration 000106 | C-7, M-1, M-2, D-1 (blob half), H-8, H-26, M-3, M-4, M-5, M-6, M-7 (document half), DEC-7, DEC-8, DEC-11, DEC-12 |
| DOC2 | doc2 | `services/document/internal/service/{retention.go,ingestion.go}`, `internal/{records,autolink,ingestroute,janitor}/**`, `handler/{retention_sweep_handler.go,records_handler.go,contract_graph_handler.go,m365_ingest.go}`, document `cmd/server/main.go` (records sweep mount only) | D-7, D-14, D-16, D-19, D-20, D-23, D-24, D-25, DEC-13 |
| DOC3 | doc3 | `services/document/internal/handler/{zt_share*,watermark*,onlyoffice*,wopi*,coauth*,irm*,db_info*,share*}`, `service/{sharing_tags.go (share-link lookup only),irm_service.go,sync_service.go}`, `repository/{sharelink_repo.go,irm_repo.go,sync_repo.go}`, document `cmd/server/main.go` (db-info, zt mounts, secret loading), document migrations 000105, 000109, 000113 | M-26, M-31, D-1 (sync half), D-2, D-3, D-4, D-5, D-6, D-8, D-9, D-10, D-11, D-12, D-13, irm/zt raw-pool sites |
| STORAGE | storage | `services/storage/**`, `services/document/internal/handler/worm_handler.go`, document migration 000107 | H-9, H-20, M-7 (storage half), L-6, D-15, D-17, D-18, D-21, D-22, O-9 (`EnforceRegion` wiring), DEC-9, DEC-10 |
| SIG1 | sig1 | `services/signature/{cmd,internal/handler,internal/service,internal/repository,internal/model}/**`, `pkg/esign/oauth.go`, document migration 000112 | C-6 (signature wiring), H-1, H-2, M-15, R-5 (signature), S-1, S-2, S-3, S-5, S-9, S-10, S-14 (server), signature raw-pool sites, DEC-17 |
| SIG2 | sig2 | `services/signature/internal/pades/**`, `pkg/signing/**`, `pkg/esign/**` (except oauth.go), `pkg/netguard/**`, `services/signature-signer/**` | H-3, S-4, S-6, S-7, S-8, S-11, S-12, R-17, DEC-18 |
| CONN | conn | `services/connector/**`, connector migration 000002 | H-4, M-16, N-1 (fan-out half), N-2 … N-12, DEC-16 |
| WF | wf | `services/workflow/**` | C-6 (workflow wiring), H-10, H-11, M-12, M-13 (caller), workflow raw-pool sites |
| PLAT | plat | `services/{audit,search,notification}/**`, `services/policy/**` except DB's two files, audit migration 000006, notification migration 000004 | H-12, M-10, M-11, M-13 (search side), M-14, L-7, L-8, L-10 (audit partitions), M-28 (notification tables), CB-3 (policy context), O-5 (audit DLQ), search/notification identity ordering, notification `LIMIT :0`, `createSavedSearch` filters |
| PY | py | `services/intelligence/**` except migrations, `services/preview/**` | C-8, H-15, H-16, H-17, M-18, M-19, M-20, M-21, M-22, M-23, M-24, M-25 (deps), DEC-4 |
| WEB | web | `web/**`, `extension/**`, `addins/**`, `mobile/**` | H-24, H-25, M-29, M-30, L-12, L-14, S-13, S-14 (client), W-1 … W-10, npm advisories, DEC-3 client half |
| COLLAB | collab | `services/collaboration/**` | R-16 (code), CB-1, CB-2, CB-3 (collab half), CB-5, CB-6, CB-7, CB-10, CB-12 |
| MISC | misc | `cmd/dms-sync/**`, `services/graphql-gateway/**`, `services/mcp-server/**` (except the PKG test), `services/billing/**`, billing migration 000012, `services/task/**` | H-14, R-5 (MCP), R-6, R-7, billing org writes, billing raw-pool, `stripe_customer_map` RLS |
| OPS | ops | `deploy/monitoring/**`, `deploy/ansible/**`, `deploy/load-test/**`, `deploy/ec2/**`, `pkg/license/**`, `scripts/soc2-evidence.sh`, `dms-provision.sh`, new `deploy/helm/sedoc/templates/monitoring/**` | O-1, O-2, O-3, O-8, O-10, O-11, O-12, O-14 |
| CI | ci | `.github/**`, `.golangci.yml`, new `scripts/check-migrations.sh` | R-11, R-12, R-13, O-13, M-25 (pip-audit flag) |
| DEPLOY | deploy | `deploy/helm/**` (except OPS's new folder), `deploy/gateway/**`, `deploy/nats/**`, `deploy/regions/**`, `docker-compose*.yml`, `build/**`, Dockerfiles outside service dirs, `web/Dockerfile` (new), `Makefile`, `install.sh`, `scripts/install/**`, `scripts/migrate-all.sh`, `scripts/init-db.sql`, `.env.example`, document migrations 000001/000065 (role creation only) | C-1, C-2, H-18 … H-23 (config parts), M-32, M-34 (deploy parts), R-14, R-15, R-16 (Helm env), R-18, R-19, O-4, O-6, O-9 (chart), Kong items from all hand-backs |
| DEPS | deps | every `go.mod`/`go.sum`, `go.work`, Go Dockerfile base images | §8 Go advisories, toolchain 1.26.6 |

## 3. Migration number allocation

| Service | Number | Owner | Purpose |
|---|---|---|---|
| document | 000104 | DB | outbox drain definer (DEC-6) |
| document | 000105 | DOC3 | share-link token lookup definer |
| document | 000106 | DOC1 | legal-hold schema alignment, only if needed |
| document | 000107 | STORAGE | `upload_sessions.storage_bucket/storage_key` |
| document | 000108 | DB | RLS hardening: FORCE on `tenant_keks`, `idempotency_keys`, `document_entities`; ENABLE+FORCE+policy on `outbox_dlq`, `privacy_ledger` |
| document | 000109 | DOC3 | IRM token lookup definer; replace fail-open policy |
| document | 000110 | PKG | idempotency reclaim column, only if `created_at` is insufficient |
| document | 000111 | AUTH | organizations mutation definers (`org_set_region_pin`, `org_create`, `org_set_status`, `org_set_feature_flags`) |
| document | 000112 | SIG1 | e-sign OAuth nonce table |
| document | 000113 | DOC3 | zt-share hash-only token storage |
| auth | 000003 | PKG | `auth_session_identity(p_hash text)` returning tenant, user, email, role, status, expires_at for active users only |
| audit | 000006 | PLAT | anonymize definers; partition grant/RLS hardening |
| billing | 000012 | MISC | `stripe_customer_map` RLS + definer lookup |
| connector | 000002 | CONN | Google OAuth nonce; global `host_path` uniqueness |
| intelligence | 000006 | DB | FORCE on `document_classifications`, `intel_processed_events`, `ocr_processed_events` |
| notification | 000004 | PLAT | RLS on `push_devices`, `notification_user_prefs` |

## 4. Execution order

1. **Wave 1:** PKG, DB, AUTH, DOC1, SIG1.
2. **Wave 2:** STORAGE, DOC3, CONN, WF, PLAT.
3. **Wave 3:** DOC2 (after DOC1 merges), SIG2, PY, WEB, COLLAB, MISC.
4. **Wave 4:** OPS, CI, DEPLOY (DEPLOY receives every "config changes needed" list).
5. **Wave 5:** DEPS, then whole-branch verification and review.

At most five implementers run at once. After each implementer, a reviewer (Octopus persona) checks the branch diff against its IDs; findings go back to the same implementer. Reviewed branches merge into `fix/review-2026-10` with `--no-ff`; the coordinator resolves conflicts and re-runs the touched modules' tests.

---

## 5. Task blocks

Each line is one task: ID → the decided behaviour → the test that pins it. Steps for every task are the same five: write the named failing test, run it and see it fail, implement, run it and the package tests green, commit.

### Task PKG — shared middleware and libraries

**Interfaces produced:** `middleware.ClientIP(r *http.Request) string`; `middleware.MaxBody(n int64) func(http.Handler) http.Handler`; `middleware.NewIPRateLimiter(rate, burst int, window time.Duration)` (signature unchanged, real token bucket keyed by `ClientIP`); `IdentityHeadersHTTP()` semantics per DEC-3.

- [ ] C-3 → `SessionAuth`/`SessionAuthOptional` call `auth_session_identity($1)` (auth 000003), then load groups in `WithTenantTx`; `APIKeyAuth` calls `auth_lookup_api_key_by_hash($1)` → `TestProdPosture_SessionAuthResolvesUser`, `TestProdPosture_APIKeyAuthResolvesKey`; fix the MCP fixture to create `api_keys` with FORCE RLS → existing test now meaningful.
- [ ] M-8 → `auth_session_identity` returns rows only for `status='active'` and non-deleted users → `TestSessionAuth_SuspendedUser401`.
- [ ] C-6 → `IdentityHeadersHTTP` ignores headers without a valid internal key → `TestIdentityHeadersIgnoredWithoutInternalKey`; `TenantHTTP`/`TenantInterceptor` context-first, mismatch 403 → `TestTenantHTTP_HeaderMismatch403`; `grpcGatewayInject` uses context identity only → `TestGatewayInject_SessionBeatsHeaders`.
- [ ] R-1, R-2 → `ClientIP` per DEC-5 used by correlation, request log, limiter; limiter refills at `rate` per `window` → `TestClientIP_RightmostUntrustedHop`, `TestClientIP_UntrustedRemoteIgnoresXFF`, `TestIPRateLimiter_RefillsAndKeysPerClient`.
- [ ] R-3 → reserve reclaims `in_progress` rows older than 5 min; completion/release use `context.WithoutCancel` with 5 s timeout; panic in `next` releases then re-panics → `TestIdempotency_StaleReservationReclaimed`, `TestIdempotency_ClientDisconnectStillCompletes`, `TestIdempotency_PanicReleases`.
- [ ] R-4 → last-used touch via `context.WithoutCancel` + 2 s timeout, at most once per minute per key → `TestAPIKeyAuth_TouchSurvivesRequestEnd`.
- [ ] M-17 → `ToGRPCError` returns `"internal error"` for non-domain/internal errors; validation uses `e.Message` → `TestToGRPCError_ScrubsInternal`.
- [ ] R-9 → `/readyz` returns `"fail"` per dependency and logs the error → `TestReadyz_NoDriverErrorInBody`.
- [ ] R-10, CB-11 → delete `pkg/events/subscriber.go`, `pkg/gateway/`, `pkg/dlp/` after confirming no importers → `go build ./...` across the workspace.
- [ ] CB-8 → Go harvest test walking `services/**/*.go` (non-test) for `"dms.<x>.<y>.v<n>"` literals; every harvested subject must be in `PublishedSubjects` and covered by `DefaultStreams`; add the missing ~70 → `TestGoEmittedSubjectsCovered`.
- [ ] O-5 → `AUDIT_EVENTS` stream uses `DiscardNew` (publish fails loudly instead of dropping history) → `TestStreamConfig_AuditDiscardNew`.
- [ ] CB-9 → S3 clients take the configured region (default `us-east-1` only when empty) → `TestNewS3_UsesConfiguredRegion`.
- [ ] DEC-19 → `MaxBody` → `TestMaxBody_413OverLimit`.

### Task DB — database layer, outbox, policy loaders, admin CLI

**Interfaces produced:** `database.WithTenant` removed; callers use `WithTenantTx`. `dms-admin outbox replay-dlq [--tenant <uuid>] [--limit N] [--execute]`.

- [ ] C-4 → DEC-6 drain → `TestProdPosture_OutboxDrainPublishes`, `TestProdPosture_OutboxDrainPublishesPreexistingRows`, `TestProdPosture_OutboxCleanupDeletesPublished`.
- [ ] H-13 → `BaseBackoff` default 2 s; `nats.ErrTimeout`, `ErrNoResponders` (transient), connection errors do not increment attempts → `TestPublisher_DefaultBackoffApplied`, `TestPublisher_TransientErrorsDoNotDeadLetter`; replay command → `TestReplayDLQ_MovesRowsBackWithZeroAttempts`.
- [ ] C-5 → delete `WithTenant`; policy loaders use `WithTenantTx`; never cache a result from an errored read → `TestProdPosture_PolicyCheckSeesGrants`.
- [ ] R-8 → remove `rotate-secrets`; usage text points to `dms-admin kms rotate` → `TestDmsAdmin_NoRotateSecretsCommand`.
- [ ] O-7, L-10, M-28 → migrations 000108 and intelligence 000006; `AssertRLSPosture` also fails when any table with `tenant_id` lacks `relforcerowsecurity` → `TestProdPosture_ForceRLSOnAllTenantTables`.

### Task AUTH — auth service

- [ ] H-5, DEC-14 → middleware rejects `api_key` on auth routes except `GET /me` → `TestAPIKeyPrincipal_403OnWebAuthnRegistration`, `TestAPIKeyPrincipal_403OnMFASetup`, `TestAPIKeyPrincipal_200OnMe`.
- [ ] H-6 → LDAP success falls through to the MFA policy block → `TestLDAPLogin_RequiredPolicyChallenges`.
- [ ] H-7, DEC-15 → `TestM365Exchange_UnverifiedEmailIgnored`, `TestM365Exchange_SuspendedUser403`, `TestM365Exchange_RequiredMFAWithoutAmr403`.
- [ ] M-8 → status checks in `finishLogin`, `createSessionInTx`, passkey and Google paths → `TestPasskeyLogin_Suspended403`, `TestGoogleExchange_Suspended403`.
- [ ] M-9 → mount SCIM at `/api/v1/scim/v2/{tenant_slug}` (keep the old path) → `TestSCIMRoute_APIv1PrefixReachesHandler`.
- [ ] M-27 + inventory → SSO, SCIM, Twilio, SMTP repos and `tenant_admin.go:227` use `WithTenantTx`; LDAP scheduler uses `ForEachTenant`; API-key last-used in tenant tx; org writes through 000111 definers → `TestProdPosture_OIDCConfigLookup`, `TestProdPosture_SCIMBearerAuth`, `TestProdPosture_LDAPSchedulerFindsTenants`, `TestProdPosture_TenantRegionPinUpdate`.
- [ ] L-1 → MFA secrets sealed as `v2:` envelope (wrapped DEK stored with ciphertext) when KMS is set; legacy local-KEK format still decrypts → `TestMFASecret_KMSRoundTrip`, `TestMFASecret_LegacyDecrypts`.
- [ ] L-2 → remember accepted TOTP step per user in Redis for 90 s → `TestTOTP_ReplayRejected`.
- [ ] L-3 → exchanges use `clientMeta`/`ClientIP` → `TestM365Exchange_MultiHopXFF`.
- [ ] L-4 → WebAuthn refuses to start when enabled and `SEDOC_WEBAUTHN_HMAC_SECRET` is unset → `TestWebAuthn_RequiresDedicatedSecret`.
- [ ] L-5 → remove chi `RealIP` from the auth router → `TestAuthRouter_TrueClientIPIgnored`.
- [ ] N-1 → password-reset outbox payload carries no token; notification resolves the link from a short-lived Redis reference → `TestPasswordResetEvent_HasNoToken`.
- [ ] CB-4 → SMS/email senders return `ErrSMSDisabled`/`ErrEmailDisabled` unless configured or `SEDOC_MFA_DEV_STUB=1`; unconfigured methods hidden; verified enrollment flips method `pending→active` → `TestSMSOTP_NoStubWithoutExplicitFlag`, `TestMFAEnrollment_ActivatesOnVerify`.
- [ ] Client-only gates (addendum W list) → confirm server-side: KEK rotate/revoke owner-only, direct user create enforces password policy and refuses `owner`, role change refuses escalation to `owner` by admins and demotion of the last owner, API-key scopes capped to the caller's role → `TestEncryptionRotate_AdminForbidden`, `TestCreateUser_OwnerRoleRefused`, `TestChangeRole_LastOwnerProtected`, `TestIssueAPIKey_ScopesCappedToRole` (only where a gap is found).

### Task DOC1 — document data integrity

- [ ] C-7, DEC-8 → `TestPurgeDocument_SharedBlobSurvives`, `TestPurgeDocument_VersionedDocSucceeds`, `TestPurgeDocument_RestoredBetweenPhasesNotDeleted`, `TestEmptyTrash_DoesNotDeleteSharedBytes`; same rules in `PurgeFolder` → `TestPurgeFolder_SharedBlobSurvives`.
- [ ] M-1, M-2, D-1, DEC-12 → `TestCreateVersion_ForeignBlobNotFound`, `TestUpsertByExternalKey_ForeignChecksumRejected`, `TestStorageProxyDownload_VersionOfOtherDocument404`.
- [ ] H-26 → blob lookups in tenant tx → `TestProdPosture_CreateVersionLinksBlob`, `TestProdPosture_StorageProxyCompleteReturnsBlobID`.
- [ ] H-8, DEC-7 → `TestCreateVersion_BindingHoldBlocks`, `TestMove_BindingHoldBlocks`, `TestCopy_BindingHoldBlocks`, `TestRestoreVersion_BindingHoldBlocks`, `TestUpsertAppend_BindingHoldBlocks`; `TestApplyHold_RoundTrip` (integration).
- [ ] M-3 → `DeleteFolder` returns 409 listing held/record/WORM documents → `TestDeleteFolder_HeldDocument409`.
- [ ] M-4 → `Update` writes only changed columns under `SELECT … FOR UPDATE` → `TestUpdateDocument_DoesNotRevertConcurrentVersion`.
- [ ] M-5 → `UpdateLifecycleState(..., from)` with `AND lifecycle_state = $from`; 0 rows → Conflict → `TestUpdateLifecycle_ConcurrentTransitionConflicts`.
- [ ] M-6 → copy gets its own version row, `version_count=1`, refcount++ → `TestCopyDocument_ContentReadable`, `TestCopyDocument_SurvivesSourcePurge`.
- [ ] M-7, DEC-11 → `TestCreateDocument_InheritsWorkspaceRegion`, `TestCreateVersion_RegionMismatchRejected`, `TestStorageProxyInitiate_IgnoresBodyRegion`.

### Task DOC2 — document background flows

- [ ] D-16, DEC-13 → `TestRetentionSweep_ArchivesUnderRealPolicy`.
- [ ] D-14, DEC-13 → `TestIngestRouteCommit_NoEditOnMatchedDocGoesToReview`.
- [ ] D-7 → cutoff computed for `creation`/`fixed_date`; `Dispose` refuses when cutoff unresolved for non-declaration triggers → `TestDispose_UnresolvedCutoffRefused`.
- [ ] D-19 → `FindTargets` scoped to the source workspace; graph read requires `view` on root and drops unviewable nodes → `TestContractGraph_HidesUnviewableNeighbours`.
- [ ] D-20, D-23 → cutoff-sweep gated by internal key, tenant from body; `checkInternalKey` constant-time → `TestCutoffSweep_InternalKeyAndBodyTenant`, `TestCheckInternalKey_ConstantTime`.
- [ ] D-24 → validate tenant (Term on malformed); `ALLOW_DUPLICATE_FAILED_ONLY` → `TestIngestRouteConsumer_MalformedTenantTerminated`.
- [ ] D-25 → M365 ingest returns 502 and rolls back the row when the version upload fails; OrphanGC skips rows with `source='m365'`-style intentional metadata-only markers → `TestM365Ingest_UploadFailureNot201`, `TestOrphanGC_SkipsIntentionalMetadataOnly`.
- [ ] Client-only gate → records schedule/category writes require compliance_officer/admin/owner server-side → `TestRecordSchedule_MemberForbidden` (only if a gap is found).

### Task DOC3 — document public and editor surfaces

- [ ] M-26 → share-link lookup via 000105 definer then `WithTenantTx` → `TestProdPosture_ShareLinkResolves`.
- [ ] irm raw-pool + fail-open policy → 000109 definer, strict policy → `TestProdPosture_IRMLicenseLookup`, `TestIRMPolicy_EmptyGUCSeesNothing`.
- [ ] D-2 → watermark routes verify `version ∈ document` → `TestWatermarkStamp_ForeignVersion404`.
- [ ] D-3, D-10 → create requires `share` on the document and version∈document; atomic `UPDATE … WHERE view_count < max_views RETURNING`; manifest 410 when revoked → `TestZTCreate_RequiresShare`, `TestZTStream_MaxViewsAtomic`, `TestZTManifest_Revoked410`.
- [ ] D-4 → token-authorized read path that decrypts under `WithTenantTx` → `TestZTStream_NoSessionServesBytes`.
- [ ] D-9 → store only the hash; URL carries the pre-image (000113 migrates existing rows by hashing) → `TestZTToken_OnlyHashStored`.
- [ ] D-5, D-6, D-11 → lock key `wopiLockKey(lockID())`; `config` calls `AuthorizeCoauth`, sets `DocumentID`, 1 h TTL; callback enforces `exp`/`iat` and rejects a committed `key` → `TestOnlyOfficeSave_RespectsWOPILock`, `TestOnlyOfficeConfig_RequiresEdit`, `TestOnlyOfficeCallback_ReplayRejected`.
- [ ] D-12 → WOPI claims carry role; resolver reloads groups in tenant tx → `TestWOPIResolve_GroupGrantedEditor`.
- [ ] D-8, D-1 (sync) → delta filtered by reader set; `sha256` removed → `TestSyncDelta_HidesPrivateFolderDocs`.
- [ ] D-13 → IRM key, zt pepper, OnlyOffice JWT fail closed (503) outside `dev` → `TestIRMSigningKey_RequiredOutsideDev`.
- [ ] M-31 → `RequireRole("owner")` plus platform-admin check on db-info → `TestDBInfo_MemberForbidden`.
- [ ] Client-only gate → share-link creation from add-ins applies the tenant share policy (expiry, password, permissions) server-side → `TestCreateShareLink_TenantPolicyApplied` (only if a gap is found).

### Task STORAGE — storage service

- [ ] H-9, DEC-9 → `TestComplete_AcrossMonthBoundary`, `TestGetDownloadURL_OldMonthBlob`, `TestGetDownloadURL_LegacySessionUsesCreatedAtMonth`, `TestAbort_DeletesInitiatedKey`.
- [ ] H-20, DEC-10 → `TestStorageStartup_RequiresKeyManagerUnlessExplicitlyDisabled`.
- [ ] L-6 → dedup lookup matches the constraint (no region filter) plus region check; final-tx errors call `failUpload`; expiry checked before the `scanning` 409; `MarkExpired` runs every 5 min via `ForEachTenant` → `TestComplete_SecondRegionDuplicateFailsCleanly`, `TestClaim_StaleScanningReclaimable`, `TestProdPosture_MarkExpiredSweeps`.
- [ ] D-15 → same-bucket re-encrypt writes a new key and flips it in the UPDATE; old key deleted after commit; round-trip verify before PUT → `TestReencrypt_SameBucketFailureKeepsOldReadable`.
- [ ] D-17 → `GREATEST` on retain-until; mode downgrade refused; no self-copy when already in the WORM bucket → `TestApplyWORM_CannotShorten`, `TestApplyWORM_CannotDowngradeMode`.
- [ ] D-18 → `RewrapDEK` updates only envelope columns with CAS; `UpdateMigration` CAS on old bucket/nonce → `TestRewrap_ConcurrentReencryptConflict`.
- [ ] D-21 → reaper logs and continues per tenant → `TestReaper_TenantErrorDoesNotAbort`.
- [ ] D-22 → `liveKEKAlias` returns errors; base alias only on `ErrNoRows` → `TestLiveKEKAlias_DBErrorAborts`.
- [ ] O-9 → `RegionEnforcer.CheckBucket` in `CompleteUpload` → `TestComplete_BucketOutsideRegionRejected`.

### Task SIG1 — signature service

- [ ] C-6 → mux wrapped in `SessionOrAPIKey`; `IdentityHeadersHTTP` only inside → `TestSignatureAPI_NoSession401WithForgedHeaders`.
- [ ] H-1 → `signing_url` omitted from GET/list; returned only in the create 201 to the creator → `TestGetRequest_NoSigningTokens`.
- [ ] H-2 → reject unless `pending`/`in_progress` and not expired; reject re-sign; conditional `CompleteTx` with RowsAffected → `TestRecordSignature_CancelledRejected`, `TestRecordSignature_ExpiredRejected`, `TestRecordSignature_ResignRejected`.
- [ ] M-15 → repository in `WithTenantTx`; `UpdateStatus` checks RowsAffected → `TestProdPosture_SignatureRequestLifecycle`.
- [ ] S-1, DEC-17 → `TestEsignSend_NonCreatorForbidden`, `TestEsignSend_CompletedRequestRejected`, `TestEsignSend_UsesStoredVersionBytes`.
- [ ] S-2 → owner/admin gate; random single-use nonce (000112) bound to tenant+user, 10 min TTL → `TestEsignOAuthStart_MemberForbidden`, `TestEsignOAuthCallback_ReplayedStateRejected`.
- [ ] S-3, S-10 → `TestQESStart_SignerMustBeCaller`, `TestQESReturn_PersistsSignedHash`, `TestQESReturn_ConcurrentReturnKeepsCompleted`.
- [ ] S-5, R-5 → `MaxBody` 64 MiB on QES start/e-sign send/webhook; server timeouts → `TestEsignWebhook_413OverLimit`.
- [ ] S-9 → verify HMAC from stored secret before any client resolution; constant 401 body → `TestEsignWebhook_UnconnectedTenantIndistinguishable`.
- [ ] Raw-pool (`ingest_grpc.go:152`, `integrations_triggers.go:64`) → `TestProdPosture_SignatureTriggersFeed`.

### Task SIG2 — PAdES, TSP, e-sign providers, signer

- [ ] H-3, DEC-18 → verifier uses `netguard.NewClient`; `verify-bytes` requires a session (SIG1 wiring) → `TestOCSPFetch_PrivateAddressRefused`, `TestCRLFetch_BodyCapped`.
- [ ] S-4 → cap `/Type /Sig` hits at 64 and walk distance at 1 MiB; overflow-safe ByteRange; `Validate` under 10 s context → `TestParser_HostileRepetitionBounded`, `TestSignedBytes_OverflowRejected`.
- [ ] S-12 → later revisions accepted when they are valid incremental signature/DSS/timestamp updates → `TestVerify_MultiRevisionCeremonyTamperEvident`.
- [ ] S-7 → `RootCAs: nil`; non-2xx is an error before decode → `TestSwisscomClient_UsesSystemRoots`, `TestInfoCert_4xxIsError`.
- [ ] S-11 → parse documented DocuSign/Adobe shapes from fixtures → `TestDocuSignWebhook_ParsesConnectPayload`, `TestAdobeWebhook_ClientIDVerified`.
- [ ] S-6, S-8, R-17 → DSS 6.x, protobuf-java ≥ 3.25.5, grpc ≥ 1.66, logback ≥ 1.5.13; delete the sidecar `Verify` RPC; refuse the baked dev keystore unless `SIGNER_ALLOW_DEV_KEY=1`; Go sealer rejects a returned level below the requested one → `docker build services/signature-signer` passes Gradle tests; `TestSealer_RejectsDowngradedLevel`.

### Task CONN — connector

- [ ] N-1, DEC-16 → `TestCreateWebhook_RejectsAuthEvents`, `TestFanout_NeverDeliversAuthSubjects`, `TestMirror_SkipsNonSubscribable`.
- [ ] H-4, M-16 → `requireConnectorAdmin` on intake, email, event-stream, capture, webhooks (session branch), Google auth-url → `TestIntakeCreate_MemberForbidden` (and one per route group); intake roots confined under `SEDOC_INTAKE_ROOT/<tenant_id>`; ingest with the creator's real role → `TestIntakeCreate_PathOutsideRootRejected`, `TestIngest_UsesCreatorRole`.
- [ ] Google OAuth → server-side single-use nonce (connector 000002) + PKCE → `TestGoogleCallback_ReplayedStateRejected`.
- [ ] N-2 → `pluckTenant` reads `tenantid` first, UUID-validated → `TestMirror_ForwardsTenantidEnvelope`; Kong route for `/api/v1/events` goes to DEPLOY's list.
- [ ] N-3 → conflict counts as duplicate only when existing row is `ingested`; claim by rename to `.ingesting` → `TestIntake_RedropAfterFailureIngests`, `TestIntake_ConcurrentSweepsSingleIngest`.
- [ ] N-4 → claim configs with `UPDATE … RETURNING`; message lease; deliveries `FOR UPDATE SKIP LOCKED` + `in_flight` CAS → `TestEmailPoll_TwoWorkersIngestOnce`, `TestWebhookDelivery_TwoWorkersDeliverOnce`.
- [ ] N-5 → IMAP host validated like webhook URLs; `imap_use_tls=false` uses STARTTLS or refuses; generic errors to callers → `TestIMAPConfig_PrivateHostRejected`, `TestIMAPDial_NoCleartextLogin`.
- [ ] N-6 → `Lstat`, skip non-regular files; `host_path` globally unique, no prefix overlap → `TestIntake_SymlinkSkipped`, `TestIntakeCreate_OverlappingPathRejected`.
- [ ] N-7 → key `uidvalidity/uid`; per-config high-water UID, oldest-first paging → `TestIMAP_OldMessagesEventuallyIngested`, `TestIMAP_UIDValidityChangeIngestsNewMail`.
- [ ] N-8 → any attachment failure fails the message; idempotent materialisation by `(message_id, part_index)`; default filename `attachment-<n>` → `TestEmailMaterialise_AttachmentFailureRetries`.
- [ ] N-9 → size caps from `RFC822.SIZE`, `LimitReader`, MIME depth ≤ 10, intake `Stat` before read → `TestMIME_DepthBounded`, `TestIntake_OversizeQuarantined`.
- [ ] N-10 → `RequireSecret("SEDOC_LOCAL_KEK")` in connector main; no derivation from an empty seed → `TestConnectorStartup_RequiresKEK`.
- [ ] N-11 → stop issuing creds files until operator mode exists (endpoint returns 501 with explanation) → `TestEventStreamToken_CredsNotIssued`.
- [ ] N-12 → delete unwired Salesforce/Microsoft providers → workspace build.

### Task WF — workflow

- [ ] C-6 → `SessionOrAPIKey` before handlers → `TestWorkflowAPI_NoSession401WithForgedHeaders`.
- [ ] H-10 → `canManageDefinition` on update/delete; private templates 404 → `TestUpdateDefinition_MemberForbidden`, `TestDeleteDefinition_PrivateNotFound`.
- [ ] H-11 → `DeliveryPayload` shape → `TestNotifyAssignee_ConsumerAccepts` (round-trip through the notification model).
- [ ] M-12 → `ListTasksByInstance`; status `completed`/`rejected` → `TestRecall_BeforeActionSucceeds`, `TestRecall_AfterApprovalRejected`.
- [ ] M-13 → DSR purge sends gateway signature and internal key → `TestPurgeSubjectFromSearch_SendsAuthHeaders`.
- [ ] Raw-pool (`scheduled_report_schedule.go:86`, `integrations_triggers.go:63`) → `TestProdPosture_ScheduledReportsReconcile`.

### Task PLAT — audit, search, notification, policy

- [ ] H-12 → audit 000006 definers `audit_redact_subject(uuid,text)` and `audit_set_event_hash(uuid,uuid,text,text)` owned by `dms_audit_admin NOLOGIN`; `ip_address = NULL`; new signed checkpoint after re-chain → `TestProdPosture_AnonymizeSubjectRechains`.
- [ ] L-10 (audit) → partitions get no direct grants; RLS enabled on each partition in `ensure_audit_partition_range` → `TestProdPosture_PartitionDirectAccessDenied`.
- [ ] M-10 → `QueueSubscribe` with deliver group = durable → `TestConsumers_TwoInstancesBind`.
- [ ] M-14 → per-tenant `pg_advisory_xact_lock` inside the insert tx; `GetLastHash` in the same tx → `TestAppendChained_ConcurrentInstancesNoFork`.
- [ ] O-5 → audit consumer publishes to the DLQ stream when `NumDelivered >= MaxDeliver` → `TestAuditConsumer_PoisonGoesToDLQ`.
- [ ] M-11, CB-3 (policy) → HTTP grant/list/revoke and `/permissions/check` resolve folder/workspace/lifecycle/clearance context server-side → `TestHTTPGrant_WorkspaceAdminAllowed`, `TestHTTPCheck_DisposedDeniesEdit`.
- [ ] L-8 → supersede only grants the caller made or below ceiling → `TestGrant_CannotDemotePeer`.
- [ ] L-7 → CSV cells starting `= + - @ \t \r` prefixed with `'` → `TestAuditCSV_FormulaNeutralised`.
- [ ] M-13 (search) → `/internal/v1/*` requires internal key; body tenant must match → `TestPurgeSubject_RequiresInternalKey`.
- [ ] Identity ordering → search and notification: session middleware outermost, `IdentityHeadersHTTP` inside → `TestSearchAPI_NoSessionForgedHeaders401`.
- [ ] M-28 (notification) → 000004 RLS; repository in tenant tx → `TestProdPosture_PushDevicesTenantScoped`.
- [ ] Notification `LIMIT :0` and `createSavedSearch` filters → `TestListNotifications_LimitApplied`, `TestCreateSavedSearch_PersistsFilters`.
- [ ] Search idempotency table (R-3 side note) → confirm shared DB has `idempotency_keys`; if search runs on a separate DB, drop the middleware there → `TestSearchIdempotency_Store`.

### Task PY — intelligence and preview

- [ ] C-8, DEC-4 → `test_require_identity_rejects_forged_headers`, `test_require_identity_accepts_session`, `test_internal_key_allows_headers`.
- [ ] H-17 → preview API internal-only; `/regenerate` persists and uses `source_bucket/source_key` → `test_preview_public_route_requires_internal_key`, `test_regenerate_keeps_ready_manifest`.
- [ ] M-18 → delete `/redact/apply` → `test_redact_apply_route_absent`.
- [ ] M-19 → NER LLM through `llm_gateway.completion`; no env-key fallback → `test_ner_llm_uses_gateway_guards`.
- [ ] M-20 → document scope and translate check readability via the allowed-doc-ids query → `test_qa_document_scope_unreadable_denied`, `test_translate_unreadable_denied`.
- [ ] H-15 → regex/spaCy passes run directly with the real tenant; empty resolution for requested types fails the job → `test_entity_redaction_empty_resolution_fails`.
- [ ] M-21 → WeasyPrint `url_fetcher` allows only `data:`/`cid:` → `test_email_render_blocks_remote_fetch`.
- [ ] H-16 → drop `putdata`; `Image.MAX_IMAGE_PIXELS = 50_000_000`; OCR uses capture budgets; Redis attempt counter dead-letters after 3 worker losses → `test_image_bomb_rejected`, `test_ocr_giant_page_terminal`, `test_worker_lost_loop_bounded`.
- [ ] M-22 → cache only non-secret config; decrypt per call → `test_llm_config_cache_has_no_key`.
- [ ] M-23 → resolve and deny private/link-local/loopback hosts for `base_url` unless provider `vllm_local` on an allow-list; generic 503 → `test_base_url_private_rejected`.
- [ ] M-24 → preview and redaction decrypt envelopes; redacted output encrypted with a fresh DEK and the source region → `test_preview_decrypts_envelope`, `test_redaction_output_encrypted_same_region`.
- [ ] M-25 → Pillow ≥ 10.3, python-multipart ≥ 0.0.18, fastapi ≥ 0.115, uvicorn ≥ 0.30, celery ≥ 5.4, redis ≥ 5.0.8, boto3 current, weasyprint ≥ 62, transformers ≥ 4.48, torch ≥ 2.6 (report if model loading breaks) → full pytest suites in both images rebuilt from the worktree.

### Task WEB — web, extension, add-ins, mobile

- [ ] DEC-3 → client stops sending identity headers; Vite proxy strips them → `client.identityHeaders.test.ts`, `devProxyStripsIdentity.test.ts`.
- [ ] H-24 → only `/auth/me` 401 means session death → `client.401.test.ts` new case "401 on /auth/login does not navigate".
- [ ] H-25 → `useLogout` clears the query cache and upload store, then hard-navigates → `useLogout.test.tsx`.
- [ ] M-29 → FIFO duplicate prompts → `uploadStore.test.ts` "two batches both resolve".
- [ ] S-13 → sign route validates `token` search param and forwards it → `sign.token.test.tsx`; S-14 → client sends only ids (SIG1 contract) → `signatures.api.test.ts`.
- [ ] W-1 → bundled pdf.js worker module shared by both viewers → `pdfWorker.test.ts`.
- [ ] W-3, W-7 → ensurer caches promises; conflict selector disabled until implemented; 2 GiB hard cap before reading; streaming unzip with per-entry caps; errors toast → `folders.test.ts` "concurrent ensure creates one folder", `archive.test.ts` "oversize rejected".
- [ ] W-4 → `ConfirmDialog` on SCIM rotate and schedule delete → `scim.test.tsx`.
- [ ] W-9 → CSV formula guard → `analytics.csv.test.ts`.
- [ ] L-12, L-14 → relative-only login redirect; end-of-day expiry; coauthor origin check; count-up clamp → named tests per file.
- [ ] M-30 → extension state in `chrome.storage.session`; upload uses `presigned_put_url`, folder, CreateVersion → `extension/tests/background.test.js` (add vitest if absent).
- [ ] W-2, W-5 → mobile upload order and atomic queue claim; cache wiped on logout → `mobile/__tests__/queue.test.ts`.
- [ ] W-6, W-8, W-10 → `ReadItem`; templated link prefixes; 409 tenant picker → manifest validation + `auth.test.ts`.
- [ ] npm advisories → axios ≥ 1.20, `@tanstack/react-router` with seroval ≥ 1.6.3, `tailwindcss-animate` to devDependencies → `npm audit --omit=dev` has no high/critical.

### Task COLLAB — collaboration

- [ ] CB-1 → Redis relay of Yjs updates per `tenant:doc`; hydrate by applying retained rows → `twoInstances.converge.test.js`.
- [ ] CB-2 → destroy awareness and doc on eviction → `eviction.test.js`.
- [ ] CB-3 → authorize through the document service permission summary (with cookie), re-check every 60 s and on close 4403 → `authz.recheck.test.js`.
- [ ] CB-5 → dirty flag survives a failed save; SIGTERM flushes all rooms (bounded 10 s) → `flush.test.js`.
- [ ] CB-6, CB-7 → `/ws` sends the gateway signature; frames require a subscribed doc; size caps → `ws.auth.test.js`.
- [ ] CB-10 → per-clientID owner socket → `awareness.owner.test.js`.
- [ ] R-16, CB-12 → `maxPayload` 1 MiB; JSON.parse guarded; exit when `SEDOC_DATABASE_URL` unset → `startup.test.js`.

### Task MISC — sync CLI, gateway, MCP, billing, task

- [ ] H-14 → `sanitize` rejects `.`/`..`; every path checked under root → `TestApply_RefusesPathOutsideRoot`.
- [ ] R-7 → thread variables into child args → `TestExecutor_ChildArgVariable`.
- [ ] R-5, R-6 → MCP `MaxBody` 1 MiB, timeouts; SSE session bound to opening key's tenant+user; guarded send → `TestSSE_ForeignKeyCannotPost`, `TestSSE_CloseDuringSendNoPanic`.
- [ ] Billing org writes via 000111 definers; `subscriptions` lookups in tenant tx; `stripe_customer_map` RLS + definer (billing 000012) → `TestProdPosture_CreateOrg`, `TestProdPosture_StripeWebhookMapsTenant`.

### Task OPS — monitoring, ansible, terraform, license, scripts

- [ ] O-1 → PrometheusRule + Alertmanager Secret templates (new `templates/monitoring/`), `--config.expand-env`, compose `prometheus`/`alertmanager` profile → `helm template --strict` renders them.
- [ ] O-2, O-3 → `status` label; severity routes; rewrite exprs to emitted metrics; delete alerts/panels for metrics nobody emits; add alerts on `outbox_publish_failures_total`, `audit_ingest_events_total{result="error"}`, `audit_partition_ensure_total{outcome="error"}`, `audit_default_partition_rows > 0`, pod restarts → `promtool check rules` (run in a `prom/prometheus` container) plus a script asserting every alert metric is emitted somewhere.
- [ ] O-8 → `/var/backups/vaultdms` 0700 postgres, runbook filename, retention → `ansible-lint`/`--syntax-check` in a container.
- [ ] O-10 → public key from `SEDOC_LICENSE_PUBLIC_KEY`/file; dev key only in `dev`; `SEDOC_REQUIRE_LICENSE=true` in chart/ansible values → `TestLicense_ProdRejectsDevKey`.
- [ ] O-11 → migrate task, `sslmode=verify-full`, `checksum:`, ufw to app CIDR, fail on undefined vault vars → syntax check.
- [ ] O-12 → endpoint CIDRs, internal Grafana with generated password, `encrypt`/lockfile → `terraform validate` in a container.
- [ ] O-14 → per-tenant `set_config`, `EMPTY` on zero rows, real metric, `mktemp` → `shellcheck` in a container.

### Task CI — pipelines

- [ ] R-11 → `make lint`, `make security-check` (module iteration); golangci-lint version supporting Go 1.26; `.golangci.yml` `go: "1.26"`.
- [ ] R-12 → actions pinned to commit SHAs (resolve with `gh api`); Trivy loops over every built image, no `|| true`.
- [ ] R-13 → jobs for `integration/`, collaboration vitest, intelligence/preview pytest (in their images), signer Gradle (in Docker), e2e migrates via `scripts/migrate-all.sh`.
- [ ] O-13 → `scripts/check-migrations.sh`: new migrations may not add FK/CHECK without `NOT VALID` on `documents`, `document_versions`, `content_blobs`, `audit_events`, `outbox`, nor non-concurrent indexes there; wired as a PR gate → script self-test with fixture migrations.
- [ ] M-25 → pip-audit invocation fixed (no `--severity`).
- [ ] Helm → `helm template --strict` job.

### Task DEPLOY — chart, gateway, compose, build

- [ ] C-1 → Ingress routes `/api/*`, `/yjs`, `/ws` to the gateway Service and `/` to web; chart Kong config generated from `deploy/gateway/kong.yaml`; container runs `deploy/gateway/entrypoint.sh` → `helm template --strict` + a script asserting no Ingress backend other than gateway/web.
- [ ] C-2 → `SEDOC_ENVIRONMENT` from `global.environment` (default `prod`); set in compose prod override, ec2, ansible (OPS), `.env.example` → render check.
- [ ] H-18 → missing values blocks; migrate Job mirroring `migrate-all.sh` with per-service tables; seed hook removed; signer Deployment added → render check.
- [ ] H-19 → SOC2 script in a ConfigMap, no remote fetch.
- [ ] H-20/R-18 → KEK `optional: false`; ESO targets/keys aligned to what templates read.
- [ ] H-21 → one namespace `ghcr.io/aieera/sedoc/<svc>` in images.yml/release.yml (with CI), per-service contexts, `web/Dockerfile` (static build + nginx), graphql-gateway and mcp-server in `SERVICES` and both matrices.
- [ ] H-22 → Kong routes for register, accept-invite, forgot-password, reset-password, `/api/v1/events`; `request-size-limiting` (64 MB uploads, 1 MB JSON); `trusted_ips`/`real_ip_header`; remove `True-Client-IP`; remove the preview route; health route returns the scrubbed readyz.
- [ ] H-23 → `docker-compose.prod.yml` becomes an override: only gateway published, `dms_app`, `:?` required secrets, LLM deploy creds false, NATS auth; dev compose binds data stores and backend ports to `127.0.0.1` (gateway and Vite unaffected).
- [ ] R-14 → Makefile special-cases document like `migrate-all.sh`; `migrate-force` takes the per-service table.
- [ ] R-15 → restricted securityContext on gateway, jobs, cronjobs; `minio/mc` pinned by digest.
- [ ] R-16 (chart) → collaboration gets the common env.
- [ ] R-19, M-32 → instance label on every pod template; NetworkPolicies for data stores; Redis auth enabled and password plumbed into every service URL.
- [ ] O-4 → NATS user/password auth in compose and Helm, `SEDOC_NATS_URL` carries credentials, 4222 not published.
- [ ] O-6 → migrations create `NOLOGIN` roles; init-db/Helm/Ansible set LOGIN and password from secrets.
- [ ] O-9 → chart consumes overlay keys (`env` as map, egress NetworkPolicy, region env); overlay file updated to match.
- [ ] DEC-1 → Helm and Ansible runtime role `dms_app`.

### Task DEPS — dependency and toolchain bumps

- [ ] Go modules: pgx ≥ 5.9.2, go-redis ≥ 9.6.3, x/net ≥ 0.55, x/text ≥ 0.39, grpc ≥ 1.83.1, goxmldsig ≥ 1.6.0, go-jose v3 ≥ 3.0.5, expr ≥ 1.17.7, opa ≥ 0.68.0, otel exporters ≥ 1.45.0, containerd ≥ 1.7.36 where used; `toolchain go1.26.6`; Go Dockerfiles on `golang:1.26-alpine` → `govulncheck` clean on called code in every module; full `make test`.

## 6. Final verification (coordinator)

- [ ] All branches merged; `make build`, `make test`, `make lint` green.
- [ ] `scripts/prod-posture-lane.sh` green with an empty allow-list.
- [ ] Web `npm run build`, `npm test`, `npm run lint`; collaboration `npm test`; Python suites in rebuilt images.
- [ ] `helm template --strict deploy/helm/sedoc` clean; `docker compose -f docker-compose.yml -f docker-compose.prod.yml config` clean.
- [ ] govulncheck and `npm audit --omit=dev` clean of high/critical.
- [ ] Whole-branch review by Octopus personas (code-reviewer, security-auditor, deployment-engineer) on `fix/review-2026-10` vs `neu-ui`; fix confirmed findings.
- [ ] Update the review reports with a status column (fixed / refuted / deferred with reason).
