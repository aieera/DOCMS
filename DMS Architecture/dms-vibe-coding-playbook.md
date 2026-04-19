# DMS VIBE-CODING PLAYBOOK
## Complete End-to-End Prompts for Enterprise-Grade Document Management System
### Tool: Claude Code (Terminal) | Backend: Go | Team: 5-10 Developers

---

> **This document is 30 phases of copy-paste-ready prompts for Claude Code.**
> Each phase includes: the PROMPT, MANUAL REVIEW checklist, MANUAL BUILD items, PAYABLE ITEMS, and ACCEPTANCE TESTS.

---

## HOW TO USE THIS PLAYBOOK

1. **PROMPT** → Copy-paste into Claude Code. Each prompt is self-contained.
2. **MANUAL REVIEW** → Things your team MUST verify. Security-critical items AI gets wrong.
3. **MANUAL BUILD** → Things to build by hand. AI code here is too risky.
4. **PAYABLE ITEMS** → Services/licenses that cost money.
5. **ACCEPTANCE TEST** → How to know this phase is done.

**Team rules:**
- Never merge without human review of every DB query for `tenant_id` filtering
- Never merge AI-generated crypto code — rewrite using standard libraries
- Run every prompt in a feature branch, review the PR as a team

---

## PHASE MAP & PARALLEL TRACKS (6-9 months)

```
TRACK A (Backend)             TRACK B (Intelligence)      TRACK C (Frontend)
2-3 developers                2-3 developers              2-3 developers
═════════════════             ══════════════════           ═══════════════
P1: Infrastructure ─────────────────────────────────────── (ALL TEAM)
P2: Database/Tenancy          │                           │
P3: Auth Service              │                           P13: Web Shell + Auth UI
P4: Policy (AuthZ)            │                           P14: Doc Browser UI
P5: Document Service          P9: OCR Pipeline            P15: Search UI
P6: Storage Service           P10: Extraction/NER         P16: Doc Viewer
P7: Preview Service           P11: Semantic Search        P17: Workflow UI
P8: Search (OpenSearch)       P12: RAG & AI Q&A           P18: Admin Pages
P19: Workflow Engine          │                           P20: Collab UI
P21: Notification Service     │                           │
P22: Audit & Compliance       │                           P23: Mobile App
P24: Signature Service        │                           │
═══════════════════════════════════════════════════════════════════════
P25: On-Prem Helm Chart (Track A + SRE)
P26: SaaS Control Plane & Billing
P27: Security Hardening (ALL)
P28: Observability
P29: Integration Platform & Connectors
P30: Load Testing & Production Readiness
```

---

# ═══════════════════════════════════════════
# PHASE 1: MONOREPO & INFRASTRUCTURE
# ═══════════════════════════════════════════
**Duration:** 1 week | **Team:** ALL

## PROMPT 1

```
I'm building an enterprise Document Management System (DMS) in Go. Set up a production-grade monorepo.

Create this structure:

dms/
├── go.work
├── docker-compose.yml          # Postgres 16, Redis 7, OpenSearch 2.12, MinIO, NATS 2.10, Qdrant 1.7, Temporal
├── Makefile                    # build, test, lint, migrate, proto, docker
├── .golangci.yml               # strict: gocritic, gosec, errcheck, staticcheck
├── proto/dms/v1/               # All protobuf definitions
│   ├── common.proto            # Pagination, UUID, Error, RegionPin enum, Timestamp
│   ├── document.proto          # DocumentService RPCs
│   ├── storage.proto           # StorageService RPCs
│   ├── search.proto            # SearchService RPCs
│   ├── auth.proto              # AuthService RPCs
│   ├── policy.proto            # PolicyService RPCs
│   ├── intelligence.proto      # IntelligenceService RPCs
│   ├── workflow.proto          # WorkflowService RPCs
│   ├── audit.proto             # AuditService RPCs
│   ├── notification.proto
│   ├── signature.proto
│   └── billing.proto
├── pkg/                        # Shared Go libraries
│   ├── config/                 # Viper: env vars + YAML + defaults
│   ├── logger/                 # zerolog: structured JSON, tenant_id, correlation_id in every line
│   ├── middleware/
│   │   ├── tenant.go           # Extract tenant_id from token, SET app.current_tenant on every DB connection
│   │   ├── correlation.go      # Generate UUIDv7 correlation ID, propagate in headers + context
│   │   ├── requestlog.go       # Log method, path, status, latency, tenant_id, correlation_id
│   │   └── region.go           # Validate operations respect document's region_pin
│   ├── database/
│   │   ├── pool.go             # pgxpool with: MaxConns=50, MinConns=5, AfterConnect hook
│   │   ├── tenant.go           # WithTenant(ctx, pool, tenantID, fn) — sets RLS session var
│   │   ├── transaction.go      # WithTx(ctx, pool, fn) — auto-rollback on error
│   │   └── outbox.go           # Transactional outbox: insert event in same TX, poll+publish to NATS
│   ├── events/                 # NATS JetStream publisher/subscriber, CloudEvents envelope
│   ├── errors/                 # Domain errors: NotFound, Forbidden, Conflict, Validation → gRPC/HTTP codes
│   ├── crypto/                 # AES-256-GCM envelope encryption, KMS interface
│   ├── auth/                   # Token validation helpers
│   ├── storage/                # S3/MinIO client abstraction
│   ├── health/                 # /healthz, /readyz, /metrics endpoints
│   └── testutil/               # Test containers, fixtures, tenant factories
├── services/
│   ├── document/               # Go — Document CRUD, versioning, state machine
│   ├── storage/                # Go — Upload/download, dedup, encryption, virus scan
│   ├── search/                 # Go — OpenSearch + Qdrant hybrid search
│   ├── auth/                   # Go — AuthN, sessions, SSO, MFA, SCIM
│   ├── policy/                 # Go — AuthZ with OPA
│   ├── intelligence/           # Python — OCR, extraction, classification, embedding, RAG
│   ├── workflow/               # Go + Temporal — Workflow engine
│   ├── notification/           # Go — Email, push, in-app, Slack/Teams
│   ├── audit/                  # Go — Hash-chained audit log
│   ├── collaboration/          # Node.js — WebSocket, presence, comments
│   ├── signature/              # Go — eSignatures
│   ├── billing/                # Go — Metering, Stripe
│   ├── connector/              # Go — Integrations, webhooks
│   ├── preview/                # Python — Thumbnails, format conversion
│   └── gateway/                # Go or Kong config — API gateway
├── web/                        # React 18 + Vite + TypeScript + Tailwind + TanStack
├── mobile/                     # React Native (Expo)
├── desktop/                    # Tauri sync client
├── deploy/
│   ├── helm/                   # Kubernetes Helm chart
│   ├── ansible/                # Bare metal playbooks
│   └── terraform/              # Cloud infra
└── docs/

For each Go service (services/*/), create:
  cmd/server/main.go:
  - Graceful shutdown (SIGTERM/SIGINT)
  - Config from env vars (DATABASE_URL, NATS_URL, REDIS_URL, MINIO_ENDPOINT, LOG_LEVEL, SERVICE_NAME)
  - zerolog with service name + version
  - HTTP server (:8080), gRPC server (:9090), health/metrics (:8081)
  - pgxpool connection, NATS JetStream, Redis connection
  - Prometheus metrics endpoint

  internal/handler/     # gRPC + REST handlers
  internal/service/     # Business logic
  internal/repository/  # Data access
  internal/model/       # Domain models
  migrations/           # SQL migrations (golang-migrate format)
  Dockerfile            # Multi-stage: golang:1.22-alpine → alpine:3.19, non-root user

Libraries:
  github.com/jackc/pgx/v5, github.com/rs/zerolog, github.com/spf13/viper,
  google.golang.org/grpc, github.com/grpc-ecosystem/grpc-gateway/v2,
  github.com/nats-io/nats.go, github.com/redis/go-redis/v9,
  github.com/prometheus/client_golang, github.com/golang-migrate/migrate/v4

CRITICAL in TenantExtractor middleware:
- Extract tenant_id from authenticated session context
- On EVERY database connection checkout: exec SET app.current_tenant = '{tenant_id}'
- If tenant_id is empty, REJECT the request (never execute queries without tenant context)
- This is defense-in-depth alongside Postgres RLS
```

## MANUAL REVIEW
- [ ] docker-compose.yml starts all services (`docker compose up -d` → all healthy)
- [ ] TenantExtractor sets Postgres session var on EVERY connection, not just first
- [ ] All Dockerfiles use non-root USER in runtime stage
- [ ] Health checks test actual dependencies (Postgres, Redis), not just 200

## PAYABLE ITEMS — Phase 1
| Item | Cost |
|------|------|
| GitHub Team | $4/user/month |
| Domain name | $12/year |
| Dev cloud server (optional) | $50-100/month |

---

# ═══════════════════════════════════════════
# PHASE 2: DATABASE SCHEMA & MULTI-TENANCY
# ═══════════════════════════════════════════
**Duration:** 1-2 weeks | **Team:** 1-2 backend devs

## PROMPT 2

```
Create PostgreSQL migrations for the DMS (golang-migrate format: 000001_init.up.sql / .down.sql).

CRITICAL RULES:
- Every table has tenant_id as FIRST column: PRIMARY KEY (tenant_id, id)
- Every table has RLS enabled + policy filtering by current_setting('app.current_tenant')::uuid
- No CASCADE deletes — all deletes are soft (deleted_at TIMESTAMPTZ)
- UUIDs for all IDs, TIMESTAMPTZ for all timestamps
- JSONB for flexible fields with GIN indexes
- Cursor-based pagination everywhere (never OFFSET)

Create these 39 tables in order:

1. organizations: id PK, name, slug UNIQUE, plan (standard/enterprise/dedicated), settings JSONB, region, created_at, updated_at, deleted_at

2. users: (tenant_id, id) PK, email, display_name, password_hash, avatar_url, role (owner/admin/member/guest), status (active/suspended/deactivated), mfa_enabled, mfa_secret, last_login_at, settings JSONB, created_at, updated_at, deleted_at. UNIQUE(tenant_id, email)

3. groups: (tenant_id, id) PK, name, description, UNIQUE(tenant_id, name)
4. group_members: (tenant_id, group_id, user_id) PK
5. workspaces: (tenant_id, id) PK, name, description, settings JSONB, created_by
6. workspace_members: (tenant_id, workspace_id, user_id) PK, role (admin/member/viewer)
7. folders: (tenant_id, id) PK, workspace_id, parent_folder_id (self-ref), name, path (ltree), depth, created_by. GiST index on path.

8. documents: (tenant_id, id) PK, workspace_id, folder_id, title, description, lifecycle_state CHECK(IN draft,in_review,active,superseded,retained,archived,disposed,legal_hold), region_pin NOT NULL, custom_metadata JSONB DEFAULT '{}', tags TEXT[], current_version_id, document_class, classification_confidence, sha256_hash, total_size_bytes, mime_type, created_by, created_at, updated_at, deleted_at.
   INDEXES: (tenant_id, workspace_id, folder_id), (tenant_id, lifecycle_state), GIN on custom_metadata, GIN on tags, (tenant_id, created_at DESC), (tenant_id, document_class)

9. versions: (tenant_id, id) PK, document_id, version_number, content_blob_id, size_bytes, mime_type, sha256_hash, created_by, change_summary. UNIQUE(tenant_id, document_id, version_number)

10. content_blobs: id PK (not tenant-scoped PK for dedup), tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, storage_class, size_bytes, mime_type, encryption_key_id, reference_count DEFAULT 1. UNIQUE(tenant_id, sha256_hash)

11. permissions: (tenant_id, id) PK, resource_type, resource_id, principal_type (user/group), principal_id, capability (view/edit/delete/share/admin), granted_by, granted_at, expires_at, valid_from DEFAULT now(), valid_to. UNIQUE constraint on (tenant_id, resource_type, resource_id, principal_type, principal_id, capability)

12. share_links: (tenant_id, id) PK, document_id, created_by, token UNIQUE, password_hash, expires_at, max_views, view_count DEFAULT 0, permissions TEXT[], is_active DEFAULT true

13. comments: (tenant_id, id) PK, document_id, version_id, parent_comment_id, author_id, body, is_resolved, resolved_by, resolved_at

14. annotations: (tenant_id, id) PK, document_id, version_id, page_number, annotation_type, annotation_data JSONB

15. tags_catalog: (tenant_id, id) PK, name UNIQUE per tenant, color
16. legal_holds: (tenant_id, id) PK, name, description, matter_reference, applied_by, applied_at, released_by, released_at, is_active DEFAULT true
17. legal_hold_documents: (tenant_id, hold_id, document_id) PK
18. retention_policies: (tenant_id, id) PK, name, document_class_filter, tag_filter TEXT[], retain_days, then_action, archive_days

19. audit_events: id PK (not tenant-scoped for append performance), tenant_id, actor_id, actor_type, action, resource_type, resource_id, metadata JSONB, ip_address INET, user_agent, previous_hash, event_hash. PARTITION BY RANGE(created_at) monthly. INDEX on (tenant_id, created_at DESC), (tenant_id, resource_type, resource_id), (tenant_id, actor_id)

20. sessions: id PK, tenant_id, user_id, token_hash UNIQUE, ip_address, user_agent, expires_at, last_activity_at
21. api_keys: (tenant_id, id) PK, user_id, name, key_hash UNIQUE, key_prefix, scopes TEXT[], expires_at, revoked_at
22. upload_sessions: (tenant_id, id) PK, document_id, filename, total_size, mime_type, upload_type, storage_region, status, parts_completed, parts_total, expires_at
23. ocr_results: (tenant_id, id) PK, version_id, page_number, text_content, confidence, language, bounding_boxes JSONB, processing_time_ms, engine
24. extraction_results: (tenant_id, id) PK, version_id, extraction_type, fields JSONB, confidence, method, cost_cents
25. document_chunks: (tenant_id, id) PK, document_id, version_id, chunk_index, text_content, token_count, embedding_model, page_numbers INT[]
26. entities: (tenant_id, id) PK, document_id, version_id, entity_type, entity_value, start_offset, end_offset, page_number, confidence
27. document_fingerprints: (tenant_id, document_id) PK, minhash_signature BYTEA, simhash BIGINT
28. duplicate_candidates: (tenant_id, id) PK, document_id, candidate_id, similarity_score, method, status (pending/confirmed/dismissed)
29. workflow_definitions: (tenant_id, id) PK, name, description, definition JSONB, is_active
30. workflow_instances: (tenant_id, id) PK, definition_id, document_id, status, current_step, started_by, started_at, completed_at, result JSONB
31. workflow_tasks: (tenant_id, id) PK, instance_id, step_name, assignee_id, status (pending/completed/rejected/delegated/escalated), due_at, completed_at, outcome, notes. INDEX(tenant_id, assignee_id, status)
32. notification_preferences: (tenant_id, user_id, channel, event_type) PK, is_enabled
33. notifications: (tenant_id, id) PK, user_id, title, body, link, is_read, event_type, read_at. INDEX(tenant_id, user_id, is_read, created_at DESC)
34. signature_requests: (tenant_id, id) PK, document_id, version_id, requested_by, status, provider, provider_request_id, expires_at
35. signature_signers: (tenant_id, id) PK, request_id, signer_email, signer_name, role, order_index, status, signed_at, certificate_id
36. webhook_subscriptions: (tenant_id, id) PK, url, events TEXT[], secret, is_active
37. webhook_deliveries: (tenant_id, id) PK, subscription_id, event_type, payload JSONB, status, attempts, next_retry_at, response_status
38. connector_configs: (tenant_id, id) PK, connector_type, config JSONB (encrypted), is_active
39. outbox: id BIGSERIAL PK, tenant_id, event_type, aggregate_type, aggregate_id, payload JSONB, published BOOLEAN DEFAULT false, created_at. INDEX(published, created_at) WHERE NOT published

For EVERY table with tenant_id:
  ALTER TABLE {t} ENABLE ROW LEVEL SECURITY;
  ALTER TABLE {t} FORCE ROW LEVEL SECURITY;
  CREATE POLICY {t}_isolation ON {t} USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
  CREATE POLICY {t}_isolation_insert ON {t} FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

Create roles: dms_app (SELECT, INSERT, UPDATE — no DELETE), dms_readonly.
Create updated_at trigger function, apply to all tables with updated_at.
Create monthly partition creation function for audit_events.
```

## MANUAL REVIEW
- [ ] **CRITICAL:** RLS uses `current_setting('app.current_tenant', true)` — the `true` returns NULL (not error) if unset, so policy returns NO rows. This is the safe default.
- [ ] No CASCADE DELETE anywhere
- [ ] All foreign keys have indexes
- [ ] Composite PKs are (tenant_id, id) not (id, tenant_id)
- [ ] Run `EXPLAIN ANALYZE` on 5 most common queries with 100K test rows

## MANUAL BUILD
- **RLS integration test:** Insert as tenant A, query as tenant B → must return 0 rows. Hand-write this test.
- **Migration rollback test:** Apply all ups, seed data, apply all downs, re-apply ups. Verify data integrity.

## PAYABLE ITEMS
None — PostgreSQL is free.

---

# ═══════════════════════════════════════════
# PHASE 3: AUTHENTICATION SERVICE
# ═══════════════════════════════════════════
**Duration:** 2 weeks | **Team:** 1-2 devs

## PROMPT 3

```
Build the Auth Service in services/auth/. Handles registration, login, sessions, MFA, SSO, SCIM.

Stack: Go 1.22, pgx, go-redis, bcrypt (cost 12), pquerna/otp (TOTP), go-webauthn, crewjam/saml, coreos/go-oidc

REST endpoints (external-facing, not gRPC):

POST /api/v1/auth/register — {email, password, display_name, tenant_slug}
  Validate: email format, password (min 12 chars, 1 upper/lower/digit/special), tenant exists
  Hash with bcrypt cost 12. Rate limit: 5/min/IP.

POST /api/v1/auth/login — {email, password, tenant_slug}
  If MFA enabled: return {mfa_required: true, mfa_session_token}
  If no MFA: create session:
    - Generate 256-bit crypto/rand token
    - SHA-256 hash before storing in sessions table + Redis (key: session:{hash}, TTL: 24h)
    - Store: token_hash, user_id, tenant_id, ip, user_agent, expires_at
    - Return session_token (plaintext, shown once) + Set-Cookie (httpOnly, secure, sameSite=strict)
  On failure: generic "invalid credentials" (never reveal if email exists)
  Rate limit: 10/min/IP, lockout after 5 failures for 15 minutes.
  Audit log every attempt (success AND failure).

POST /api/v1/auth/mfa/verify — {mfa_session_token, totp_code}
POST /api/v1/auth/mfa/setup — requires auth → generate TOTP secret, QR URI, 8 recovery codes (bcrypt hashed)
POST /api/v1/auth/mfa/disable — requires auth + TOTP code
POST /api/v1/auth/logout — delete session from DB + Redis
POST /api/v1/auth/refresh — extend session if within 1h of expiry (sliding window, max 7 days absolute)

GET /api/v1/auth/session/validate (internal gRPC) — lookup Redis then DB, return {user_id, tenant_id, roles, groups}

POST /api/v1/auth/api-keys — {name, scopes[]} → generate "dms_" + 48 random chars, SHA-256 hash before storing, return plaintext once. Limit: 20/user.
DELETE /api/v1/auth/api-keys/{id}

SAML 2.0:
  GET /api/v1/auth/saml/{tenant_slug}/metadata → SP metadata
  POST /api/v1/auth/saml/{tenant_slug}/acs → validate assertion signature, one-time consumption (store assertion ID, reject replays), extract email+groups, find-or-create user, create session, redirect with cookie
  GET /api/v1/auth/saml/{tenant_slug}/login → redirect to IdP

OIDC:
  GET /api/v1/auth/oidc/{tenant_slug}/login → redirect with PKCE
  GET /api/v1/auth/oidc/{tenant_slug}/callback → exchange code, extract user info, create session

SCIM 2.0 (bearer token auth configured in IdP):
  /api/v1/scim/v2/Users — CRUD + List with eq/ne/co/sw filtering on userName, emails, active
  /api/v1/scim/v2/Groups — CRUD + PATCH membership

Session middleware:
  1. Extract token from Authorization: Bearer or Cookie
  2. Redis lookup (<1ms), Postgres fallback
  3. Set user_id, tenant_id, roles, groups in context
  4. 401 if invalid/expired
  5. Sliding window extension

SECURITY RULES:
  - NEVER log passwords, tokens, API keys, MFA secrets
  - NEVER return password_hash in responses
  - crypto/subtle.ConstantTimeCompare for ALL secret comparisons
  - Validate SAML NotBefore/NotOnOrAfter timestamps
  - Concurrent session limit: 5 per user (configurable)
  - Session binding: validate IP range + user agent match

Write tests: login flow, MFA setup+verify+recovery, session lifecycle, API key CRUD, rate limiting, SCIM provisioning.
```

## MANUAL REVIEW
- [ ] bcrypt cost is 12+ (AI often uses 10)
- [ ] Session tokens use crypto/rand, not math/rand
- [ ] Tokens SHA-256 hashed before DB storage
- [ ] ConstantTimeCompare for all secret comparisons
- [ ] SAML assertion one-time consumption (replay prevention)
- [ ] Grep codebase for "password" in log statements → must find 0
- [ ] Failed login doesn't reveal whether email exists
- [ ] MFA recovery codes are bcrypt hashed

## MANUAL BUILD
- SAML certificate management + rotation procedure
- OIDC PKCE verification (code_verifier/code_challenge S256)
- Comprehensive audit event wiring for all auth events

## PAYABLE ITEMS
| Item | Cost |
|------|------|
| Twilio Verify (SMS MFA) | $0.05/verification |
| SES/SendGrid (email OTP) | $0.001/email |

---

# ═══════════════════════════════════════════
# PHASE 4: AUTHORIZATION (OPA)
# ═══════════════════════════════════════════
**Duration:** 2 weeks | **Team:** 1-2 devs

## PROMPT 4

```
Build the Policy Service in services/policy/ using Go + embedded OPA (github.com/open-policy-agent/opa/rego).

This is HOT PATH — p99 < 5ms required. Embed OPA as Go library, NOT sidecar.

gRPC API:
- CheckPermission(subject_type, subject_id, action, resource_type, resource_id, context) → {allowed, reason}
- BatchCheckPermissions(checks[]) → results[]
- GrantPermission / RevokePermission
- ListPermissions(resource_type, resource_id)
- GetResourcePermissionGroups(resource_type, resource_id) → group IDs with VIEW+ access (for search indexing)

OPA Rego policy (embed as bundle):

```rego
package dms.authz
import future.keywords.in
import future.keywords.if

default allow := false

# Direct permission
allow if {
    some p in data.permissions
    p.principal_type == input.subject_type
    p.principal_id == input.subject_id
    p.resource_type == input.resource_type
    p.resource_id == input.resource_id
    capability_includes(p.capability, input.action)
    not expired(p)
}

# Group membership
allow if {
    input.subject_type == "user"
    some gid in data.user_groups[input.subject_id]
    some p in data.permissions
    p.principal_type == "group"
    p.principal_id == gid
    p.resource_type == input.resource_type
    p.resource_id == input.resource_id
    capability_includes(p.capability, input.action)
    not expired(p)
}

# Workspace admin
allow if {
    input.subject_type == "user"
    some wm in data.workspace_members
    wm.user_id == input.subject_id
    wm.role == "admin"
    wm.workspace_id == input.context.workspace_id
}

# Org admin/owner
allow if { input.user_role in {"admin", "owner"} }

# Folder cascades to documents
allow if {
    input.resource_type == "document"
    some p in data.permissions
    p.principal_type == input.subject_type; p.principal_id == input.subject_id
    p.resource_type == "folder"; p.resource_id == input.context.folder_id
    capability_includes(p.capability, input.action)
    not expired(p)
}

# Hierarchy: admin > delete > edit > share > view
capability_includes(granted, requested) if {
    h := {"admin": 5, "delete": 4, "edit": 3, "share": 2, "view": 1}
    h[granted] >= h[requested]
}

expired(p) if { p.expires_at != null; time.now_ns() > time.parse_rfc3339_ns(p.expires_at) }
```

Cache: Redis with 60s TTL. Key: perm:{tenant_id}:{resource_type}:{resource_id}
On GrantPermission/RevokePermission: invalidate cache, publish permission.changed.v1 to NATS.

Performance test: 1000 checks in < 5 seconds.
```

## MANUAL REVIEW
- [ ] **CRITICAL:** "No permissions found" = DENY (verify default allow := false works)
- [ ] Cache invalidation covers ALL paths: grant, revoke, group membership change, workspace membership change
- [ ] Rego policy doesn't have catch-all allow rule

## MANUAL BUILD
- Rego policy review by security-focused developer
- Temporal permission queries (valid_from/valid_to)

---

# ═══════════════════════════════════════════
# PHASE 5-8: DOCUMENT, STORAGE, PREVIEW, SEARCH SERVICES
# ═══════════════════════════════════════════

Due to document length, I'll provide the key prompt essence for each. Each prompt should be run as a separate Claude Code session with full context.

## PROMPT 5 — Document Service (Key Points)

```
Build services/document/ — the core Document CRUD service.

REST API: Folders (CRUD), Documents (CRUD + move + lifecycle + batch metadata), Versions, Tags, Share Links.

CRITICAL BUSINESS RULES:
1. LIFECYCLE STATE MACHINE — valid transitions only:
   draft→in_review, in_review→draft/active, active→superseded/retained,
   superseded→retained, retained→archived/active, archived→disposed.
   Any state→legal_hold (except disposed). legal_hold→previous state on release.

2. LEGAL HOLD blocks: deletion, content modification, metadata affecting retention, region moves.
   Allows: viewing, downloading, comments, annotations.

3. REGION PIN: set at creation, NEVER changeable. All operations respect region_pin.

4. CUSTOM METADATA: validate against tenant's JSON Schema (tenant_metadata_schemas table).
   Required fields enforced only on transition to 'active' (not draft).

5. SOFT DELETE only. Hard delete = separate admin endpoint + audit + reason.

6. FOLDER PATH: ltree extension. Max depth 20. On folder move, update all descendants atomically.

7. SHARE LINKS: crypto/rand tokens, constant-time validation, password optional, max_views, expiry.

8. EVENTS: every write → outbox pattern → NATS. Events: document.created/updated/moved/deleted, version.created, share_link.created, document.state_changed.

9. PERMISSION CHECKS: call Policy Service gRPC before every operation.

10. OPTIMISTIC LOCKING on updates (version number check).

Write table-driven tests for EVERY state machine transition (valid + invalid).
```

## PROMPT 6 — Storage Service (Key Points)

```
Build services/storage/ — upload/download with S3/MinIO.

POST /api/v1/uploads/initiate — {document_id, filename, size, mime_type, sha256_hash, region_pin}
  - Permission check, size validation (5GB standard, 50GB enterprise)
  - Mime-type allowlist (block executables)
  - Dedup: if sha256 exists for tenant+region, return deduplicated=true
  - Strategy: <100MB single presigned PUT, 100MB-5GB multipart (100MB parts), >5GB TUS protocol
  - Region-aware: use document's region_pin to select correct bucket

POST /api/v1/uploads/{id}/complete
  - Async pipeline: ClamAV virus scan → mime re-verify (magic bytes) → SHA-256 verify → create content_blob → publish version.uploaded.v1
  - If malware: quarantine bucket, notify user, audit event

GET /api/v1/downloads/{doc_id}/versions/{ver_id}
  - Presigned GET URL (5-min expiry) for internal users
  - Proxied download for share links (watermark with email+timestamp on PDFs)

ENCRYPTION: AES-256-GCM envelope encryption. DEK per blob, DEK encrypted with tenant KEK via KMS.
KMS interface: GenerateDEK(kekID) → (plaintext, encrypted), DecryptDEK(kekID, encrypted) → plaintext
Implementations: LocalKeyManager (dev), VaultKeyManager, AWSKMSKeyManager

STORAGE LIFECYCLE (daily job): active+no access 90d→warm, archived→cold, disposed+retention→deep cold
```

## PROMPT 7 — Preview Service (Python, Key Points)

```
Build services/preview/ in Python 3.12 + FastAPI + Celery.

NATS consumer: dms.version.uploaded.v1 → generate previews.

Per format:
- PDF: page 1 thumbnail 256x256 JPEG + first 10 pages 1200px PNG (pdf2image/poppler)
- Office (DOCX/XLSX/PPTX): LibreOffice headless → PDF → same as above. 60s timeout.
- Images: resize 256x256 thumb + 2000px preview. Strip EXIF (privacy).
- Video: ffmpeg frame at 5s mark. HLS transcode optional.
- Text/Code: extract first 1000 chars as text snippet.

Upload to dms-{region}-previews bucket. Publish version.preview_ready.v1.

Dockerfile: python:3.12-slim + libreoffice-core + poppler-utils + ffmpeg.
Resource limits: 2GB RAM, 10GB temp disk, 4 concurrent jobs.
ALWAYS clean up temp files, even on error.
```

## PROMPT 8 — Search Service (Key Points)

```
Build services/search/ — hybrid search using OpenSearch + Qdrant.

OpenSearch index mapping: tenant_id (keyword), document_id, title (text + keyword + autocomplete edge_ngram), description, content (full OCR text), tags, document_class, lifecycle_state, region_pin, custom_metadata (dynamic object), readable_by (keyword array), extracted_entities, created_at, size_bytes.

POST /api/v1/search — {query, filters, facets, sort, page_size, page_token, search_mode}
  Build query:
  - multi_match on title (3x boost), description (1.5x), content (1x), tags (2x)
  - CRITICAL FILTER: terms on readable_by = [user's groups + user_id + "everyone"]
  - CRITICAL FILTER: term on tenant_id
  - Aggregations for facets (ALSO filtered by readable_by — no leaking!)
  - Highlight on title + content

GET /api/v1/search/autocomplete?q=X — completion suggester, <50ms.

INDEXING PIPELINE (NATS consumers):
  document.created/updated → index in OpenSearch with readable_by from Policy Service
  version.ocr_completed → update content field
  permission.changed → update readable_by for affected documents

Hybrid search (Phase 11 adds Qdrant):
  BM25 results + Qdrant ANN results → Reciprocal Rank Fusion (k=60).

Performance: p99 < 300ms search, p99 < 50ms autocomplete.
```

## MANUAL REVIEW — Phase 5-8
- [ ] Every Document Service endpoint has permission check
- [ ] Legal hold blocks ALL modifications
- [ ] region_pin cannot change after creation
- [ ] Presigned URLs use correct region bucket
- [ ] Virus scan cannot be bypassed
- [ ] Search readable_by filter on EVERY query INCLUDING aggregations
- [ ] Preview strips EXIF from images
- [ ] No OFFSET pagination anywhere

## MANUAL BUILD — Phase 5-8
- State machine transition matrix test (all state × action combinations)
- KMS integration (VaultKeyManager, AWSKMSKeyManager)
- Permission-filtered aggregation test (3 users, 100 docs, verify facet counts)

## PAYABLE ITEMS — Phase 5-8
| Item | Cost |
|------|------|
| AWS S3 | $0.023/GB/month |
| AWS KMS | $1/key/month + $0.03/10K req |
| HashiCorp Vault | $0.03/op or self-hosted free |
| AWS OpenSearch | ~$220/month (3-node min) |
| CloudFront CDN | $0.085/GB transfer |
| ClamAV | Free |
| MinIO | Free (AGPL) |

---

# ═══════════════════════════════════════════
# PHASE 9-12: INTELLIGENCE, SEMANTIC SEARCH, RAG, NOTIFICATIONS
# ═══════════════════════════════════════════

## PROMPT 9 — OCR Pipeline (Python Intelligence Service)

```
Build services/intelligence/ in Python 3.12 + FastAPI + Celery (Redis broker).

OCR pipeline (NATS consumer: dms.version.uploaded.v1):
1. Download file from S3
2. PDF with text layer → extract directly (pymupdf). Scanned PDF/images → OCR.
3. Run Surya OCR (primary): surya.ocr.run_ocr() with language detection.
   If confidence < 0.7 per page, fallback to PaddleOCR.
4. Arabic: Surya handles natively. Preserve RTL ordering.
5. Store in ocr_results: tenant_id, version_id, page_number, text_content, confidence, language, bounding_boxes JSONB, engine.
6. Publish: version.ocr_completed.v1

Performance: <3s/page CPU, <0.5s/page GPU. 100 docs/min on 4-GPU fleet.
Celery: concurrency 2/GPU worker, 10min task timeout, 3 retries.

Dockerfile: nvidia/cuda:12.2.0-runtime-ubuntu22.04 (GPU) or python:3.12-slim (CPU).
Bundle Surya + PaddleOCR models at build time (no runtime download for air-gapped).
~4GB GPU image, ~2GB CPU image.
```

## PROMPT 10 — Extraction, Classification, NER (extend Intelligence Service)

```
Add to services/intelligence/:

classify_document (triggered by version.ocr_completed.v1):
  Tier 1: Rule-based keywords ($0/doc, 90% coverage)
  Tier 2: Fine-tuned DistilBERT ($0.001/doc, confidence>0.8)
  Tier 3: LLM zero-shot via LiteLLM ($0.02/doc, fallback)
  Store: document_class + confidence in documents table.

extract_fields (triggered after classification):
  Invoice: regex first (invoice_number, amounts, dates), LLM fallback if confidence<0.8
  Contract: LLM extraction (parties, dates, governing law, value, key clauses)
  Other: basic entity extraction only.

detect_entities (NER):
  SpaCy en_core_web_trf: PERSON, ORG, DATE, MONEY, GPE
  Regex: SSN (r'\b\d{3}-\d{2}-\d{4}\b'), credit card (Luhn-validated), email, phone
  Flag PII documents for DLP review.

detect_duplicates:
  MinHash (128 perms) + LSH for near-dup (Jaccard>0.8)
  Store in duplicate_candidates table.

LLM routing via LiteLLM:
  Per-tenant config in connector_configs: {llm_provider, model, api_key (encrypted), base_url}
  Track tokens + cost per tenant, publish billing.llm_usage.v1
```

## PROMPT 11 — Semantic Search (extend Intelligence + Search)

```
Add embedding generation to Intelligence Service:
  Triggered by version.ocr_completed.v1.
  Chunk text: 512 tokens, 64 overlap, preserve sentence boundaries.
  Embed with sentence-transformers/all-MiniLM-L6-v2 (384 dims). Batch 32.
  Store chunks in document_chunks table.
  Upsert to Qdrant collection with payload: tenant_id, document_id, readable_by.

Update Search Service for hybrid search:
  search_mode="hybrid":
    1. BM25 query → OpenSearch → ranked list A
    2. Embed query → Qdrant ANN search (filter: tenant_id + readable_by) → ranked list B
    3. RRF fusion: score = Σ 1/(60 + rank_i)
    4. Return merged top 20
```

## PROMPT 12 — RAG & AI Q&A + Notifications

```
Add RAG to Intelligence Service:

POST /api/v1/intelligence/ask
  {question, scope (workspace/folder/document), scope_id, conversation_id}
  Pipeline:
  1. Permission check on scope
  2. Embed question
  3. Retrieve: Qdrant top 50 (filtered by tenant + scope + readable_by) + OpenSearch top 50
  4. RRF fusion → top 20
  5. Re-rank with cross-encoder (ms-marco-MiniLM) → top 5
  6. Context: top 5 chunks (max 4000 tokens)
  7. LLM call: "Answer based ONLY on provided documents. Cite sources as [Document: title, Page: X]."
  8. Parse citations, map to document IDs
  9. Store conversation turn, meter tokens
  Response: {answer, citations[], model_used, tokens_used, cost_cents}

POST /api/v1/intelligence/summarize — map-reduce for long docs
POST /api/v1/intelligence/redact — NER→candidates→human review→burn-in (pymupdf black rectangles)→new version

---

Build Notification Service (services/notification/ in Go):
NATS consumer: dms.notify.*
Channels: in_app (insert to notifications table + Redis pub/sub), email (SES/SendGrid HTML templates), push (FCM/APNs), Slack (webhook), Teams (webhook), SMS (Twilio, critical only, max 10/day/user).
Dedup: batch same event type within 5 minutes.
Quiet hours: configurable per user.
REST: GET notifications, PATCH read, POST read-all, GET/PUT preferences.
```

## MANUAL REVIEW — Phase 9-12
- [ ] LLM API keys encrypted in DB
- [ ] Qdrant readable_by filter matches OpenSearch logic
- [ ] NER doesn't log detected PII values
- [ ] RAG only retrieves chunks user has permission to see
- [ ] Prompt injection test: doc with "ignore instructions" → AI shouldn't comply
- [ ] Email templates don't include sensitive content in body
- [ ] SMS rate limiting works

## MANUAL BUILD
- OCR accuracy benchmark (100 docs, 5 languages, measure CER/WER)
- LLM prompt engineering (iterative refinement with real docs, 2 weeks)
- Arabic OCR fine-tuning if accuracy <95%

## PAYABLE ITEMS — Phase 9-12
| Item | Cost |
|------|------|
| GPU instances (g5.xlarge) | $1.01/hr/GPU (need 2-4) |
| OpenAI API | $5/1M input tokens |
| Anthropic API | $3/1M input tokens |
| AWS SES | $0.10/1K emails |
| Twilio SMS | $0.0079/SMS |
| Firebase Cloud Messaging | Free |
| Qdrant Cloud | $0.025/GB/month (or self-hosted free) |

---

# ═══════════════════════════════════════════
# PHASE 13-18: FRONTEND
# ═══════════════════════════════════════════

## PROMPT 13 — React App Shell

```
Build web/ using React 18 + TypeScript + Vite + TanStack Router + TanStack Query + Tailwind CSS + Zustand.

Install: @tanstack/react-query, @tanstack/react-router, tailwindcss, @radix-ui/react-*, zustand, react-hot-toast, lucide-react, dayjs, react-dropzone, @dnd-kit/core, zod, react-intl

Structure: routes/ (file-based), components/ui/ (Radix primitives), components/layout/ (Sidebar, Header, FolderTree, Breadcrumb), components/documents/, components/search/, components/workflow/, components/admin/, hooks/, api/, store/, lib/, locales/

Design: Professional enterprise — slate/zinc grays + deep blue #1E40AF accent. Inter for UI, JetBrains Mono for code. Dark mode via CSS vars. WCAG 2.2 AA. RTL support (CSS logical properties + Tailwind rtl:). NOT generic Bootstrap.

Build:
1. Login + Register with zod validation
2. Authenticated layout: collapsible sidebar (workspaces + folder tree) + header (search bar + notifications bell + avatar menu)
3. Dashboard: recent docs, quick actions, storage usage
4. Workspace list + create
5. API client (Axios): auth header, 401→login, 403→toast, 429→toast, 5xx→retry

Auth tokens: httpOnly cookie OR in-memory only. NEVER localStorage.
```

## PROMPT 14-18 (summarized — run each separately)

**P14 - Document Browser:** Grid view (thumbnails), table view (sortable columns), drag-drop upload with progress, bulk select+actions, context menu, breadcrumbs, empty states.

**P15 - Search UI:** Autocomplete dropdown with keyboard nav, results page with highlights, facet filters sidebar (type, tags, date, workspace, author), saved search CRUD.

**P16 - Document Viewer:** Full-page modal. PDF.js (nav, zoom, text select, search-in-PDF). Image viewer (zoom/pan). Text/code with syntax highlighting. Annotation toolbar. Version compare side-by-side. Metadata panel. Comment thread. Activity timeline.

**P17 - Workflow Designer:** ReactFlow drag-drop nodes (approval, review, notification, condition). Properties panel. Save/load as JSON. Task list ("My Tasks" view).

**P18 - Admin Pages:** User management table (invite/edit/deactivate). Group management. Permission editor. Audit log viewer with filters + export. Retention policy CRUD. Compliance dashboard with charts. Settings page.

## MANUAL REVIEW — Frontend
- [ ] Auth tokens NOT in localStorage
- [ ] All API calls include tenant context
- [ ] Document viewer blocks download when restricted
- [ ] Search results don't flash then disappear
- [ ] RTL works with Arabic locale
- [ ] Keyboard navigation in folder tree, search, doc list
- [ ] Run axe-core accessibility audit on every page

## MANUAL BUILD
- PDF annotation coordinate mapping (screen ↔ PDF coords across zoom levels)
- Design polish: spacing, animations, loading/error/empty states (2 weeks dedicated)

## PAYABLE ITEMS
| Item | Cost |
|------|------|
| ReactFlow Pro (optional) | $249/year |
| Vercel hosting (optional) | $20/month |

---

# ═══════════════════════════════════════════
# PHASE 19-24: WORKFLOW, COLLAB, AUDIT, SIGNATURES, COMPLIANCE, MOBILE
# ═══════════════════════════════════════════

## PROMPT 19 — Workflow Engine (Go + Temporal)

```
Build services/workflow/ with Go + Temporal SDK.
Add temporalio/auto-setup:1.22 to docker-compose.

Temporal workflows:
- ApprovalWorkflow: sequential steps, each with signal channel (approve/reject/delegate) + timer (escalation).
  On reject → document back to draft. On all approved → document to active.
- ParallelApprovalWorkflow: multiple approvers, configurable require_all or require_any.
- ConditionalWorkflow: branch on document metadata (e.g., contract_value > 100K → add VP step).

REST API: CRUD workflow definitions (JSON schema), start/cancel instances, complete steps, list my tasks (across all workflows).

Workflow definition JSON: steps[]{id, name, type, assignee_type/value, timeout_hours, escalate_to, condition, mode}.
```

## PROMPT 20 — Collaboration Service (Node.js WebSocket)

```
Build services/collaboration/ in Node.js 22 + ws library.

WebSocket on :8083/ws. Auth: first message {type:"auth", token} validated via Auth Service gRPC.
Channels: subscribe/unsubscribe to document:{doc_id}.
Features: presence (who's viewing which doc/page), live comments, real-time notification push.
Redis pub/sub for cross-instance communication.
Heartbeat: 30s ping, 10s pong timeout.
Scale: 10K connections/instance, sticky sessions via Envoy.
```

## PROMPT 21 — Audit Service

```
Build services/audit/ — tamper-evident hash-chained audit log.

NATS consumer: all domain events → derive audit entries.
Hash chain: event_hash = SHA-256(previous_hash + tenant_id + actor + action + resource + timestamp).
Per-tenant mutex (Redis SETNX) for ordering.

REST: query events (filterable), export CSV/JSON, verify chain integrity, generate SOC 2 report, GDPR data subject export/anonymize.
Audit events partition by month. Append-only (no update/delete even for admins).
```

## PROMPT 22 — Compliance Engine

```
Extend Document Service with compliance features:

Retention enforcement (daily 2AM cron): find docs matching policy filters past retain_days, NOT under legal hold → transition to retained/disposition queue.
Disposition review queue: admin approves → disposed → 30-day grace → hard delete.

Legal holds: POST create (by doc IDs or search query), DELETE release. Transitions docs to legal_hold state.

Data residency dashboard: per-region doc count + storage + violations.
Data subject requests: export/rectify/erase/portability with legal hold precedence check.
Compliance report: lifecycle distribution, upcoming expirations, active holds, encryption status.
```

## PROMPT 23 — Signature Service

```
Build services/signature/ — eSignatures.

Internal signing: draw/type signature capture → embed in PDF with PAdES format (PKCS#7 + TSA timestamp + OCSP for LTV). pymupdf or go-pdfium for PDF manipulation.
External: DocuSign/Adobe Sign API integration (OAuth, envelope creation, webhook for status).

REST: create request, signing URL per signer, verify signatures, get status.
On completion: new document version with signed PDF, publish signature.completed.v1.
```

## PROMPT 24 — React Native Mobile App

```
Build mobile/ with Expo SDK 51 (React Native + TypeScript).

Expo Router: (auth)/login, (tabs)/(home, search, upload, notifications, profile), workspace/[id], folder/[id], document/[id].

Features: login+SSO, document browser, PDF viewer (react-native-pdf), camera capture (expo-camera) with auto-crop, file upload, search, push notifications (expo-notifications), offline mode (expo-sqlite for metadata cache, pinned docs in device storage).
```

## MANUAL REVIEW — Phase 19-24
- [ ] Temporal workflow can't skip approval steps
- [ ] WebSocket validates auth on EVERY connection
- [ ] WebSocket doesn't broadcast to unauthorized users
- [ ] Audit hash chain is correct (run integrity verification)
- [ ] Audit events can't be modified/deleted
- [ ] Retention engine respects legal holds
- [ ] PDF signature is valid (verify with Adobe Acrobat)
- [ ] Mobile stores tokens in Keychain/Keystore (not AsyncStorage)

## MANUAL BUILD
- PDF signing with LTV (PKI expert review)
- DocuSign OAuth + webhook signature verification
- Temporal workflow integration tests
- App Store / Play Store submission process

## PAYABLE ITEMS — Phase 19-24
| Item | Cost |
|------|------|
| Temporal Cloud | $200/month (or self-hosted free) |
| DocuSign API | $10/envelope (prod) |
| TSA (DigiCert) | $0.01-0.05/timestamp |
| Apple Developer | $99/year |
| Google Play Developer | $25 one-time |
| Expo EAS Build | $99/month |

---

# ═══════════════════════════════════════════
# PHASE 25-30: ON-PREM, SAAS, SECURITY, OBSERVABILITY, INTEGRATIONS
# ═══════════════════════════════════════════

## PROMPT 25 — Helm Chart for On-Prem

```
Build deploy/helm/ — Kubernetes Helm chart for on-prem deployment.

values.yaml: global.imageRegistry, global.storageClass, global.tenantIsolation.
Per-service: replicas, resources, env, autoscaling.
Dependencies: postgresql (bitnami), redis, opensearch, minio, nats, qdrant, temporal.

Templates per service: Deployment (probes, PDB, anti-affinity), Service, HPA, ConfigMap, Secret, ServiceMonitor, NetworkPolicy.
Ingress: support nginx/traefik/istio with TLS.

values-onprem.yaml: local MinIO, local SMTP, no telemetry, local LLM.
values-airgapped.yaml: all images from local registry, offline license, bundled OCR models, bundled LLM.

Hooks: pre-install/upgrade DB migration job, post-install default admin creation.
Backup CronJob template. NOTES.txt with post-install instructions.
```

## PROMPT 26 — SaaS Control Plane

```
Build services/controlplane/ — internal service for tenant management + billing.

POST /internal/v1/tenants/provision — create org, schema/DB, OpenSearch index, Qdrant collection, MinIO bucket, Stripe customer+subscription, admin user, welcome email.

Stripe integration (stripe-go): webhook handler for invoice.paid, payment_failed, subscription.updated. Grace period 7 days on payment failure then suspend.

Usage metering (hourly): storage GB, OCR pages, API calls, active users, AI tokens → push to Stripe usage records.

Feature flags per tenant (stored in config): ai_enabled, advanced_workflow, custom_branding, api_access, sso_enabled, e_signatures. Defaults by plan, overrides per tenant.

Tenant routing table (Redis): tenant_route:{id} → {db_host, db_schema, search_cluster, storage_region}.
```

## PROMPT 27 — Security Hardening

```
Add across all services:

API Gateway: rate limiting (per-tenant, per-IP, per-endpoint), WAF rules (SQLi, XSS, path traversal), 10MB body limit, security headers (HSTS, CSP, X-Frame-Options DENY, nosniff), CORS per tenant.

Input validation: go-playground/validator on all structs, string sanitization, UUID validation, JSON depth limit (10 levels), null byte rejection.

DLP pipeline: on share/download, scan for credit card/SSN/custom keywords. Actions: warn, block, quarantine, redact.

Secrets rotation CLI: dms-admin rotate-secrets (DB passwords, API keys, JWT signing, KEKs). Old secrets valid 24h.

CI/CD scanning: Semgrep (SAST), Trivy (container images), gosec, npm audit. Block merge on HIGH/CRITICAL.
```

## PROMPT 28 — Observability

```
Add to all services:

Logging: zerolog, every line has timestamp+level+service+tenant_id+correlation_id. PII scrubbing. DEBUG sampled 1%.

Prometheus metrics: http_requests_total, http_request_duration_seconds (buckets 10/50/100/250/500/1000/5000ms), grpc_*, db_query_duration, search_latency, ocr_pages_processed, upload/download_bytes, websocket_connections, cache_hits/misses, event_bus_lag.

OpenTelemetry tracing: auto-instrument HTTP/gRPC/DB/Redis. Custom spans for OPA, S3, LLM, OCR. Export to Tempo/Jaeger.

Grafana dashboards (JSON): Platform Overview, Per-Service Health, Search Performance, Intelligence Pipeline, Tenant Health (noisy neighbor detection).

Alerting: P1 pages (error rate>1%, DB down, cross-tenant leak), P2 Slack (latency>500ms, queue>10K, disk>85%), P3 dashboard (cache <80%, job failures>5%).
```

## PROMPT 29 — Integration Platform

```
Build services/connector/:

WEBHOOK SYSTEM: POST /api/v1/webhooks — create subscription (validate URL reachable, block internal IPs for SSRF). Delivery: HMAC-SHA256 signature, exponential backoff (5s→6h, 6 retries), dead letter, delivery log.

MCP SERVER: Expose DMS as MCP tool for LLM agents. Tools: search_documents, get_document, upload_document, start_workflow, ask_question. Go HTTP server, JSON-RPC over SSE, API key auth.

CONNECTORS (build 3):
1. Microsoft 365: Graph API for email ingestion + SharePoint migration + Teams notifications
2. Salesforce: attach docs to records, bidirectional sync
3. Google Workspace: Gmail ingestion + Drive migration
Each: encrypted OAuth config, automatic token refresh, rate limiting, error retry.

OpenAPI 3.1 spec generation from all REST endpoints. API docs site.
```

## PROMPT 30 — Load Testing & Production Readiness

```
Build load testing suite using k6 (grafana/k6):

Scenarios:
1. Document CRUD: 1000 concurrent users, 500 creates/min, 5000 reads/min
2. Search: 200 concurrent searches/sec, mixed lexical/semantic/hybrid
3. Upload: 100 concurrent uploads (10MB-100MB files)
4. OCR pipeline: 1000 documents queued, verify all complete <30min
5. WebSocket: 5000 concurrent connections, 100 messages/sec broadcast
6. Mixed workload: 10K simulated users doing realistic operations for 1 hour

Target latencies to verify:
  Search p99 < 300ms, Upload init p99 < 200ms, OCR p95 < 30s/page,
  Download URL p99 < 100ms, Document read p99 < 150ms,
  Permission check p99 < 5ms, Autocomplete p99 < 50ms

Write k6 scripts for each scenario. Store results in InfluxDB + Grafana dashboard.

Also create:
- Chaos test: kill random pod during load test, verify no data loss
- Failover test: kill DB primary, verify Patroni promotes standby <30s
- Cross-tenant isolation test: during load test, verify no cross-tenant data leakage
```

## MANUAL REVIEW — Phase 25-30
- [ ] Helm secrets injected via K8s Secrets, not ConfigMaps
- [ ] NetworkPolicies deny by default
- [ ] Stripe webhook signature verification implemented
- [ ] Webhook delivery blocks SSRF (no internal/private IPs)
- [ ] MCP server requires auth on every tool call
- [ ] Load test results meet all target latencies
- [ ] Chaos test confirms zero data loss
- [ ] Cross-tenant isolation verified under load

## MANUAL BUILD — Phase 25-30
- Penetration test by external firm (NCC Group, Trail of Bits)
- SOC 2 Type II readiness assessment
- Bug bounty program setup (HackerOne)
- Production runbooks for every incident scenario
- On-call rotation design

## PAYABLE ITEMS — Phase 25-30
| Item | Cost |
|------|------|
| Stripe | 2.9% + $0.30/transaction |
| External pentest | $30K-80K |
| SOC 2 audit | $50K-150K |
| HackerOne bug bounty | $500-20K/bounty |
| k6 Cloud (optional) | $99/month |
| Grafana Cloud | $0 (free tier) to $299/month |

---

# ═══════════════════════════════════════════
# COMPLETE PAYABLE ITEMS SUMMARY
# ═══════════════════════════════════════════

## Monthly Recurring Costs (Production SaaS)

| Category | Item | Monthly Cost |
|----------|------|-------------|
| **Compute** | AWS EKS cluster (production) | $500-2,000 |
| **Compute** | GPU instances for OCR (2-4× g5.xlarge) | $1,500-3,000 |
| **Database** | AWS RDS PostgreSQL (Multi-AZ) | $400-1,500 |
| **Search** | AWS OpenSearch (3-node) | $220-800 |
| **Cache** | AWS ElastiCache Redis | $100-300 |
| **Storage** | AWS S3 (varies by data volume) | $100-10,000+ |
| **CDN** | CloudFront | $50-500 |
| **Email** | SES/SendGrid | $10-100 |
| **LLM APIs** | OpenAI/Anthropic | $200-5,000+ |
| **Monitoring** | Grafana Cloud / Datadog | $0-500 |
| **CI/CD** | GitHub Actions | $0-50 |
| **Secrets** | HashiCorp Vault Cloud | $0-200 |
| **Mobile builds** | Expo EAS | $99 |
| **Billing** | Stripe processing (% of revenue) | Variable |
| **TOTAL (starter)** | | **~$3,500-5,000/month** |
| **TOTAL (scale)** | | **~$10,000-25,000/month** |

## One-Time Costs

| Item | Cost |
|------|------|
| Domain + SSL | $50 |
| Apple Developer Program | $99/year |
| Google Play Developer | $25 |
| External penetration test | $30K-80K |
| SOC 2 Type II audit | $50K-150K |
| Legal (terms of service, DPA, privacy policy) | $10K-30K |
| **TOTAL** | **$90K-260K** |

## Open Source (Free) Components

PostgreSQL, Redis, OpenSearch, MinIO, NATS, Qdrant, Temporal, OPA, ClamAV, Surya OCR, PaddleOCR, LiteLLM, PDF.js, LibreOffice, FFmpeg, Grafana, Prometheus, Jaeger, React, Tailwind, Radix UI, Yjs, sentence-transformers, SpaCy.

---

# ═══════════════════════════════════════════
# ANTI-PATTERNS CHECKLIST
# ═══════════════════════════════════════════

After EVERY phase, verify none of these exist in the codebase:

1. ❌ Query without `WHERE tenant_id = ?`
2. ❌ `SELECT *` in production code
3. ❌ OFFSET-based pagination
4. ❌ CASCADE DELETE
5. ❌ Secrets in env vars or config files (must use Vault)
6. ❌ Session tokens in localStorage
7. ❌ math/rand for security tokens (must use crypto/rand)
8. ❌ String concatenation in SQL queries
9. ❌ Logging passwords, tokens, PII, or API keys
10. ❌ Distributed locks for document editing (use optimistic concurrency)
11. ❌ Custom crypto implementations
12. ❌ Shared encryption key across tenants
13. ❌ Sequential integer IDs for documents or share links
14. ❌ Direct database access between services
15. ❌ Missing health checks or readiness probes
16. ❌ Unrestricted upload sizes
17. ❌ `LIKE '%term%'` on document tables (use search index)
18. ❌ Missing circuit breakers on synchronous service calls
19. ❌ WebSocket and REST in the same process
20. ❌ Cache keys without tenant namespace prefix
