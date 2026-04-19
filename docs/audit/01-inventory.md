# VaultDMS Audit — Phase A: Inventory

**Date:** 2026-04-16
**Auditor:** Claude Code automated audit

---

## 1. Directory Tree (3 levels)

```
DOCMS/
├── .claude/commands/          # Claude Code slash commands
├── .github/workflows/         # CI: ci.yml, release.yml
├── cmd/dms-admin/             # Admin CLI (secrets rotation)
├── deploy/
│   ├── helm/vaultdms/         # 69 Helm template files
│   └── monitoring/
│       ├── alerts/            # Prometheus rules (1 file)
│       └── dashboards/        # Grafana JSON (6 files)
├── docs/api/                  # OpenAPI 3.1 spec
├── mobile/
│   ├── api/                   # 6 API modules
│   ├── app/                   # 10 screen .tsx files
│   └── store/                 # 1 auth store
├── pkg/                       # 17 shared Go packages, 37 .go files
│   ├── auth/                  # Context helpers (tenant, user, correlation)
│   ├── config/                # Viper config loader
│   ├── crypto/                # Envelope encryption (AES-256-GCM + KEK)
│   ├── database/              # Pool, tenant tx, outbox, migrations (7 files)
│   ├── dlp/                   # Data loss prevention scanner
│   ├── errors/                # Domain error types + gRPC/HTTP mapping
│   ├── events/                # NATS JetStream helpers
│   ├── gateway/               # Rate limiting, WAF, security headers, CORS
│   ├── health/                # Health check server
│   ├── logger/                # zerolog wrapper with PII scrubbing
│   ├── metrics/               # Prometheus metric definitions
│   ├── middleware/             # 7 middleware files (tenant, correlation, etc.)
│   ├── storage/               # S3/MinIO client
│   ├── tenant/                # Tenant routing via Redis
│   ├── testutil/              # Docker test containers + fixtures
│   ├── tracing/               # OpenTelemetry init
│   └── validation/            # Input validation (go-playground/validator)
├── proto/vaultdms/v1/         # 13 .proto files
├── proto/gen/go/              # go.mod only — NO generated .pb.go files
├── scripts/                   # init-db.sql, seed, snapshot
├── services/                  # 14 services (11 Go, 2 Python, 1 Node)
├── tests/load/                # 7 k6 scenarios + chaos tests
└── web/src/                   # React 18 + TypeScript frontend
    ├── api/                   # 12 API client modules
    ├── components/            # 54 .tsx component files
    ├── hooks/                 # 10 custom hooks
    ├── lib/                   # cn, formatters, constants, validators, permissions
    ├── locales/               # en.json, ar.json
    ├── routes/                # 28 route files
    ├── store/                 # 3 Zustand stores
    ├── styles/                # globals.css
    └── types/                 # api.ts
```

## 2. Service Inventory

### Go Services (11)

| Service | .go files | Test files | main.go | handler | service | repository | model | migrations | Dockerfile |
|---------|-----------|------------|---------|---------|---------|------------|-------|------------|------------|
| audit | 5 | 0 | ✓ | ✓ | ✓ | ✓ | ✓ | 0 | ✓ |
| auth | 34 | 1 | ✓ | ✓ | ✓ | ✓ | ✓ | 0 | ✓ |
| billing | 9 | 0 | ✓ | ✓ | ✓ | ✓ | ✓ | 0 | ✓ |
| connector | 11 | 0 | ✓ | ✓ | ✓ | ✓ | ✓ | 0 | ✓ |
| document | 25 | 3 | ✓ | ✓ | ✓ | ✓ | ✓ | 6 (3 up+down) | ✓ |
| notification | 5 | 0 | ✓ | ✓ | ✓ | ✓ | ✓ | 0 | ✓ |
| policy | 15 | 1 | ✓ | ✓ | ✓ | ✓ | ✓ | 0 | ✓ |
| search | 12 | 3 | ✓ | ✓ | ✓ | ✓ | ✓ | 2 (1 up+down) | ✓ |
| signature | 5 | 0 | ✓ | ✓ | ✓ | ✓ | ✓ | 0 | ✓ |
| storage | 15 | 0 | ✓ | ✓ | ✓ | ✓ | ✓ | 0 | ✓ |
| workflow | 7 | 0 | ✓ | ✓ | ✓ | ✓ | ✓ | 0 | ✓ |

**Total Go: 143 files, 8 test files**

### Python Services (2)

| Service | .py files | Test files | Dockerfile | requirements.txt | pytest.ini |
|---------|-----------|------------|------------|------------------|------------|
| intelligence | 30 | 4 | ✓ | ✓ | ✓ |
| preview | 27 | 7 | ✓ | ✓ | ✓ |

### Node.js Services (1)

| Service | .js files | Dockerfile | package.json |
|---------|-----------|------------|-------------|
| collaboration | 4 | ✓ | ✓ |

## 3. Shared Packages (pkg/)

| Package | Files | Exported functions | Imported by services |
|---------|-------|-------------------|---------------------|
| auth | 1 | GetTenantID, GetUserID, GetUserRole, GetCorrelationID, SetTenantID, SetCorrelationID, WithUser | auth, document, policy |
| config | 1 | Load, MustLoad | ALL (via main.go) |
| crypto | 2 | EncryptData, DecryptData, GenerateDEK, NewLocalKeyManager, NewAWSKMSKeyManager, NewVaultKeyManager | storage, auth |
| database | 7+1test | DefaultPoolConfig, NewPool, WithTenant, WithTenantTx, WithTx, RunMigrations, NewOutboxEvent, NewOutboxRepository, NewOutboxPublisher | ALL Go services |
| dlp | 2+1test | NewScanner | (none directly — used via gateway) |
| errors | 1 | Validation, Conflict, Wrap, KindOf, FromPgError, ToGRPCError, ToHTTPError | document, policy, search, storage |
| events | 2 | ConnectNATS, EnsureStreams, NewPublisher, NewSubscriber, NewCloudEvent | ALL (via main.go) |
| gateway | 3 | NewRateLimiter, WAFMiddleware, SecurityHeadersMiddleware, CORSMiddleware | (none directly — meant for gateway service) |
| health | 1 | NewServer | ALL (via main.go) |
| logger | 1 | New, NewWithWriter, HashEmail, HashIP | ALL (via main.go) |
| metrics | 1 | Handler, HTTPMiddleware, (22 metric vars) | (none directly — meant for middleware chain) |
| middleware | 7 | RecoveryInterceptor, CorrelationInterceptor, TenantInterceptor, RequestLogInterceptor, NewRateLimiter, NewIPRateLimiter, NewRegionEnforcer + HTTP variants | ALL (via main.go) |
| storage | 2+1test | NewS3Client | storage, signature |
| tenant | 1 | NewRouter | billing |
| testutil | 3 | NewPostgresContainer, NewRedisContainer, NewMinIOContainer, NewNATSContainer, 8 fixture helpers | document (tests) |
| tracing | 1 | Init, Tracer, StartSpan, RecordError | (none directly — meant for main.go init) |
| validation | 1 | Struct, DecodeAndValidate, TrimString, IsValidUUID, ContainsNullByte | (none directly — meant for handlers) |

## 4. Proto Files

| Proto file | Status |
|-----------|--------|
| audit.proto | Source present, **NO generated code** |
| auth.proto | Source present, **NO generated code** |
| billing.proto | Source present, **NO generated code** |
| collaboration.proto | Source present, **NO generated code** |
| common.proto | Source present, **NO generated code** |
| document.proto | Source present, **NO generated code** |
| intelligence.proto | Source present, **NO generated code** |
| notification.proto | Source present, **NO generated code** |
| policy.proto | Source present, **NO generated code** |
| search.proto | Source present, **NO generated code** |
| signature.proto | Source present, **NO generated code** |
| storage.proto | Source present, **NO generated code** |
| workflow.proto | Source present, **NO generated code** |

**proto/gen/go/ contains only go.mod — zero .pb.go files exist.**
`buf generate` or `make proto-gen` has never been run.

## 5. Migration Files

| Service | Count | Files | Gaps |
|---------|-------|-------|------|
| document | 6 | 000001 up+down, 000002 up+down, 000003 up+down | None |
| search | 2 | 000001 up+down | None |
| All others | 0 | — | **All 9 other Go services have zero migrations** |

## 6. Frontend (web/)

- **Routes:** 28 files (login, register, forgot-password, shared, dashboard, workspaces, document detail, search, tasks, notifications, trash, 13 admin pages)
- **Components:** 54 .tsx files across ui/ layout/ documents/ viewer/ search/ workflow/ ai/ admin/ shared/
- **Hooks:** 10 files (useAuth, useDocuments, useSearch, useUpload, useFolders, useNotifications, useWorkspaces, useAI, useWebSocket, useTheme)
- **API modules:** 12 files
- **Stores:** 3 (auth, ui, upload)
- **Tests:** 0
- **Locales:** 2 (en.json, ar.json)

## 7. Mobile (mobile/)

- **Screens:** 10 .tsx files (login, home, search, upload, notifications, profile, workspace detail, document detail, 2 layouts)
- **API:** 6 .ts files
- **Store:** 1 .ts file
- **Tests:** 0

## 8. Infrastructure

| Item | Present |
|------|---------|
| go.work | ✓ (13 modules) |
| Makefile | ✓ (28+ targets) |
| .golangci.yml | ✓ |
| docker-compose.yml | ✓ (13 services) |
| docker-compose.prod.yml | ✓ |
| .github/workflows/ci.yml | ✓ (lint, test, build, 5 security scans) |
| .github/workflows/release.yml | ✓ |
| .github/dependabot.yml | ✓ (8 ecosystems) |
| Helm chart | ✓ (69 files, 3 value files) |
| Prometheus alert rules | ✓ (13 rules in 3 tiers) |
| Grafana dashboards | ✓ (6 dashboards) |
| k6 load tests | ✓ (7 scenarios) |
| OpenAPI spec | ✓ (docs/api/openapi.yaml) |

## 9. Totals

| Metric | Count |
|--------|-------|
| Go source files | 180 |
| Go test files | 8 |
| Python source files | 57 |
| Python test files | 11 |
| Node.js source files | 4 |
| TypeScript/TSX (web) | 107 |
| TypeScript/TSX (mobile) | 17 |
| Proto files | 13 |
| Generated proto Go files | **0** |
| Helm templates | 69 |
| Grafana dashboards | 6 |
| k6 test scripts | 7 |
| Total services | 14 |
| Total pkg packages | 17 |
