# SeDoc codebase review — addendum (round 2)

Companion to [2026-10-12-codebase-review.md](2026-10-12-codebase-review.md). Same branch and commit (`neu-ui` @ `e9e74eec`). Round 2 did two things: verified every item the first report marked "reported only", and reviewed the areas the first round did not reach.

- Items prefixed **R-** were verified by an adversarial Octopus-persona verifier.
- Items prefixed **S-, D-, N-, W-, C-, O-** come from round-2 reviewer seats and are **not yet independently verified**. Whoever fixes one must first confirm it in code; a refuted item is recorded as such, not fixed.

## R — previously "reported only", now verified

| ID | Sev | Finding | Where |
|---|---|---|---|
| R-1 | MEDIUM | `NewIPRateLimiter` keys on `RemoteAddr`; behind Kong every request shares the Kong address, so one anonymous client exhausts the share-link (10/min), zt (30/min) and M365-ingest (60/min) buckets for every tenant. `rate` is never read (coarse fixed window). | pkg/middleware/ratelimit_ip.go:37-55; services/document/cmd/server/main.go:500,511-512 |
| R-2 | MEDIUM | Client IP for audit/outbox/request logs is the left-most `X-Forwarded-For` hop; Kong appends rather than replaces and has no `trusted_ips`, so audit `ip_address` is client-chosen. | pkg/middleware/correlation.go:73-79,101-102; requestlog.go:83-98; gateway_meta.go:21-22 |
| R-3 | MEDIUM (HIGH for ERP) | Idempotency `in_progress` rows are never reclaimed; completion UPDATE and release DELETE both run on `r.Context()`, so a client disconnect after the record was created wedges the key with 409 forever; a panic skips both. Search mounts the middleware but only document migrates the table. | pkg/middleware/idempotency.go:179-211; services/search/cmd/server/main.go:219,222 |
| R-4 | LOW | API-key `last_used_at` touch runs in a goroutine on the request context and is usually cancelled. | pkg/middleware/apikey.go:93-96 |
| R-5 | MEDIUM | Anonymous, gateway-signature-exempt esign webhook reads an unbounded body before HMAC; MCP decodes unbounded bodies behind API-key auth; no Kong size limit. | services/signature/internal/handler/esign.go:303; pkg/middleware/gatewaysig.go:22; services/mcp-server/internal/handler/handler.go:55,136 |
| R-6 | LOW | MCP SSE sessions are not bound to the opening API key (response spoofing into another stream); close/send race panics a request. | services/mcp-server/internal/handler/handler.go:124-152; sessions.go:69-75 |
| R-7 | LOW | GraphQL child-field arguments are resolved with nil variables (latent). | services/graphql-gateway/internal/exec/executor.go:207 |
| R-8 | LOW | `dms-admin rotate-secrets` writes keys nothing reads, prints 64-bit key prefixes, reports "Rotation complete". | cmd/dms-admin/main.go:62-102 |
| R-9 | MEDIUM | `GET /api/v1/health` is routed anonymously by Kong to `auth:8081/readyz`, which returns raw Postgres/Redis/S3 driver errors (host, user, database). | pkg/health/health.go:135,143,159; deploy/gateway/kong.yaml:460-463 |
| R-10 | LOW | Dead code traps: `pkg/events` Subscriber (no recover, drops poison, never writes DLQ) and `pkg/gateway/cors.go` (reflects any Origin with credentials). No importers. | pkg/events/subscriber.go:40; pkg/gateway/cors.go:21,48-61 |
| R-11 | MEDIUM | CI `lint` and `security-go` run at the module-less root; `./...` errors on every run, so the jobs are always red. | .github/workflows/ci.yml:25-29,356-363; .golangci.yml:5 |
| R-12 | MEDIUM | Trivy scans one image; build failures swallowed; scanners pinned to `@master`/`:latest`. | ci.yml:377,392-400,511 |
| R-13 | MEDIUM | `integration/`, collaboration vitest, intelligence/preview pytest (bar one file) and the signer's Gradle tests never run in CI; e2e migrates only the document track. | ci.yml:224-225,274-276,449-450 |
| R-14 | MEDIUM | `make migrate-up/down SERVICE=document` use `document_schema_migrations`; every other path uses the default table; `migrate-force` ignores per-service tables. | Makefile:125,131,139-142 |
| R-15 | HIGH | Gateway Deployment, hook Jobs, bucket-init Job and all CronJobs lack the restricted securityContext; the pre-install migrate hook is rejected by `restricted` admission, so install fails. | deploy/helm/sedoc/templates/{gateway,hooks,jobs,cronjobs}/* |
| R-16 | HIGH | Default Helm install crash-loops collaboration (missing `SEDOC_GATEWAY_SECRET` and other env); no WebSocket `maxPayload`; Redis subscriber `JSON.parse` uncaught. | templates/collaboration/deployment.yaml:21-27; services/collaboration/src/index.js:29,81-87 |
| R-17 | MEDIUM | Signer image bakes a self-signed dev keystore with password `changeit` as the default. | services/signature-signer/Dockerfile; SignerServer.java:25-32 |
| R-18 | HIGH | ExternalSecrets creates secrets nothing reads; pods read `sedoc-encryption/local-kek` (optional), `sedoc-gateway/shared-secret`, `sedoc-billing-secrets/api-key`. | deploy/helm/sedoc/values.yaml:70,90-92,122-146; _helpers.tpl:73-77,112-116 |
| R-19 | MEDIUM | No NetworkPolicy for Postgres/Redis/NATS/OpenSearch/Qdrant/MinIO; Redis auth off; intelligence/preview/collaboration pod templates lack the instance label. | values.yaml:580-584; templates/*/deployment.yaml |

## S — signature, QES, e-sign, signer

| ID | Sev | Finding | Where |
|---|---|---|---|
| S-1 | HIGH | `esign/send` never loads or authorizes the request: any member rebinds any request to their own envelope; the completed envelope's bytes become a new version of the victim document under the creator's identity. | services/signature/internal/service/esign.go:549,657; repository/esign.go:214 |
| S-2 | HIGH | e-sign OAuth start/callback/disconnect are not admin-gated; `state` is a deterministic, non-expiring HMAC of `tenant|provider`; any member binds their own DocuSign/Adobe account to the tenant. | handler/esign.go:161,176,222; pkg/esign/oauth.go:359; service/esign.go:308 |
| S-3 | MEDIUM | QES start takes request, signer and document bytes from the body; caller never bound; the TSP `SignedHash` is discarded. | service/qes.go:89-148,266-276 |
| S-4 | MEDIUM | PAdES parser is quadratic on hostile input; `ByteRange` sums overflow int64 and panic in `make`. | pades/parser.go:279-333,359-369 |
| S-5 | MEDIUM | No body limit on `qes/start` and `esign/send` (base64 PDF in JSON). | handler/qes.go:63; handler/esign.go:72 |
| S-6 | MEDIUM | Signer dependencies with known CVEs: DSS 5.12.1 (BouncyCastle < 1.78), grpc 1.63 / protobuf-java < 3.25.5, logback 1.5.6. | services/signature-signer/build.gradle.kts:39-72 |
| S-7 | LOW | Swisscom client uses an empty `RootCAs` pool (every call fails); InfoCert/Intesi decode 4xx bodies as success. | pkg/signing/tsp/swisscom.go:67; infocert.go:216-228; intesi.go:242-256 |
| S-8 | LOW | Sidecar `Verify` reports `tamper_evident = count > 0` with no trust anchors (no caller today). | SignerService.java:181,219-222 |
| S-9 | LOW | e-sign webhook resolves the tenant client (DB read, possible token refresh) before HMAC and returns raw errors — tenant enumeration. | service/esign.go:478-493,816-826; handler/esign.go:308-316 |
| S-10 | LOW | Concurrent QES returns can flip a completed session to failed. | service/qes.go:218-264; repository/qes.go:114-121 |
| S-11 | LOW | Vendor webhook parsing does not match DocuSign/Adobe wire shapes; completion relies on the reconciler. | pkg/esign/docusign.go:245-251; adobesign.go:597-606 |
| S-12 | LOW | Verifier marks every multi-signature document (including the service's own ceremony output) as not tamper-evident. | pades/parser.go:449-459; ltv.go:80-83 |
| S-13 | CRITICAL | The web sign page never forwards the signer's `?token=`, so every remote and mobile signature returns 401. | web/src/routes/_authenticated/sign.$requestId.$signerId.tsx; web/src/api/signatures.ts:87-93 |
| S-14 | MEDIUM | Signing pages download the whole PDF, base64-encode it on the main thread and send client-chosen bytes and hash to the backend. | sign.$requestId.$signerId.tsx:62-105; signatures.send.$documentId.tsx:89-102 |

## D — document and storage (extended surfaces, internals, consumers)

| ID | Sev | Finding | Where |
|---|---|---|---|
| D-1 | HIGH | ERP upsert relinks any tenant blob by bare `blob_checksum`; the sync delta hands every workspace member the sha256 of every document, including private folders. | services/document/internal/service/external_key.go:353-366; repository/sync_repo.go:56-60 |
| D-2 | HIGH | Watermarked page/thumbnail render authorizes the document in the URL but renders whatever version id is given. | handler/watermark_handler.go:88,175-192; services/preview/app/api/internal.py:59 |
| D-3 | MEDIUM | zt-share create has no permission check (comment promises owner/admin); manifest leaks title, MIME and sender email for any document. | handler/zt_share_handler.go:97-148,273-313; main.go:905-915 |
| D-4 | MEDIUM | The zt public viewer cannot serve its sessionless audience (policy denies nil user; encrypted blobs redirect to a session-only route). | zt_share_handler.go:380-401; storage_proxy.go:272-307 |
| D-5 | MEDIUM | OnlyOffice save-back checks the lock under the version id while WOPI writes it under the document id: concurrent editors overwrite each other. | handler/onlyoffice_handler.go:304-308; wopi_handler.go:144-149 |
| D-6 | MEDIUM | OnlyOffice `config` mints a 24 h write-capable token for any version id without authorization. | onlyoffice_handler.go:103-166 |
| D-7 | MEDIUM | Records whose schedule trigger is not `declaration` never get a cutoff, so `Dispose` skips the retention guard. | internal/records/service.go:317-322,415-417 |
| D-8 | LOW | Sync delta ignores per-document ACL and private folders. | service/sync_service.go:28-62 |
| D-9 | LOW | zt token id is stored in cleartext beside its hash. | zt_share_handler.go:128-129,562-567 |
| D-10 | LOW | zt `max_views` check-then-increment races; bump errors ignored; manifest ignores revocation. | zt_share_handler.go:297-401 |
| D-11 | LOW | OnlyOffice callback JWT checked for signature only (no exp/iat/replay). | onlyoffice_handler.go:212-242,340-356 |
| D-12 | LOW | WOPI identity context carries no role or groups; group/role editors get 404. | handler/wopi_resolver.go:88-90 |
| D-13 | LOW | Hard-coded dev fallbacks for the IRM signing key, zt pepper and OnlyOffice JWT. | service/irm_service.go:47-53; main.go:898-900 |
| D-14 | HIGH | Ingest-route commit replays the uploader as `admin` and appends to whatever document the OCR'd key matches, across workspaces. | service/ingestion.go:248-252 |
| D-15 | HIGH | `ReencryptBlob` overwrites the live object in place (same bucket and key) before the DB commit; a failed UPDATE leaves the blob undecryptable. | services/storage/internal/service/reencrypt.go:157-190 |
| D-16 | HIGH | Retention sweep runs as role `system`, which policy never allows; every archive/dispose is denied while the sweep reports success. | service/retention.go:17-20,82-102,466-472 |
| D-17 | MEDIUM | Re-applying WORM can shorten `retain_until` or downgrade COMPLIANCE→GOVERNANCE. | handler/worm_handler.go:129-133; storage service/worm.go:82-88 |
| D-18 | MEDIUM | `UpdateMigration` has no compare-and-set; RewrapDEK writes a stale snapshot back over a concurrent re-encrypt. | storage repository/content_blobs.go:141-150; service/rewrap.go:138-145 |
| D-19 | MEDIUM | Autolink builds tenant-wide edges from user-set metadata; the contract-graph read returns neighbour titles with no permission check. | internal/autolink/repo.go:58-63; handler/contract_graph_handler.go:80-135 |
| D-20 | LOW | `/internal/v1/records/cutoff-sweep` has neither session nor internal-key auth and reads tenant from context: always 401. | main.go:1001-1004; handler/records_handler.go:472-476 |
| D-21 | LOW | Blob reaper is dead code (nothing decrements `reference_count`); one tenant error aborts the cycle. | storage service/reaper.go:80; repository/content_blobs.go:82,109-131 |
| D-22 | LOW | `liveKEKAlias` swallows DB errors and can rewrap back to a retired KEK. | storage service/rewrap.go:39-41 |
| D-23 | LOW | Document `checkInternalKey` compares the internal key with `==`. | handler/retention_sweep_handler.go:77-84 |
| D-24 | LOW | Ingest-route consumer: unvalidated tenant; `REJECT_DUPLICATE` strands failed items. | internal/ingestroute/consumer.go:58-86 |
| D-25 | LOW | Orphan GC soft-deletes metadata-only documents after 24 h; M365 ingest returns 201 when the blob upload failed. | janitor/orphan_gc.go:141-149; handler/m365_ingest.go:197-201,254-258 |

## N — connector

| ID | Sev | Finding | Where |
|---|---|---|---|
| N-1 | CRITICAL | Webhook fanout subscribes `dms.auth.>` and forwards payloads verbatim; the password-reset event carries the plaintext reset token. Any member can subscribe an external URL and take over the owner account. | services/connector/internal/service/service.go:79,213,251-271; services/auth/internal/service/password_reset.go:75-82 |
| N-2 | HIGH | Event-stream mirror reads `tenant_id` but CloudEvents carry `tenantid`; nearly all events are dropped; `/api/v1/events` is not routed by Kong. | internal/eventstream/mirror.go:71-125; kong.yaml:392-402 |
| N-3 | HIGH | Intake treats an existing `failed` row as already ingested; re-dropped files go to `processed/` and are never ingested; concurrent sweeps race. | internal/intake/intake.go:338-354,511-516 |
| N-4 | HIGH | No lease for email polling or webhook delivery: with 2 replicas every message is ingested and every webhook delivered twice. | internal/email/email.go:137,199-305; repository/repository.go:321-337 |
| N-5 | MEDIUM | IMAP host/port is an unrestricted SSRF and port-scan oracle; `imap_use_tls=false` sends LOGIN in cleartext. | internal/email/imap_backend.go:57-69; email.go:479-531 |
| N-6 | MEDIUM | Intake follows symlinks; `host_path` is unique per tenant only, so two tenants can watch the same path. | intake.go:294,345,428-431; document migration 000042:31 |
| N-7 | MEDIUM | IMAP dedupe key omits UIDVALIDITY; the newest-100 cap means older mail is never ingested. | imap_backend.go:96-100,208-214 |
| N-8 | MEDIUM | Email materialisation is non-atomic: attachment failures dropped, stamp failures duplicate. | email.go:338-416 |
| N-9 | MEDIUM | No size or depth caps on IMAP fetch, MIME walk or intake reads. | imap_backend.go:121; mime.go:50-116; intake.go:345 |
| N-10 | MEDIUM | Connector sealing key and OAuth-state HMAC derive from an empty `SEDOC_LOCAL_KEK` when KMS is vault/aws. | services/connector/cmd/server/main.go:89-90; email/crypto.go:39-44 |
| N-11 | MEDIUM | Event-stream NATS credentials are unusable by construction and unrevocable. | internal/eventstream/eventstream.go:128-297 |
| N-12 | LOW | Unwired Salesforce/Microsoft providers carry JSON injection, tenant-controlled token endpoints and timeout-less HTTP. | internal/providers/{salesforce,microsoft,base}.go |

## W — web admin, bulk upload, add-ins, mobile

| ID | Sev | Finding | Where |
|---|---|---|---|
| W-1 | HIGH | pdf.js worker loaded from unpkg at runtime: violates the app CSP once enforced, runs third-party code on decrypted PDFs, breaks air-gapped tenants. | web/src/components/viewer/PDFLayoutViewer.tsx:10; PDFViewer.tsx:10 |
| W-2 | HIGH | Mobile creates the document before bytes are stored; the offline queue replays non-idempotently and silently drops captures after 5 attempts. | mobile/api/upload.ts:63-90; mobile/store/queue.ts:69-130 |
| W-3 | HIGH | Bulk-upload folder ensurer races under 4 workers (duplicate sibling folders); the conflict-policy selector does nothing. | web/src/lib/bulkUpload/folders.ts:47-63; runner.ts:20-66 |
| W-4 | MEDIUM | SCIM token rotation and retention-schedule deletion fire without confirmation. | admin/scim.tsx:100-103; admin/records.tsx:132-134 |
| W-5 | MEDIUM | Mobile keeps decrypted document bytes in caches that survive logout. | mobile/app/document/[id].tsx:155,185-196 |
| W-6 | MEDIUM | Outlook add-in requests `ReadWriteMailbox`; only `ReadItem` is needed. | addins/outlook/manifest.xml:67,92 |
| W-7 | MEDIUM | Bulk ZIP import inflates the whole archive in memory with no size gate; parse errors are unhandled. | web/src/lib/bulkUpload/archive.ts:26-43; UploadWizard.tsx |
| W-8 | LOW | Apps Script `openLinkUrlPrefixes` hard-coded to the example domain. | addins/google/appsscript.json:22-25 |
| W-9 | LOW | Report CSV export has no formula-injection guard. | web/src/api/analytics.ts:86-94 |
| W-10 | LOW | Outlook/Word add-ins dump the 409 tenant-picker JSON instead of offering a picker. | addins/{outlook,word}/src/auth.ts |

Client-only gates the web seat asked the backend to confirm: records schedules/categories (compliance role), tenant KEK rotate/revoke (owner only), direct user create (password policy, no `owner`), role change (no self-demotion of the last owner, no escalation to owner), API-key scopes capped to the caller's role, add-in share links honour tenant share policy.

## C — collaboration and remaining shared packages

| ID | Sev | Finding | Where |
|---|---|---|---|
| C-1 | HIGH | Yjs rooms are per-pod with no cross-replica relay; Helm runs ≥2 replicas; each pod's full-state snapshot overwrites the other's, losing edits. | services/collaboration/src/yjs-server.js:57-67; yjs-persistence.js:53-100; values.yaml:496-506 |
| C-2 | HIGH | Room eviction never destroys the Y.Doc/Awareness; each closed room leaks a 3 s interval and the whole document until OOM. | yjs-server.js:62-63,429 |
| C-3 | HIGH | Collaboration authorizes through the policy HTTP check, which lacks lifecycle/clearance/inheritance context, and never re-checks during a session. | yjs-server.js:267-333; services/policy/internal/handler/http.go:220-228 |
| C-4 | MEDIUM | SMS MFA silently stubs when Twilio is unset and accepts `000000`; no code path ever activates an enrolled SMS/email method. | pkg/notifications/sms.go:85-112; services/auth/cmd/server/main.go:142 |
| C-5 | MEDIUM | A failed snapshot save zeroes the pending counter; no shutdown flush; up to 25 edits lost. | yjs-server.js:137-143,416-422; yjs-persistence.js:136-139 |
| C-6 | MEDIUM | `/ws` auth calls the auth service without the gateway signature: always 401. | services/collaboration/src/handler.js:28-43 |
| C-7 | MEDIUM | `/ws` presence and comment frames are not bound to an authorized document and are unbounded. | handler.js:169-263 |
| C-8 | MEDIUM | The subject-coverage gate checks a stale hand list; ~70 emitted Go subjects are not harvested. | pkg/events/coverage.go:75-201; coverage_test.go |
| C-9 | MEDIUM | S3 clients hard-code signing region `us-east-1`; real AWS in another region fails every request. | pkg/storage/s3.go:91-114 |
| C-10 | LOW | Any socket can overwrite or remove other peers' awareness state. | yjs-server.js:102-106,370-385 |
| C-11 | LOW | `pkg/dlp` is unused and its detectors and masking are wrong. | pkg/dlp/dlp.go |
| C-12 | LOW | Collaboration falls back to a hard-coded dev DSN when `SEDOC_DATABASE_URL` is unset. | yjs-persistence.js:17-21 |

## O — ops, monitoring, NATS, ansible, migrations hygiene

| ID | Sev | Finding | Where |
|---|---|---|---|
| O-1 | HIGH | Alert rules and Alertmanager config are never deployed; Alertmanager secrets are literal `${VAR}` strings. | deploy/monitoring/alertmanager.yml:256-274 |
| O-2 | HIGH | Availability/error-budget alerts filter on a `code` label that does not exist; `critical`/`warning` alerts (outbox DLQ, DB down) route to the email digest. | deploy/monitoring/alerts/tiered-alerts.yml; sedoc-rules.yml:82-103; alertmanager.yml:228-253 |
| O-3 | HIGH | 12 alert metrics and 27 dashboard metrics are never emitted; `event_bus_lag` and `db_connections` are never set; outbox publish failures, audit ingest errors and partition errors have no alert. | deploy/monitoring/**; pkg/metrics/metrics.go:50,111 |
| O-4 | HIGH | NATS runs with no auth or TLS, port 4222 is published, and tenant event-stream credentials are never validated. | deploy/nats/nats.conf; values.yaml:601-607; docker-compose.yml:340 |
| O-5 | HIGH | JetStream `LimitsPolicy`+`DiscardOld` and `MaxDeliver(5)` silently discard events, including audit evidence. | pkg/events/publisher.go:273-283; services/audit/internal/service/service.go:520 |
| O-6 | HIGH | Schema migrations create LOGIN roles with password `devpassword` in every environment. | document migrations 000001:45-52, 000065:23-33 |
| O-7 | HIGH | Helm and Ansible run as the table owner, so six ENABLE-but-not-FORCE tables (incl. `tenant_keks`) bypass RLS. | postgres-cluster.yaml:41-48; document 000004:33; intelligence 000001/000002; document 000021, 000066 |
| O-8 | HIGH | Ansible nightly `pg_dump` cannot write its file; the DR runbook names a file that is never produced. | deploy/ansible/roles/postgres/tasks/main.yml:134-141 |
| O-9 | HIGH | The UAE residency overlay's keys are not consumed by the chart or code (`env` list vs map, no egress policy, `me-central-1` rejected, `EnforceRegion` unwired). | deploy/regions/uae-central.yaml; templates/_service.tpl:63-66 |
| O-10 | MEDIUM | License trust root is the embedded dev public key with no override; `SEDOC_REQUIRE_LICENSE` unset in Helm/Ansible. | pkg/license/state.go:31-48 |
| O-11 | MEDIUM | Ansible: `sslmode=disable`, empty-secret defaults that crash-loop, unverified binary download, no migration step, ports open to any source, no gateway. | deploy/ansible/roles/** |
| O-12 | MEDIUM | Load-test Terraform: public EKS endpoint, internet-facing Grafana with a committed password, unlocked/unencrypted state. | deploy/load-test/terraform/** |
| O-13 | MEDIUM | Lock-heavy migrations (FKs and CHECKs without NOT VALID, non-concurrent indexes on large/partitioned tables). Applied migrations cannot be changed; the risk is in future ones. | document 000064, 000002, 000052; audit 000001 |
| O-14 | LOW | SOC 2 evidence script produces silently empty output under the prod role and queries a metric that does not exist; fixed `/tmp` path in `dms-provision.sh`. | scripts/soc2-evidence.sh:164-178; dms-provision.sh:22-24 |
