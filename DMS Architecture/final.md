VAULTDMS
Enterprise Completion Prompt
Zero New Features · Finish-What-Exists Protocol
Baseline: 2026-04-17 (post Waves 1–4)
Scope: Waves 5–14 — Pilot-ready → Production-ready → Feature-complete
Audience: AI coding agent (Claude Code / Cursor / Copilot) + engineering team
Guardrail: NO new features — only finish, wire, harden, document, and test what exists
CONFIDENTIAL · ENGINEERING EXECUTION SPEC
 
0. How to Use This Document
This document is an executable completion plan — not a feature roadmap. It contains everything an AI coding agent and a human engineering team need to take SeDoc from its current ~60% functional state to enterprise production-ready, without adding a single new feature.
0.1 Operating model
•	Waves 5 through 14 are ordered by dependency. Wave 5 (Event Pipeline) unblocks 70% of the downstream system and MUST complete before any other wave starts integration testing.
•	Every wave contains: Objective, Preconditions, Prompts, Definition of Done (DoD), Acceptance Tests, and Rollback Plan.
•	Every prompt is self-contained and designed to be pasted into Claude Code, Cursor, or a Claude session with a working tree mounted.
•	No wave is considered complete until all DoD items are green AND the acceptance tests pass in CI.
0.2 Prompt format convention
Prompts are written in a strict shape so they can be issued to an autonomous agent without ambiguity:
ROLE:        Senior <domain> engineer at a FAANG-level org.
CONTEXT:     <links to files, services, schemas, existing patterns>
TASK:        <single, atomic deliverable>
CONSTRAINTS: <what MUST NOT be changed, touched, or re-scoped>
DELIVERABLES: <files, tests, migrations, dashboards to produce>
DoD:         <how we know it is done>
TESTS:       <how a reviewer can verify locally and in CI>
0.3 Non-negotiable engineering principles
•	No silent failures. Every consumer and every publisher must emit structured logs + metrics on both success and error paths.
•	No direct NATS publishes from request handlers. All events flow through the transactional outbox.
•	No context.Background() in production code paths. Every handler, consumer, and worker must derive a context with timeout from its parent.
•	No PII in logs. Redact email, phone, Emirates ID, SSN, and payload bodies at the logging middleware layer.
•	No secrets in source. Every secret loaded from KMS/Vault at boot, reloaded on SIGHUP.
•	No unbounded channels, goroutines, or retries. Everything is bounded and has a dead-letter path.
•	No feature work. If a prompt reveals that a feature is missing beyond what is listed in the State of the Project, DOCUMENT it in the Out-of-Scope ledger and stop. Do not build it.
 
1. Mission, Guardrails, and Definition of Done
1.1 Mission
Take SeDoc from 'skeleton-strong, muscle-thin' to a pilot-ready, then production-ready, enterprise Document Management System — by completing the unfinished 40% of the 30-prompt plan, not by extending the 30-prompt plan.
1.2 Three milestone gates
Gate	Name	Scope	Exit criteria (abbreviated)
G1	Pilot-Ready	3–5 customers, single region, no custom workflows, no eSign	Event pipeline end-to-end, OCR + search live, security fixes landed, audit export works
G2	Production-Ready SaaS	Multi-region, Temporal workflows, retention + legal hold + GDPR live	All waves through 12 complete, chaos + load pass, DR runbook rehearsed
G3	Feature-Complete	Signatures, mobile, desktop sync, full integrations, air-gapped image	All 14 services have READMEs, full Helm, OpenAPI 100%, control plane GA
1.3 Hard guardrails (non-negotiable)
DO-NOT-TOUCH LIST
Do not add any feature outside the 30-prompt blueprint in the State of the Project.
Do not refactor working services (auth, policy, billing, audit) unless a listed defect explicitly requires it.
Do not rewrite the Go module layout, the monorepo structure, or the RLS role model (dms_app).
Do not change database column types without a migration + backfill + verification script.
Do not introduce new third-party dependencies without a written ADR (docs/adr/NNN-<title>.md).
1.4 Global Definition of Done (applies to every wave)
1.	Code compiles and passes go vet, staticcheck, and golangci-lint with no new warnings.
2.	Unit tests: ≥ 75% line coverage on every NEW file; ≥ 60% on any touched file.
3.	Integration test harness in CI exercises the wave's end-to-end flow.
4.	OpenAPI spec (docs/api/openapi.yaml) updated for every new/changed HTTP route.
5.	Prometheus metrics added: one *_total counter, one *_duration_seconds histogram, one *_errors_total counter per new code path.
6.	Structured logs (zerolog) at INFO on success, ERROR on failure, with tenant_id, user_id, trace_id, correlation_id.
7.	Grafana dashboard JSON updated in ops/grafana/dashboards/.
8.	OTEL spans emitted at every service boundary.
9.	All new queries run through the dms_app role and are tested against an RLS enforcement test.
10.	Every new NATS subject is declared in the stream bootstrap and has a DLQ subject defined.
11.	Every new database table has an index-plan comment documenting its access patterns.
12.	A rollback script (migrations/NNNN_down.sql, or feature flag) exists for every change.
13.	Runbook entry added to docs/runbooks/<wave>-<topic>.md with 'what can go wrong' + 'how to fix'.
 
2. Current State — What You Are Walking Into
This section is a snapshot the agent can paste verbatim into any new session so it never loses context.
2.1 Services and their truth
Service	Status	Reality on disk
auth	✅ Live	Password, TOTP, SAML/OIDC, SCIM, API keys, /auth/me. DEFECT: math/rand serial in SAML signer.
policy	✅ Live	OPA embedded, Redis cache, < 5 ms perm checks. Emits dms.user.* with no matching stream.
document	✅ Live	CRUD + versioning + workspace + restore. REST proxy for storage was added Wave 2b.
storage	🟡 Blocker	gRPC OK, upload + chunk OK. MUST publish dms.version.uploaded.v1 in CompleteUpload. Shared KEK.
search	✅ Live	OpenSearch indexer, hybrid skeleton, saved searches. No consumer for semantic result merge yet.
intelligence	🟡	Chunking + embedding code. Qdrant upsert is stubbed. RAG endpoint reachable, not integration-tested.
preview	🟡	Python service runs, NATS consumer wired. Frontend viewer still uses raw PDF, not rendered pages.
workflow	❌	Temporal connection code only. ZERO workflow definitions. 'My Tasks' has nothing to surface.
notification	✅	Rate-limited email + web push working.
audit	🟡	Hash chain + GET + CSV export. verify-integrity endpoint NOT exposed.
signature	❌	Skeleton only. No PAdES library integrated, no PDF signing.
collaboration	🟡	Node service boots. Auth middleware + comment event handlers are incomplete.
connector	🟡	Webhook delivery logic done. OAuth for M365 / SFDC scaffolded, not connected.
billing	🟡	Stripe webhooks skeleton. usage_records cron errors every minute (known).
2.2 The single biggest blocker
CRITICAL: dms.version.uploaded.v1 has no publisher
Storage service completes upload successfully but emits no event.
Consequence: OCR consumer never fires → classification never fires → embedding never fires → semantic search is empty → RAG has nothing to retrieve → 'upload → search' is broken end-to-end.
Fix size: a few hours of code. Ripple: unblocks prompts 9, 10, 11, 12, 14, 16.
This fix is the first task in Wave 5. Do not start any other wave until it is merged.
2.3 Silent data-loss condition
NATS stream subjects do not cover outbox emissions
auth, policy, and billing emit dms.user.* events through the outbox worker.
There is no JetStream stream bound to dms.user.> — the broker accepts the publish, routes to nothing, and the message is lost.
Every user creation, role change, and API-key rotation is silently dropped from the event bus.
Fix: add stream USER_EVENTS with subjects dms.user.>, retention Limits, max_age 168h, replicas 3. See Wave 5.2.
2.4 Security debt to clear before pilot
•	Shared KEK vaultdms-storage-default across all tenants → per-tenant KEK lookup via KMS alias.
•	Session token stored in localStorage → httpOnly, Secure, SameSite=Strict cookie + CSRF double-submit token.
•	math/rand used for X.509 serial in services/auth/internal/sso/saml.go → crypto/rand with 128-bit serial.
•	14 NATS handlers use context.Background() → derive from parent with 30s timeout.
•	4 services publish NATS directly from handlers → move to outbox pattern.
 
3. Enterprise Execution Principles
Every prompt in this document inherits these. They exist so that the output of an AI agent is indistinguishable in quality from a senior engineer at Stripe, Datadog, or Cloudflare.
3.1 Reliability
•	Idempotency keys on every mutating POST. Key = tenant_id + client-generated UUID, stored for 24h.
•	At-least-once delivery with deduplication. Every consumer checks event_id against a processed_events table scoped to the consumer.
•	Timeouts everywhere. Default 30s for handlers, 5s for DB, 10s for downstream HTTP, 2s for Redis.
•	Retries with jitter. Exponential backoff, cap 5 attempts, always jittered ±20%.
•	Circuit breakers on all outbound HTTP. Threshold 50% error rate over 30s, half-open every 10s.
•	Dead-letter queues on every NATS consumer, visible in a dedicated Grafana panel.
3.2 Security
•	Zero-trust between services. Every internal call carries a signed SPIFFE-style workload identity header.
•	Every HTTP handler passes through: tenant resolver → auth middleware → policy check → rate limiter → idempotency → handler.
•	Every database connection opens as dms_app role so RLS is always enforced; super-user is reserved for migrations only.
•	Per-tenant KEK wrapped by a KMS-resident CMK; data keys (DEK) envelope-encrypted per object.
•	Secrets rotation: KEK every 90 days, JWT signing keys every 30, API internal tokens every 7. All automated.
•	CSP headers, HSTS preload, X-Frame-Options DENY, SameSite=Strict on every cookie.
•	Every request is OpenTelemetry-traced with tenant_id, user_id, workspace_id as span attributes — never as log fields alone.
3.3 Multi-tenancy
•	Every table that holds tenant data has a tenant_id column, an RLS policy using current_setting('app.tenant_id'), and a composite index starting with tenant_id.
•	Every service sets app.tenant_id at the start of every transaction via SET LOCAL, and asserts it is non-empty before any query.
•	Cross-tenant leaks are treated as P0. Add a chaos test that attempts to read another tenant's row from every endpoint.
3.4 Observability
•	The Three Pillars are mandatory for every new code path: metrics (Prometheus), logs (zerolog JSON), traces (OTEL).
•	RED method for every HTTP endpoint: Rate, Errors, Duration — histogram with buckets at 10, 25, 50, 100, 250, 500, 1000, 2500, 5000 ms.
•	USE method for every resource: Utilization, Saturation, Errors — queue depth, connection pool usage, CPU, memory.
•	Every alert has a runbook link in its annotation; no unrunbooked alert reaches production.
•	SLI dashboards split customer-facing SLIs from internal SLIs so noise does not burn the error budget.
3.5 Documentation
•	Every service has a README.md with: purpose, dependencies, local-run, env vars, metrics list, key endpoints, architecture diagram path, on-call runbook link.
•	Every ADR lives in docs/adr/NNNN-<slug>.md using the Michael Nygard format: Context / Decision / Consequences / Alternatives considered.
•	Every API change updates docs/api/openapi.yaml in the same PR; CI fails if a route is added without a spec entry.
•	Every runbook ends with a 'last rehearsed' date and the name of the on-call who did the rehearsal.
3.6 Code quality gates
Gate	Rule
Lint (Go)	golangci-lint run --timeout 5m; must pass with zero issues on touched files.
Lint (TS)	eslint --max-warnings 0, typescript strict mode, no any without a // eslint-disable comment with a reason.
Lint (Python)	ruff check + mypy --strict on touched files.
Format	gofmt -s, prettier, ruff format; enforced by pre-commit + CI.
SAST	semgrep CI rules, gosec for Go, bandit for Python, npm audit --audit-level=moderate.
Secret scan	trufflehog filesystem on every PR; gitleaks pre-commit.
Dep scan	trivy image scan on every container; fail on CRITICAL, warn on HIGH.
License	go-licenses + license-checker; block GPL, AGPL in production deps.
Coverage	≥ 75% on new files, ≥ 60% on touched files; trend must not regress > 2%.
Mutation	go-mutesting on critical packages (auth, policy, storage); ≥ 70% mutation score.
 
4. Wave 5 — Event Pipeline End-to-End (CRITICAL)
This wave is the hinge on which the entire system turns. Every other wave assumes it is done.
4.1 Objective
Make upload → OCR → classification → NER → embedding → index → semantic search → RAG a fully wired, observable, retriable, dead-letterable pipeline — with zero silent failures.
4.2 Preconditions
•	go.work is clean, docker-compose up -d brings postgres, opensearch, qdrant, redis, nats, temporal, minio all healthy.
•	All Wave 1–4 merges are on main.
4.3 Prompt 5.1 — Publish dms.version.uploaded.v1
ROLE:        Senior Go backend engineer.
CONTEXT:     services/storage/internal/handlers/upload.go. The CompleteUpload RPC
             currently writes the final object manifest to Postgres and returns OK,
             but never publishes a domain event. Downstream consumers (OCR, classify,
             embed) are blocked on dms.version.uploaded.v1.
             The outbox pattern is already implemented in pkg/outbox/outbox.go.
             Look at services/document/internal/service/document.go (CreateDocument)
             for a reference implementation that correctly uses the outbox.
TASK:        In the SAME SQL transaction that persists the version row,
             insert a row into outbox_events with:
               subject=dms.version.uploaded.v1
               payload=VersionUploadedV1 protobuf (or JSON if proto not yet defined)
               headers={tenant_id, trace_id, correlation_id, idempotency_key}
             Define the VersionUploadedV1 schema in pkg/events/v1/version.proto
             with: event_id, tenant_id, document_id, version_id, storage_uri,
             mime_type, size_bytes, sha256, uploaded_by_user_id, uploaded_at.
CONSTRAINTS: MUST be in the same transaction. MUST NOT call nats.Publish directly.
             MUST be idempotent on event_id.
DELIVERABLES: proto file, generated Go stubs, modified upload.go, unit test,
             integration test that asserts the row lands in outbox_events.
DoD:         Uploading a file produces exactly one outbox row with the right subject.
             Outbox worker then drains it to NATS. Metric storage_events_published_total
             increments with label subject="dms.version.uploaded.v1".
TESTS:       make test-integration-storage uploads a 1 MB PDF and asserts:
             - outbox row exists
             - NATS stream DOC_EVENTS has the message
             - message headers contain tenant_id and trace_id
4.4 Prompt 5.2 — JetStream stream topology
ROLE:        Senior platform engineer.
CONTEXT:     NATS JetStream is used as the event bus. Today only DOC_EVENTS stream
             exists, bound to dms.document.>, dms.version.>. Auth/policy/billing
             publish dms.user.*, dms.policy.*, dms.billing.* — and those messages
             are silently dropped because no stream is bound.
             Stream bootstrap lives in pkg/nats/bootstrap.go.
TASK:        Declare the following streams idempotently at service start
             (any service may bootstrap; use a distributed lock in Redis key
             'nats:bootstrap:lock' with 60s TTL to avoid races):
               DOC_EVENTS      dms.document.>, dms.version.>, dms.workspace.>
               USER_EVENTS     dms.user.>, dms.session.>, dms.apikey.>
               POLICY_EVENTS   dms.policy.>, dms.permission.>
               BILLING_EVENTS  dms.billing.>, dms.subscription.>, dms.usage.>
               AUDIT_EVENTS    dms.audit.>
               SEARCH_EVENTS   dms.search.>
               WORKFLOW_EVENTS dms.workflow.>, dms.task.>
               INTEL_EVENTS    dms.ocr.>, dms.classify.>, dms.embed.>
             Each with: Retention=Limits, MaxAge=168h, Replicas=3, Storage=File,
             Discard=Old, Duplicates=2m (for idempotency window),
             plus a matching DLQ stream <NAME>_DLQ with 720h retention.
CONSTRAINTS: DO NOT delete or recreate existing streams — use UpdateStream when the
             subjects list changes and the stream already exists.
DELIVERABLES: pkg/nats/bootstrap.go updated, unit tests, a CLI command
             `dms-admin nats bootstrap --dry-run` that prints a diff.
DoD:         Running any service from scratch produces all 8 streams + 8 DLQ streams.
             A message published to dms.user.created.v1 lands in USER_EVENTS.
             A replay via `dms-admin nats replay --stream USER_EVENTS` works.
TESTS:       Integration test publishes one message to each of the 8 primary subjects
             and asserts it is retained and consumable.
4.5 Prompt 5.3 — OCR consumer hardening
ROLE:        Senior Python backend engineer (FastAPI + Surya/PaddleOCR).
CONTEXT:     services/preview/workers/ocr_consumer.py subscribes to
             dms.version.uploaded.v1. It currently: downloads the object, runs OCR,
             writes the text to S3, and emits dms.ocr.completed.v1.
TASK:        Harden the consumer so it is production-grade:
             1. At-least-once: ack only after dms.ocr.completed.v1 is durably written.
             2. Deduplication: check event_id against ocr_processed_events (tenant_id,
                event_id, processed_at) with a 14-day TTL index.
             3. Per-tenant concurrency cap: semaphore keyed by tenant_id, default 8.
             4. Timeout: 90s per page, abort whole document at 30 minutes.
             5. Retries: 3 attempts with jittered exponential backoff (base 5s).
             6. DLQ: on final failure, publish to dms.ocr.failed.v1 with full error
                context (stack, attempt count, last 4KB of stdout/stderr) and move
                the original message to OCR_DLQ.
             7. Metrics: ocr_documents_total{status}, ocr_pages_total, ocr_duration
                _seconds histogram, ocr_queue_depth gauge, ocr_dlq_total counter.
             8. Traces: one root span per document, child span per page.
             9. Structured logs with tenant_id, document_id, version_id, page_count.
CONSTRAINTS: DO NOT swap the OCR engine. DO NOT change the output schema.
DELIVERABLES: refactored ocr_consumer.py, new ocr_processed_events migration,
             Grafana panel JSON, Prometheus alert rules, runbook
             docs/runbooks/05-ocr-pipeline.md.
DoD:         Uploading a 30-page PDF produces one dms.ocr.completed.v1 event within
             30 seconds p95. Killing the consumer mid-run does not duplicate output
             on restart. A malformed PDF ends up in OCR_DLQ with full context.
TESTS:       Chaos test kills the worker pod at 50% page progress on 100 docs;
             no duplicate ocr_completed events, no lost docs, no blocked queue.
4.6 Prompt 5.4 — Classification + NER consumer
ROLE:        Senior ML platform engineer.
CONTEXT:     services/intelligence/workers/classify_consumer.py consumes
             dms.ocr.completed.v1 and should produce dms.classify.completed.v1 +
             dms.ner.completed.v1.
TASK:        Apply the same hardening profile as Prompt 5.3 (dedupe, cap, timeout,
             retry, DLQ, RED metrics, traces, logs). Additionally:
             - Write classifications to document_classifications(document_id,
               category_id, confidence, model_version, classified_at).
             - Write entities to document_entities(document_id, entity_type, value,
               start_offset, end_offset, confidence).
             - Emit dms.classify.completed.v1 with (document_id, top_3_categories,
               confidences, model_version).
             - Emit dms.ner.completed.v1 similarly.
CONSTRAINTS: Do not change category taxonomy. Do not call external LLMs — use the
             on-box classifier already configured in configs/intelligence.yaml.
DELIVERABLES: hardened consumer, two migrations, dashboard panels, alert rules,
             runbook docs/runbooks/05-classification-pipeline.md.
DoD:         One upload produces exactly one classification row and N entity rows.
             Re-running the consumer on the same event_id is a no-op.
TESTS:       Integration test uploads 10 known docs; asserts classifications match
             a golden file within ±5% confidence tolerance.
4.7 Prompt 5.5 — Embedding + Qdrant upsert
ROLE:        Senior ML platform + vector DB engineer.
CONTEXT:     services/intelligence/workers/embed_consumer.py consumes
             dms.classify.completed.v1 (or dms.ocr.completed.v1 depending on routing)
             and must: chunk the OCR text, embed each chunk with the configured
             model, and upsert to Qdrant collection vaultdms_chunks.
             Today the upsert is STUBBED.
TASK:        Implement the full upsert path:
             - Chunk with sliding-window of 512 tokens, 64 overlap.
             - Embed in batches of 32 with concurrency cap 4.
             - Qdrant point id = deterministic uuid5(document_id, chunk_index).
             - Payload: tenant_id, document_id, version_id, chunk_index,
               start_char, end_char, classification_top_1, page_number.
             - Use separate Qdrant collection per tenant_id ONLY when the tenant
               opts into 'isolated-vector-store'; otherwise use payload-scoped search
               with mandatory tenant_id filter.
             - Emit dms.embed.completed.v1 (document_id, chunk_count, model_version).
             - Apply the same hardening profile as 5.3.
CONSTRAINTS: Must use the existing embedding client in pkg/embed. Must not introduce
             a new vector store.
DELIVERABLES: implemented upsert, Qdrant index-plan ADR, runbook, dashboard panels.
DoD:         After a single upload, a semantic search for a phrase inside the doc
             returns at least one result from that document, with tenant_id filter
             applied. A cross-tenant semantic search returns ZERO results.
TESTS:       Cross-tenant isolation test MUST pass: 2 tenants upload docs with the
             same content; tenant A's search never returns tenant B's results.
4.8 Wave 5 Acceptance Tests
14.	End-to-end: curl upload → wait 60s → curl search → result appears with matching snippet and highlight.
15.	Cross-tenant isolation: 2 tenants upload identical docs → each tenant's search returns only their own results.
16.	Idempotency: re-deliver the same dms.version.uploaded.v1 10 times → exactly one OCR row, one classify row, one embed row.
17.	Chaos: kill OCR worker mid-run → restart → no duplicates, no loss, DLQ empty after replay.
18.	Observability: Grafana 'Event Pipeline' dashboard shows every hop, with p50/p95/p99 latencies and error rates.
 
5. Wave 6 — Security Hardening (CRITICAL)
Pilot cannot ship until these five items are fixed. They are each small in scope but catastrophic in consequence.
5.1 Prompt 6.1 — Per-tenant KEK
ROLE:        Senior cryptography + platform engineer.
CONTEXT:     Today every encrypted object is wrapped by a single shared KEK named
             vaultdms-storage-default. This is a blast-radius disaster.
             KMS/Vault is already integrated via pkg/kms/client.go, with support for
             AWS KMS, GCP KMS, Azure Key Vault, and HashiCorp Vault Transit.
TASK:        Implement per-tenant KEK with these semantics:
             - On tenant creation, create a KMS CMK alias vaultdms/tenant/<tenant_id>.
             - Every object encryption generates a fresh DEK, encrypts the object
               with the DEK (AES-256-GCM), wraps the DEK with the per-tenant KEK.
             - Store wrapped_dek, kek_version, key_alias on the object row.
             - On decrypt, look up by alias, unwrap DEK, decrypt object.
             - Support KEK rotation: `dms-admin kms rotate --tenant <id>` re-wraps all
               DEKs for that tenant with a new KEK version (online, chunked,
               resumable).
             - Existing objects under the shared KEK must be migrated lazily on next
               access, with a background job that can force-migrate in the
               background.
CONSTRAINTS: Do not break any existing download URL. Do not leak old ciphertext.
             Never log a DEK or KEK, ever.
DELIVERABLES: pkg/kms/tenant_kek.go, migration to add wrapped_dek, kek_version,
             key_alias columns, backfill script, admin CLI subcommand, runbook
             docs/runbooks/06-key-management.md including rotation drill.
DoD:         Two tenants uploading identical plaintexts produce ciphertexts
             encrypted under different KEKs. Deleting a tenant purges their CMK
             (24h scheduled deletion with cancellation window).
TESTS:       Test that reading tenant A's object while authenticated as tenant B
             fails at the KMS layer even if RLS is bypassed in a unit test.
5.2 Prompt 6.2 — Session cookies, not localStorage
ROLE:        Senior full-stack security engineer.
CONTEXT:     Session tokens are stored in localStorage in apps/web/src/store/auth.ts.
             This is XSS-exfiltratable and a pilot blocker.
TASK:        Move to httpOnly, Secure, SameSite=Strict cookies with a CSRF
             double-submit token:
             - Backend /auth/login sets cookie dms_session (httpOnly, Secure,
               SameSite=Strict, Path=/, Max-Age=tied to session TTL).
             - Backend also sets dms_csrf (readable by JS, NOT httpOnly).
             - Every non-GET request from the frontend sends header X-CSRF-Token
               matching the cookie value.
             - Backend middleware rejects any mutating request where header !=
               cookie OR where cookie is missing.
             - Refresh token endpoint /auth/refresh uses a separate httpOnly cookie
               dms_refresh on path /auth with a long TTL.
             - Logout clears all three cookies with Max-Age=0.
             - Remove every localStorage.getItem('token') / setItem('token').
CONSTRAINTS: No API shape change. Backward compat for 30 days: accept Bearer token
             AND cookie, but prefer cookie. Log a deprecation warning when Bearer
             is used.
DELIVERABLES: middleware change, Zustand auth store rewrite, e2e test, OWASP
             cheat-sheet alignment doc in docs/security/session-cookies.md.
DoD:         Opening devtools → Application → Local Storage shows NO auth token.
             XSS payload cannot read the session cookie. CSRF attack blocked.
TESTS:       Playwright e2e simulates an XSS that tries to read the cookie; fails.
             CSRF test from a different origin is blocked with 403.
5.3 Prompt 6.3 — crypto/rand for SAML serial
ROLE:        Senior Go security engineer.
CONTEXT:     services/auth/internal/sso/saml.go uses math/rand to mint X.509 serials
             for SAML signing certs. Predictable serials + crafted inputs = downgrade
             attack surface.
TASK:        Replace with crypto/rand, 128-bit serial, big.Int, re-verify the cert
             template generation path doesn't leak the private key. Add a CI
             grep-based guard that fails if math/rand is imported anywhere in
             services/auth, services/signature, services/policy.
CONSTRAINTS: Keep existing IdP trust chain valid. Do not re-issue certs for existing
             customers without a migration announcement.
DELIVERABLES: patched saml.go, linter rule, unit test that asserts serial bit length
             >= 127 bits and uniqueness across 10k draws.
DoD:         go test ./services/auth/... passes. gosec reports no weak-random finding.
TESTS:       Property test: 10,000 serials generated; all unique, all >= 127 bits.
5.4 Prompt 6.4 — Context propagation audit
ROLE:        Senior Go platform engineer.
CONTEXT:     14 NATS handlers in the codebase use context.Background() instead of
             a timeout-derived context. This breaks tracing, prevents graceful
             shutdown, and allows handlers to hang indefinitely.
TASK:        Sweep all consumers. For each one:
             - Start with ctx := otel.Tracer(...).Start(parentCtx, 'handler')
               where parentCtx carries trace_id from NATS headers.
             - Derive ctx, cancel := context.WithTimeout(ctx, 30*time.Second).
             - Pass ctx to every DB, HTTP, NATS call in the handler body.
             - defer cancel().
             - On shutdown, the service must drain in-flight handlers within 30s or
               force-cancel.
             Add a static analysis rule (custom go-ruleguard or grep in CI) that
             fails the build if context.Background() appears inside any function
             named *Handler, *Consumer, *Worker.
CONSTRAINTS: Do not change public signatures.
DELIVERABLES: sweep PR, CI rule, dashboard panel showing handler duration vs.
             timeout, alert when timeout rate > 1%.
DoD:         Killing the service via SIGTERM drains cleanly in <30s in chaos test.
TESTS:       Integration test sends 10k messages; SIGTERM mid-run; all in-flight
             messages either complete or DLQ, none leak.
5.5 Prompt 6.5 — Outbox-only publishing
ROLE:        Senior distributed systems engineer.
CONTEXT:     4 services publish directly to NATS from request handlers instead of
             going through the outbox. This means: on a crash between DB commit and
             NATS publish, the event is lost forever.
TASK:        Identify every direct nats.Publish / js.Publish call. For each:
             - Replace with outbox.Append(ctx, tx, subject, payload, headers).
             - The outbox worker (already running) will drain it.
             - Add a lint rule that forbids direct js.Publish outside pkg/outbox.
CONSTRAINTS: Every replacement must be in the SAME transaction as the state change
             it describes.
DELIVERABLES: PR with replacements + ADR docs/adr/0021-outbox-only-publishing.md.
DoD:         grep -r 'js.Publish(' services/ returns zero hits outside pkg/outbox.
TESTS:       Crash test: kill the service between DB commit and outbox drain; after
             restart, the event still lands on NATS exactly once.
 
6. Wave 7 — Workflow Engine (Temporal)
Temporal is wired but runs nothing. This wave ships the smallest set of workflows that unblocks 'My Tasks', review routing, and approval — nothing more.
6.1 Objective
Define, register, and serve four core workflows that every DMS needs: Document Review, Approval Chain, Retention Disposition, and Signature Orchestration (stub only — actual signing lands in Wave 9).
6.2 Prompt 7.1 — Workflow package skeleton
ROLE:        Senior Temporal + Go engineer.
CONTEXT:     services/workflow has connection code only. No workflows, no activities,
             no worker.
TASK:        Build the package structure:
               services/workflow/internal/workflows/review.go
               services/workflow/internal/workflows/approval.go
               services/workflow/internal/workflows/retention.go
               services/workflow/internal/workflows/signature_stub.go
               services/workflow/internal/activities/{db,nats,notify,policy}.go
               services/workflow/cmd/worker/main.go
             Register all workflows + activities on taskqueue vaultdms-default.
             Namespace: one per tenant (vaultdms-<tenant_id>) OR shared namespace
             with search attribute tenant_id — pick the latter per ADR 0022.
CONSTRAINTS: Use Temporal Go SDK v1.26+. No Temporal Cloud-specific APIs — the
             customer may self-host.
DELIVERABLES: package tree, ADR 0022-temporal-namespace-strategy.md, worker binary,
             Helm template for temporal-worker deployment.
DoD:         `temporal workflow list --namespace vaultdms` shows the worker polling.
6.3 Prompt 7.2 — Document Review workflow
ROLE:        Senior workflow engineer.
CONTEXT:     Review workflow in services/workflow/internal/workflows/review.go.
             Business rule: a document can be submitted for review by its author;
             one or more reviewers are notified; each has 72h to approve or reject;
             on unanimous approval, emit dms.review.approved.v1; on any rejection,
             emit dms.review.rejected.v1 with comments.
TASK:        Implement as a Temporal workflow with:
             - Activities: LoadReviewers, NotifyReviewer, WaitForDecision (signal),
               RecordDecision, EmitOutcome.
             - 72h timer per reviewer with auto-escalation signal.
             - Query handlers: GetStatus, GetPendingReviewers.
             - Signals: ReviewerDecided(reviewer_id, decision, comment),
               ReviewerReassigned(old_id, new_id, reason).
             - Workflow ID: review-<tenant_id>-<document_id>-<version_id>.
             - Retry policy on activities: max 3, backoff coefficient 2, max 30s.
CONSTRAINTS: Workflow code must be deterministic. No time.Now, no rand, no direct DB.
             All I/O goes through activities.
DELIVERABLES: workflow file, activities, replay test (Temporal replay test harness),
             runbook, Grafana panel.
DoD:         Starting a review via POST /workflow/reviews creates a workflow that
             appears in `temporal workflow list`. Completing all reviewer decisions
             emits dms.review.approved.v1 on the NATS bus.
TESTS:       Replay test using exported event history — workflow must be replayable
             without non-determinism panics.
6.4 Prompt 7.3 — Approval Chain workflow
ROLE:        Senior workflow engineer.
CONTEXT:     Same file layout. Approval chain is sequential (unlike Review which is
             parallel). Each approver must act before the next is notified.
TASK:        Same hardening profile as 7.2. Additionally support:
             - Delegate signal: an approver can delegate to another user with reason.
             - Skip-level policy: if approver is on leave (checked via policy
               service), auto-skip with recorded reason.
             - Any rejection short-circuits the chain.
DoD:         An approval started with 5 approvers completes when all 5 approve;
             completes with 'rejected' on the first reject; delegation works.
TESTS:       Replay test + chaos test that kills the worker between approvals 2 and 3;
             on restart, workflow continues correctly without re-notifying approver 1.
6.5 Prompt 7.4 — My Tasks endpoint + UI wire-up
ROLE:        Senior full-stack engineer.
CONTEXT:     The frontend has a 'My Tasks' page that currently renders from an
             empty stub. Tasks come from workflows.
TASK:        - Add REST endpoint GET /workflow/tasks?status=pending&assignee=me
               that reads from a task_inbox table populated by workflow activities.
             - task_inbox columns: id, tenant_id, assignee_user_id, workflow_id,
               workflow_type, entity_type, entity_id, title, due_at, created_at,
               completed_at, decision, comment.
             - Every NotifyReviewer / NotifyApprover activity INSERTs a row.
             - Every decision signal UPDATEs the row.
             - Wire the existing 'My Tasks' React page to this endpoint.
             - Add 'Approve' / 'Reject' buttons that POST a decision signal.
CONSTRAINTS: No new feature — this is wiring only.
DELIVERABLES: migration, endpoint, page wiring, e2e test.
DoD:         A reviewer logs in, sees their pending tasks, clicks approve, the
             workflow advances, the task moves to 'completed'.
6.6 Prompt 7.5 — ReactFlow designer (prompt 17 recovery)
ROLE:        Senior React engineer.
CONTEXT:     Prompt 17 (workflow UI designer) from the original 30-prompt plan was
             never started. The project scope LIMITS this to a FINISH-what-exists
             deliverable, so this designer only needs to: render the four built-in
             workflows read-only, show their state, and let an admin reassign
             pending steps. No custom workflow authoring.
TASK:        Add apps/web/src/pages/admin/workflows/WorkflowDesigner.tsx using
             reactflow v11. For each of the four workflows, hard-code a node graph
             template. When viewing a running workflow, fetch its status from the
             workflow service and color nodes by state (pending/active/complete/
             rejected). Provide a 'reassign' modal on pending nodes that calls the
             workflow signal endpoint.
CONSTRAINTS: Read-only + reassign. No custom workflow authoring. No drag-to-create.
DELIVERABLES: page, tests, screenshot in docs.
DoD:         Admin sees live status of every running workflow; can reassign.
 
7. Wave 8 — Compliance Plumbing
Retention, legal hold, GDPR — all have schemas but no endpoints or cron jobs. This wave makes them real.
7.1 Prompt 8.1 — Retention cron
ROLE:        Senior backend engineer.
CONTEXT:     retention_policies, document_retention_bindings tables exist. No cron
             runs. Documents never transition to archived / disposed.
TASK:        Build services/workflow/internal/workflows/retention.go as a Temporal
             cron-schedule workflow (runs daily at 03:00 UTC per tenant):
             - Find all documents whose retention clock expired.
             - For each: if a legal_hold exists, skip (emit dms.retention.held.v1).
             - Otherwise transition to archived, after 30 days to dispose candidate,
               after dispose-approval to disposed (soft delete + ciphertext shred).
             - Every transition logs an audit event and emits a domain event.
CONSTRAINTS: Disposition is IRREVERSIBLE — requires a two-person approval signal
             before moving from dispose-candidate to disposed.
DELIVERABLES: workflow, cron schedule registration, admin UI to configure policy,
             runbook docs/runbooks/08-retention.md with 'emergency pause' section.
DoD:         A policy with 30-day retention on a document created 31 days ago causes
             the document to enter archived state on the next cron run.
TESTS:       Time-travel test using Temporal's time skipping.
7.2 Prompt 8.2 — Legal hold API
ROLE:        Senior backend engineer.
CONTEXT:     legal_holds table exists. No endpoints. Hold is a binary flag per
             document.
TASK:        Expose:
               POST   /compliance/holds
               GET    /compliance/holds?status=active&document_id=&custodian=
               GET    /compliance/holds/{id}
               PATCH  /compliance/holds/{id} (extend, update scope, update
                       custodian)
               POST   /compliance/holds/{id}/release (requires reason + approver)
             - A hold pins every current AND future version of the document.
             - Deletion, disposition, and redaction are blocked while any hold is
               active.
             - Release emits dms.hold.released.v1 with audit trail.
             - OPA policy: only users with role=compliance_officer can create/
               release holds.
CONSTRAINTS: Immutability: once created, hold_id, tenant_id, matter_id are never
             mutable.
DELIVERABLES: handlers, service, repo, OpenAPI updates, admin UI page
             apps/web/src/pages/admin/legal-holds/, policy rule, audit events.
DoD:         A held document returns 423 Locked on DELETE.
TESTS:       Attempt to delete a held doc via every delete path (API, workflow,
             retention cron) → all blocked, all audited.
7.3 Prompt 8.3 — GDPR data-subject endpoints
ROLE:        Senior privacy engineer.
CONTEXT:     Blueprint requires export + erase + anonymize. None implemented.
TASK:        Add three endpoints:
               POST /privacy/dsr/export  { subject_email }
               POST /privacy/dsr/erase   { subject_email, verification_token }
               POST /privacy/dsr/anonymize { subject_email }
             Each starts a Temporal workflow:
             Export: collects every row across all services where subject appears,
               packages as a signed ZIP, uploads to a tenant-scoped bucket, returns
               a signed URL valid 7 days. SLA: 30 days (configurable).
             Erase: overwrites PII columns with 'erased-<uuid>', disposes documents
               authored by the subject unless on legal hold, invalidates all
               sessions + API keys, records a tombstone in a privacy_ledger.
             Anonymize: same as erase but preserves aggregate analytics by hashing
               subject identifiers rather than overwriting.
             Every DSR action must be logged to privacy_ledger with 7-year retention.
CONSTRAINTS: Legal hold short-circuits erase — emit dms.dsr.blocked.v1 instead.
             Never delete audit trail entries; redact them in export.
DELIVERABLES: workflows, endpoints, admin UI 'Privacy Requests' page, runbook,
             ADR 0023-gdpr-dsr-strategy.md.
DoD:         End-to-end test: register a user, upload 3 docs, request export →
             signed ZIP arrives, contains exactly that user's data, cross-tenant
             users absent.
TESTS:       Legal hold interaction test: erase request on a subject with any held
             doc returns blocked-until-released status, writes a ledger entry.
7.4 Prompt 8.4 — Residency dashboard
ROLE:        Senior full-stack + infra engineer.
CONTEXT:     Per-document residency is on the blueprint. Today every doc lives in
             the default bucket in the default region.
TASK:        - Add column storage_region to documents (nullable, default tenant
               region).
             - Storage service picks the bucket by region at upload time.
             - Admin dashboard page shows per-region document counts, per-tenant
               override, and a 'migrate documents to region X' button that queues a
               Temporal workflow (move + re-encrypt with region-local KEK).
             - The migration workflow is resumable, idempotent, and emits
               dms.residency.migrated.v1.
CONSTRAINTS: FINISH-what-exists: this dashboard is a view + a trigger, not a new
             residency policy engine. Policy decisions continue to live in OPA.
DELIVERABLES: migration, endpoint, page, workflow, runbook.
DoD:         A tenant admin can see where their documents live and initiate a
             migration for a subset.
 
8. Wave 9 — Signature Service (PAdES)
The signature service is an empty skeleton. This wave integrates a PAdES-compliant signing library and exposes the minimum flow the blueprint describes.
8.1 Prompt 9.1 — PAdES library selection ADR
ROLE:        Senior document-security engineer.
CONTEXT:     services/signature has no PDF library.
TASK:        Author docs/adr/0024-pades-library.md. Compare: Digidoc4j, PDFBox-PAdES,
             iText 7 (AGPL — rule out for distribution), pdfcpu + custom DSS, and
             eu.europa.ec.dss. Pick based on: license compat (no AGPL/GPL for
             self-hosted customer builds), PAdES B-B / B-T / B-LT / B-LTA support,
             LTV (long-term validation), maintenance activity, CVE history,
             JVM-free if possible.
             Recommend one. Document fallback if primary becomes unmaintained.
DoD:         ADR merged. Library pinned in go.mod (or as a sidecar service if JVM).
8.2 Prompt 9.2 — Signing orchestration
ROLE:        Senior backend engineer.
CONTEXT:     The signature workflow stub from Wave 7 now gets a real implementation.
TASK:        Implement:
               POST /signatures/envelopes { document_id, signers[], fields[], order }
               GET  /signatures/envelopes/{id}
               POST /signatures/envelopes/{id}/signers/{signer_id}/sign
                    (body: signed hash + cert chain OR server-held HSM directive)
             - Each signature produces a PAdES-B-LT revision of the PDF.
             - Server-held signing uses KMS-resident cert (for SaaS tenants); user-
               held signing accepts a detached signature + cert chain (for on-prem).
             - Every event is audited + emitted as dms.signature.*.v1.
             - The Temporal signature workflow tracks envelope state and signer
               sequence.
CONSTRAINTS: Only PAdES. No DocuSign-style branded UI — FINISH-what-exists.
DELIVERABLES: handlers, workflow, HSM integration doc, admin page for in-flight
             envelopes, runbook.
DoD:         A 2-signer envelope signs in order and yields a PDF whose signatures
             validate in Adobe Acrobat Reader with 'Signature valid' + LTV.
TESTS:       Adobe Reader validation harness (pdf-signature-validator CLI) asserts
             B-LT compliance on the output.
 
9. Wave 10 — Admin UI Completion
Seven admin pages are missing. Each is a small page, but their absence blocks pilot customer self-service.
9.1 Pages to build (finish-what-exists scope)
Admin page	Content + API it wires to
Groups	List, create, rename, delete groups. Add/remove members. Uses existing /groups API.
Permission matrix	Role × resource × action grid. Read-only. Pulls from OPA bundle + policy service.
SSO wizard	Step-by-step SAML/OIDC connection test. Upload metadata, run a test auth, show assertion attributes.
Retention policies	Form: name, trigger (created_at, last_accessed, custom), duration, disposition action.
Legal holds	List + create + release. Uses Wave 8 endpoints.
Webhooks	List, create, rotate secret, delivery log with last 100 attempts, re-deliver button.
Metadata schema	JSON schema editor (monaco) with validation. Apply to a doc type. Version-tracked.
Tags admin	Tenant-level tag CRUD (per-doc tagging already works).
Share-link admin	List active share links per doc; revoke one; revoke-all button per doc.
9.2 Prompt 10.x — Pattern for every admin page
ROLE:        Senior React / TypeScript engineer.
CONTEXT:     All admin pages share a layout defined in apps/web/src/layouts/
             AdminLayout.tsx. Use TanStack Query for data, Zustand only for UI state,
             shadcn/ui components exclusively, tailwind for layout.
TASK:        Build page <PAGE_NAME>:
             - Route: /admin/<slug>
             - Data: TanStack Query, query keys [<slug>, tenantId, filters].
             - Table: columns <list>; sortable; paginated; URL-state.
             - Actions: <action list> with confirmation dialogs on destructive ops.
             - Empty state: useful copy + primary action.
             - Error state: toast + inline retry.
             - Loading: skeletons matching row count.
             - Access: policy check `admin:<slug>:read` on mount; redirect on denial.
CONSTRAINTS: No new design tokens, no new npm deps, no new icons outside lucide.
DELIVERABLES: page, tests (Playwright + react-testing-library), screenshot in docs.
DoD:         WCAG 2.1 AA compliance (axe CI check passes). Keyboard reachable.
             No layout shift. Mobile > 375px works.
9.3 Prompt 10.y — MFA recovery login flow (finish-what-exists)
ROLE:        Senior full-stack engineer.
CONTEXT:     Backend MFA recovery code endpoint exists. Login UI never added the
             'use recovery code' path.
TASK:        On the TOTP challenge page, add a link 'use a recovery code'. Posts to
             /auth/mfa/recovery with {code}. On success, force the user through a new
             MFA enrollment immediately and revoke all remaining recovery codes.
DoD:         End-to-end: enroll MFA, log out, log in with recovery code, prompted to
             re-enroll, all old recovery codes invalidated.
9.4 Prompt 10.z — Document viewer: annotations + version-compare
ROLE:        Senior frontend engineer.
CONTEXT:     PDF.js viewer exists. Annotations + version-compare are listed as
             pending in the State doc, not as new features — they are scoped in the
             blueprint's prompt 16.
TASK:        - Annotations: highlight + sticky-note only (no freehand). Persist as
               document_annotations table. Show other users' annotations in read-
               only mode.
             - Version-compare: side-by-side PDF.js viewer with text-level diff.
               Use diff-match-patch. Highlight added/removed.
             - Thumbnails: fetch from preview service (finish-what-exists — backend
               already generates them), replace raw-PDF first-page fallback.
CONSTRAINTS: No new viewer library. No collaborative-cursor overlays (that is a
             future feature).
DoD:         Open a document, highlight a paragraph, save, reload — annotation
             persists. Compare v1 vs v3 shows inline diff.
 
10. Wave 11 — Integration & Plumbing Completion
10.1 Prompt 11.1 — Collaboration auth + comments
ROLE:        Senior Node + WebSocket engineer.
CONTEXT:     services/collaboration boots but auth middleware and comment handlers
             are incomplete. Comments already have REST endpoints.
TASK:        - WebSocket upgrade verifies the dms_session cookie (same auth path as
               the web app) + CSRF token in the handshake.
             - Connection join room r:<document_id>; room membership checked via
               policy service (document:read).
             - Events over the socket: comment.created, comment.updated, comment.
               deleted, presence.joined, presence.left, typing.start, typing.stop.
             - Each event is validated against a Zod schema; invalid messages drop
               the connection with a 4000-range close code.
             - Server-side fan-out uses Redis Pub/Sub so multiple collaboration pods
               stay consistent.
CONSTRAINTS: No new message types beyond those listed. No collaborative editing.
DELIVERABLES: middleware, handlers, Zod schemas, e2e test with two browsers, load
             test (k6) with 1k concurrent connections per pod.
DoD:         User A comments; User B (in the same doc) sees the comment within 200ms
             without refresh. Cross-tenant WebSocket attempt rejected at handshake.
10.2 Prompt 11.2 — Preview thumbnails in viewer
ROLE:        Senior frontend engineer.
CONTEXT:     Preview service already generates per-page thumbnails to S3 on
             dms.version.uploaded.v1 (after Wave 5 is merged). Viewer still renders
             the raw PDF.
TASK:        Update the browser grid and document viewer sidebar to show the
             pre-rendered thumbnails (signed URL from storage), falling back to
             PDF.js-generated thumbnails only if the preview is not yet available
             (show a 'generating preview...' chip).
DoD:         Grid view loads thumbnails in <200ms p95 on the pilot dataset instead
             of parsing a PDF per tile.
10.3 Prompt 11.3 — Audit verify-integrity endpoint
ROLE:        Senior backend engineer.
CONTEXT:     Audit service has a hash chain but no public verify-integrity endpoint.
TASK:        Add:
               POST /audit/verify-integrity { tenant_id, from_ts, to_ts }
             Walks the chain, recomputes each hash, returns
               { ok: bool, broken_at_event_id?, expected_hash?, actual_hash? }
             Plus a CLI: `dms-admin audit verify --tenant <id> --since 2024-01-01`.
             Attack surface: if a chain is broken, DO NOT self-heal; produce a
             forensics report and emit dms.audit.tamper_detected.v1 (P0 alert).
DoD:         Injecting a bad row into audit_events (test fixture) → verify returns
             broken_at with the exact event_id; P0 alert fires.
10.4 Prompt 11.4 — Mobile camera + offline cache
ROLE:        Senior React Native engineer.
CONTEXT:     Mobile app has 10 screens; camera capture and offline cache are
             partially wired (blueprint prompt 24).
TASK:        - Camera: capture, auto-edge-detect (react-native-document-scanner-
               plugin), queue upload, resume on reconnect. Max 25 MB per capture.
             - Offline cache: documents opened while online are cached with their
               preview + metadata; cache is per-tenant, encrypted with device-local
               key. Eviction LRU, cap 500 MB.
             - Background sync uses WorkManager (Android) / BGTaskScheduler (iOS).
CONSTRAINTS: No new screens. Finish the two capabilities exactly.
DoD:         Plane-mode test: open 3 docs online, disconnect, reopen → all 3 render.
             Reconnect, modify one, background sync pushes change.
10.5 Prompt 11.5 — Connector OAuth flows (finish-what-exists)
ROLE:        Senior backend engineer.
CONTEXT:     M365 and Salesforce OAuth flows are scaffolded but not connected.
             Webhook delivery logic is done.
TASK:        Complete the authorization-code + PKCE flow for both. Store refresh
             tokens encrypted per-tenant (reuse the per-tenant KEK). On token
             refresh failure, mark the connection degraded and notify the admin.
DoD:         Admin can connect an M365 tenant end-to-end; a test event round-trips.
 
11. Wave 12 — Infrastructure Completion
11.1 Prompt 12.1 — Complete Helm charts
ROLE:        Senior DevOps engineer.
CONTEXT:     9 of 14 services have full Helm templates. Missing: billing,
             collaboration, connector, intelligence, preview.
TASK:        For each missing service, produce: Deployment, Service, HPA (min 2,
             max configurable, cpu 70%, memory 80%), PDB (maxUnavailable 1),
             NetworkPolicy (deny-all + explicit egress allow-lists to postgres,
             redis, nats, s3, kms), ServiceAccount, PodSecurityContext (runAsNonRoot,
             readOnlyRootFilesystem, allowPrivilegeEscalation=false, seccomp
             RuntimeDefault, caps drop ALL), Ingress (optional), PrometheusServiceMonitor.
             Pin all images by digest. Use topology-spread constraints: zone + hostname.
CONSTRAINTS: Reuse the existing Helm library chart charts/dms-common.
DELIVERABLES: 5 chart directories, helm lint passes, helm template renders cleanly,
             policy tests with conftest pass.
DoD:         `helm install vaultdms charts/umbrella --dry-run` produces a valid plan.
11.2 Prompt 12.2 — Secrets rotation CLI
ROLE:        Senior platform engineer.
CONTEXT:     dms-admin binary exists but is missing the rotate-secrets subcommand.
TASK:        Implement:
               dms-admin secrets rotate --target jwt-signing --scope global
               dms-admin secrets rotate --target tenant-kek --tenant <id>
               dms-admin secrets rotate --target api-internal-token --service <name>
               dms-admin secrets rotate --target db-password --role <role>
             Each rotation: creates new version in Vault/KMS, dual-read window
             (accept old + new for N minutes), flip write to new, wait for drain,
             revoke old. Emits dms.rotation.started.v1 and dms.rotation.completed.v1.
CONSTRAINTS: No downtime. No manual pod restart required.
DELIVERABLES: subcommand, runbook docs/runbooks/12-secret-rotation.md with a
             rehearsal script.
DoD:         A full rotation of every secret type succeeds in staging with zero 5xx.
11.3 Prompt 12.3 — Control plane completion (tenant provisioning + Stripe)
ROLE:        Senior SaaS platform engineer.
CONTEXT:     Control plane has provisioning + Stripe webhook scaffolds only.
TASK:        Finish:
             - Tenant provisioning workflow (Temporal): create Postgres schema
               entries, RLS seed, KMS CMK alias, OpenSearch index, Qdrant collection,
               default roles, default policies.
             - De-provisioning: 30-day soft delete, then hard dispose with
               cryptographic shredding of the CMK.
             - Stripe webhook handlers: checkout.session.completed,
               invoice.payment_failed, customer.subscription.updated,
               customer.subscription.deleted. Each writes to subscriptions + emits
               domain events + updates entitlements.
             - Idempotency: Stripe event_id stored in stripe_events table; duplicate
               webhooks are no-ops.
             - Signature verification on every webhook.
CONSTRAINTS: FINISH-what-exists: no new plan types, no new meter types.
DELIVERABLES: workflow, handlers, admin page 'Tenants', runbook.
DoD:         Signup → checkout → tenant is live in 3 minutes. Payment failure →
             tenant moved to grace period → 14 days later suspended.
11.4 Prompt 12.4 — Billing usage cron fix
ROLE:        Senior backend engineer.
CONTEXT:     billing cron errors every minute because usage_records table is missing
             or misdeclared (known residual from 04b).
TASK:        Create/fix usage_records migration (tenant_id, metric_key, period_start,
             period_end, quantity, unit, aggregated_at, reported_at). Idempotent
             aggregation. Stripe Meter reporting. Runbook update.
DoD:         Log stream is clean; billing dashboard shows per-tenant usage.
11.5 Prompt 12.5 — Per-service migrations split
ROLE:        Senior DB engineer.
CONTEXT:     9 services (auth, policy, storage, workflow, notification, signature,
             billing, connector, audit) share document's initial migration today.
TASK:        Split: each service owns its tables under services/<name>/migrations/.
             Single tool: `dms-admin db migrate --service <name>`.
             Composite target: `dms-admin db migrate --all` runs in dependency order.
             Shared tables (tenants, users) stay in a pkg/schema/core package.
CONSTRAINTS: No data change. No column change. Structural only.
DoD:         Fresh db + `migrate --all` produces an identical schema diff to today.
 
12. Wave 13 — Testing, Observability, Chaos
Without this wave, we do not know whether anything built is correct under load.
12.1 Prompt 13.1 — Integration test harness in CI
ROLE:        Senior test infra engineer.
CONTEXT:     Integration tests exist but are not in CI. Unit tests on 3 services only.
TASK:        - CI job 'integration' boots a minimal docker-compose (postgres,
               opensearch, qdrant, redis, nats, minio, temporal-dev, clamav-stub)
               and runs the test suite in services/*/test/integration/.
             - Golden dataset: 50 fixture PDFs, 20 DOCX, 10 emails, 5 images.
             - Run per PR with cache of binaries + model weights.
             - Budget: 12 minutes p95 wall clock; fail CI if exceeded.
             - Add a smoke suite (30s) that runs on every push to any branch.
CONSTRAINTS: No Temporal Cloud, no managed OpenSearch, no managed Qdrant.
DELIVERABLES: .github/workflows/integration.yml, test fixtures, README in tests/,
             CI dashboard tile.
DoD:         A breaking change to storage.CompleteUpload fails CI within 3 minutes.
12.2 Prompt 13.2 — Load tests executed, not just written
ROLE:        Senior performance engineer.
CONTEXT:     k6 scenarios exist (blueprint prompt 30) but have never been run.
TASK:        Execute against a staging cluster sized to pilot target:
             - Upload: 500 docs/min steady, 2000 docs/min burst for 5 min.
             - Search: 2000 q/s mixed lexical+semantic, 70/30 split.
             - Auth: 500 logins/s.
             - Collab: 1000 WS connections per pod.
             Capture: RED per endpoint, DB/NATS/Redis USE, cost per 1k operations.
             Compare to SLOs: search p99 <300ms, upload-init <200ms, OCR p95 <30s,
             perm check <5ms.
             Produce docs/performance/baseline-YYYYMMDD.md with flamegraphs, bottle-
             necks, and fix PRs filed.
DoD:         All SLOs met OR a fix PR exists for every missed SLO.
12.3 Prompt 13.3 — Chaos suite
ROLE:        Senior reliability engineer.
CONTEXT:     Chaos tests never executed.
TASK:        Using litmus or chaos-mesh, author and run:
             - Pod kill: every service, every 10 minutes, 1 hour run.
             - Network partition: postgres primary vs. replicas, 5 min.
             - Clock skew: +5 minutes on one node, validate workflow time-based logic.
             - Disk full: /var/lib/postgresql filled to 98%.
             - NATS disconnect: storage pod loses NATS for 60s; outbox must drain.
             - Upstream failure: simulate KMS outage; document behavior.
             Each scenario has: expected behavior, actual behavior, screenshots of
             Grafana during the run, post-mortem.
DELIVERABLES: docs/chaos/scenarios/*.md, chaos runbook, scheduled quarterly drill.
DoD:         Every scenario has a PASS verdict or a tracked fix.
12.4 Prompt 13.4 — Frontend test coverage ramp
ROLE:        Senior frontend QA engineer.
CONTEXT:     Frontend has 0% coverage.
TASK:        - Vitest + react-testing-library for components.
             - Playwright for 10 critical user journeys: login, MFA, login+MFA
               recovery, upload, search, share, review approve, admin user create,
               API key rotate, audit export.
             - Target: 50% statement coverage on apps/web/src/pages at end of wave.
             - Visual regression: one snapshot per page, reviewed on PR.
DoD:         CI runs all 10 journeys on PR. Coverage gate set at 50% and rising.
12.5 Prompt 13.5 — Mutation testing on auth + policy + storage
ROLE:        Senior security test engineer.
CONTEXT:     Critical security packages should not silently regress.
TASK:        Add go-mutesting to CI on packages services/auth/internal/...,
             services/policy/internal/..., services/storage/internal/crypto/...
             Fail PR if mutation score < 70%.
DoD:         Baseline score published; scorecard panel in observability dashboard.
12.6 Prompt 13.6 — SLI / SLO / error-budget policy
ROLE:        Senior SRE.
CONTEXT:     SLOs stated, but SLIs not automated into a burn-rate alert.
TASK:        For each SLO: define SLI (window = 30d rolling), compute error budget,
             author Prometheus multi-window burn-rate alerts (fast + slow),
             dashboard per SLO, and error-budget policy
             (docs/slo/error-budget-policy.md) that says: when budget < 20%,
             all feature work pauses. Write the pause runbook.
DoD:         Burning 10% of the upload SLO fast burn produces a page to on-call in
             <5 min; dashboard shows trend.
 
13. Wave 14 — Documentation, Runbooks, Release Readiness
13.1 Prompt 14.1 — Service READMEs (14 files)
ROLE:        Senior tech writer + engineer.
CONTEXT:     Every service lacks a README. On-call cannot page a service they don't
             understand.
TASK:        Produce services/<name>/README.md with:
             - One-paragraph purpose.
             - Runtime dependencies (versions).
             - Local dev run (one command).
             - Env vars table.
             - HTTP endpoints table.
             - gRPC services table.
             - NATS subjects consumed/published.
             - Database tables owned.
             - Metrics list.
             - Common failure modes + fixes.
             - Architecture diagram (PlantUML or Mermaid) embedded.
             - On-call runbook link.
DoD:         14 READMEs merged; docs site renders them; a new engineer can run any
             service locally in <15 minutes.
13.2 Prompt 14.2 — OpenAPI completion
ROLE:        Senior API engineer.
CONTEXT:     docs/api/openapi.yaml is partial.
TASK:        Reach 100% route coverage. Use oapi-codegen to regenerate server stubs
             and client SDKs (TS, Python, Go). Add CI rule: openapi-diff on PR; any
             handler without a spec entry fails.
DoD:         Swagger UI at /docs renders 100% of live routes with example payloads.
13.3 Prompt 14.3 — Disaster recovery runbook + rehearsal
ROLE:        Senior SRE.
CONTEXT:     No DR runbook. Backups never practiced.
TASK:        Write docs/runbooks/dr/ with:
             - RPO/RTO targets (15 min / 4 hours proposed).
             - Full cluster restore from cold backups (postgres WAL, opensearch
               snapshots, qdrant snapshots, object store replication).
             - Per-tenant restore.
             - Quarterly rehearsal script and checklist.
             - Tabletop exercise template.
             Then RUN the rehearsal in a staging copy; capture the time taken;
             iterate until RTO is met.
DoD:         Signed rehearsal report in docs/runbooks/dr/rehearsals/YYYY-MM-DD.md.
13.4 Prompt 14.4 — Threat model + pentest remediation plan
ROLE:        Senior security engineer.
CONTEXT:     No formal threat model exists.
TASK:        STRIDE-based threat model in docs/security/threat-model.md covering:
             - Trust boundaries (frontend, gateway, service mesh, DB, KMS).
             - Data flow diagrams per critical operation.
             - Threats ranked by risk with mitigations and status.
             Schedule external pentest. Gate pilot launch on resolution of all
             CRITICAL + HIGH findings.
DoD:         Threat model approved; pentest report attached; all HIGH+ resolved.
13.5 Prompt 14.5 — Air-gapped + on-prem packaging
ROLE:        Senior distribution engineer.
CONTEXT:     Blueprint requires air-gapped deployment. Today image-layering strategy
             is absent.
TASK:        Produce a single-tarball distribution:
             - All container images (service + sidecars + dependencies).
             - OCR model weights (~4 GB), embedding models, RAG LLM (~10 GB) as a
               separate 'ai-pack' tarball so customers who don't use AI skip it.
             - Helm chart + values template.
             - `dms-installer` binary that validates prerequisites and installs.
             - Offline docs site.
             Sign every artifact with cosign.
DoD:         End-to-end install in an air-gapped VM reaches a working login in <30
             minutes.
13.6 Prompt 14.6 — Release engineering
ROLE:        Senior release engineer.
CONTEXT:     No release versioning or changelog discipline today.
TASK:        - SemVer across the monorepo with `release-please`.
             - Conventional commits enforced in CI.
             - Changelogs auto-generated, per-service + umbrella.
             - Signed release artifacts (cosign + SLSA provenance level 3).
             - Support matrix: last 2 minor versions supported + CVE backports.
DoD:         A PR merge that changes storage produces a new storage minor release
             with signed artifacts and provenance attestation.
 
14. Out-of-Scope Ledger
Anything the agent discovers during execution that looks like a feature gap but is NOT in the State of the Project gets recorded here and deferred. This prevents scope creep and keeps the finish line visible.
Rule
If the AI agent finds a missing feature mid-prompt, it MUST stop, append an entry to docs/backlog/out-of-scope.md with (date, wave, where-found, suggested-fix), and return to the prompt. It MUST NOT implement the feature.
14.1 Pre-seeded entries
•	Desktop sync client — deferred post-G3.
•	Terraform / Ansible modules for on-prem — deferred post-G2.
•	Collaborative live-cursor editing — not in blueprint; decline if requested.
•	Custom workflow authoring UI (drag-to-create) — read-only designer only in Wave 7.
•	DocuSign-style branded envelope UI — PAdES-only in Wave 9.
•	AI-generated document summaries auto-appended to every doc — blueprint has RAG on-demand only.
 
15. Gate Acceptance Matrices
15.1 G1 — Pilot-Ready (exit criteria)
Domain	Proof
Event pipeline	Upload a PDF, within 60s it is OCR'd, classified, embedded, searchable by semantic query, and RAG can answer a question about it. Cross-tenant isolation verified.
Security	No shared KEK, no localStorage token, crypto/rand everywhere, context.Background() CI guard green, outbox-only publishing guard green.
Auth + admin	Users, roles, API keys, SSO wizard, MFA recovery login, audit export — all functional in the UI.
Search	Lexical + semantic hybrid search in UI; saved searches; facets; highlights.
Viewer	PDF viewer with preview-service thumbnails, annotations, version compare.
Compliance	Legal hold API + UI live; retention cron runs; GDPR export + erase + anonymize work.
Observability	Grafana dashboards for every service; 14 READMEs merged; SLI/burn-rate alerts wired.
Reliability	Chaos kill-pod scenario passes for every service; integration tests in CI.
Release	Signed release; SLSA L3 provenance; changelog; rollback drill rehearsed.
15.2 G2 — Production SaaS (additional exit criteria)
•	Workflows: review, approval, retention, signature-orchestration all live.
•	Signature service producing Adobe-valid PAdES B-LT PDFs.
•	Control plane: self-serve signup → tenant provisioned in <3 minutes.
•	Multi-region residency dashboard + migrate-documents workflow.
•	Secrets rotation for every secret type with zero downtime.
•	Load test meets SLOs (search p99 <300ms at 2000 q/s).
•	DR rehearsal within RTO/RPO targets.
•	Threat model + external pentest remediations all HIGH+ closed.
15.3 G3 — Feature-Complete (additional exit criteria)
•	All 14 services: Helm, HPA, PDB, NetworkPolicy, PodSecurity.
•	Connectors: M365 + Salesforce OAuth round-trip.
•	Mobile: camera + offline cache.
•	Air-gapped distribution validated in a fresh VM.
•	OpenAPI 100%.
•	Mutation tests on auth, policy, storage crypto >= 70%.
 
16. Claude Skill Orchestration
This project completion is meant to be executed with a Claude-class agent. The following mapping tells the agent which skill/tool to use for each kind of task.
Task class	Skill / tool to invoke
Go / TS / Python code	Code editor + bash (go build, pnpm test, pytest, golangci-lint). Use the code-auditor skill for every non-trivial PR review.
OpenAPI / JSON schema	Edit docs/api/openapi.yaml; run oapi-codegen; validate with spectral; auto-generate SDKs.
Architecture docs / ADRs	Markdown files under docs/adr. Use the docx skill only when exporting final readouts for executives.
Runbooks	Markdown files under docs/runbooks. Follow the Google SRE runbook template.
Database migrations	sqlc or goose migrations under services/<name>/migrations/. Always generate an UP + DOWN pair.
Deployment manifests	Helm charts under charts/. Validate with helm lint + conftest + kube-linter.
Grafana dashboards	JSON files under ops/grafana/dashboards/. Use grafonnet-lib for authoring.
Prometheus rules	YAML under ops/prometheus/rules/. Validate with promtool test rules.
Chaos experiments	YAML under ops/chaos/. litmus or chaos-mesh CRDs.
Load scenarios	k6 scripts under ops/loadtests/. Run in staging; publish baselines.
Threat model / security	Markdown under docs/security; STRIDE tables; attack trees; use draw.io diagrams checked in as SVG.
Executive readouts	Use the docx skill. Same voice as this document.
Spreadsheet trackers	Use the xlsx skill for the wave-burndown and RAID log.
Slide reviews	Use the pptx skill for stakeholder updates.
Research (libraries, CVEs, standards)	Web search + web fetch. ADR always records sources consulted.
Front-end design review	Use the frontend-design skill for every new admin page.
16.1 Standing prompt preamble
Every time the agent resumes work on this project, it prepends this preamble to its session:
You are resuming work on SeDoc. Read first:
  1. docs/STATE_OF_THE_PROJECT.md
  2. docs/ENTERPRISE_COMPLETION_PROMPT.md (this document)
  3. docs/backlog/out-of-scope.md
  4. docs/adr/index.md
 
Rules:
  - NO new features. Check every request against the State and this Prompt.
  - Honor the Global DoD (Section 1.4) on every PR.
  - Honor the Enterprise Execution Principles (Section 3).
  - Work in wave order. Do not start Wave N+1 until Wave N's Acceptance Tests pass.
  - If a request implies a feature, STOP, add to out-of-scope.md, and decline.
  - On every PR: update OpenAPI, tests, metrics, docs, runbook, dashboard.
 
Ask for the current wave and current prompt number before writing code.
 
17. RAID Log Starter (Risks · Assumptions · Issues · Dependencies)
Type	Severity	Statement + mitigation
Risk	Critical	Cross-tenant data leak under load never tested. MIT: Wave 5 cross-tenant test + Wave 12 chaos suite + external pentest.
Risk	High	Per-tenant KEK migration window could leave objects in inconsistent state. MIT: dual-read, shadow decrypt, resumable backfill.
Risk	High	Temporal operational ramp steeper than team expects. MIT: dedicate one engineer to own Temporal during Waves 7–9; pair with temporal.io support.
Risk	Medium	OCR accuracy below customer expectations on real docs. MIT: Wave 5.3 adds accuracy harness; publish per-tenant accuracy metrics.
Risk	Medium	Air-gapped image size (~20 GB with AI pack) challenges customer download/storage. MIT: AI pack is optional and versioned separately.
Assumption	—	Customer pilots are single-region. Multi-region deferred to G2.
Assumption	—	LiteLLM as RAG adapter is acceptable; no direct vendor lock-in to a specific LLM provider.
Issue	Open	usage_records cron errors every minute (fixed in Wave 12.4).
Issue	Open	dms.user.* events silently dropped (fixed in Wave 5.2).
Dependency	External	KMS/Vault availability SLA ≥ 99.99%. MIT: cache unwrapped DEKs in memory for 60s with envelope re-wrap on rotation.
Dependency	External	Temporal Cluster availability; self-hosted option must match SaaS behavior.
17.1 Indicative timeline (4 engineers unless noted)
Wave	Wall time	Notes
Wave 5	1 week	Blocker; all hands. Parallelize 5.1/5.2 with 5.3/5.4/5.5.
Wave 6	1 week	Can partially overlap with Wave 5 Part 2.
G1 ready	~3 weeks total	Per original estimate.
Wave 7	1.5 weeks	Temporal learning curve; one pair leads.
Wave 8	1.5 weeks	Compliance review with legal on DSR flow.
Wave 9	1 week	PAdES integration may require JVM sidecar.
Wave 10	1.5 weeks	Frontend-weighted; parallel with Wave 8.
Wave 11	1 week	Small pieces; parallelizable.
Wave 12	1.5 weeks	Infra-heavy.
G2 ready	~2–3 months	Per original estimate, 6 engineers.
Wave 13	1 week continuous + quarterly	Chaos rehearsals become BAU.
Wave 14	1 week	Documentation sprint; all-hands Friday.
G3	~6 months total	Per original estimate.
 
18. Closing Directive
Directive to the executing agent
You are not here to be creative. You are here to finish.
Every wave in this document exists because someone can already see the shape of it in the code. Your job is to complete the shape — not to redesign it.
When in doubt, prefer the boring, auditable, observable choice. Prefer the change that is easiest to roll back. Prefer the change that produces the clearest Grafana panel. Prefer the change that a senior engineer on vacation can read in three minutes.
At the end of every wave, ship a short memo to stakeholders: what shipped, what metrics moved, what surprised you, what is next. Treat shipping a memo as part of the wave's DoD.
When Waves 5 through 14 are green, SeDoc is no longer a promising skeleton. It is a production enterprise DMS.
Do the work in that order. Ship each wave. Do not add features.
— END OF DOCUMENT —
