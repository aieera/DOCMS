# DMS COMPLETE BUILD PROMPTS
## 30 Enterprise-Grade Prompts for Claude Code + Claude Ecosystem Guide
### Copy-Paste Ready — Each Prompt Builds One Complete Phase

---

# CLAUDE ECOSYSTEM BUILD STRATEGY

Before diving into the 30 prompts, here's how to use Claude's full ecosystem to build this DMS:

## 1. Claude Code (Terminal) — Primary Build Tool
Use for: All 30 prompts below. Clone your repo, cd into it, run `claude` in terminal.
**Pro tips:**
- Start each session with: `/init` to let Claude Code understand your project structure
- Use `/add-dir services/document` to focus Claude on specific services
- Use `claude --continue` to resume a previous session
- Use `claude --dangerously-skip-permissions` only in dev (auto-approves file writes)
- Set `CLAUDE_MODEL=claude-sonnet-4-20250514` for faster iteration on boilerplate, switch to opus for architecture decisions
- Create a `.claude/commands/` folder with custom slash commands for repeated tasks

## 2. Claude Code Custom Commands (Create These First)
Create these files in your repo to speed up development:

**.claude/commands/new-service.md:**
```
Create a new Go microservice in services/$ARGUMENTS with the standard structure:
cmd/server/main.go, internal/handler/, internal/service/, internal/repository/,
internal/model/, migrations/, Dockerfile. Follow existing service patterns.
```

**.claude/commands/add-endpoint.md:**
```
Add a new REST+gRPC endpoint to the current service for: $ARGUMENTS
Include: handler, service logic, repository method, migration if needed,
tests, OpenAPI doc comment. Follow existing patterns in this service.
```

**.claude/commands/security-audit.md:**
```
Audit the current service for security issues. Check for:
- Missing tenant_id in queries
- Missing permission checks
- Logging of sensitive data
- SQL injection vectors
- Missing input validation
- Hardcoded secrets
- Missing rate limiting
Report each issue with file, line, and fix.
```

## 3. Claude MCP Servers — Connect These
In your Claude Desktop or Claude Code config, add these MCP servers:

**GitHub MCP:** For PR reviews, issue tracking directly from Claude
```json
{ "mcpServers": { "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] } } }
```

**PostgreSQL MCP:** Let Claude query your dev database directly
```json
{ "mcpServers": { "postgres": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-postgres", "postgresql://localhost:5432/dms"] } } }
```

**Filesystem MCP:** Let Claude browse project files
```json
{ "mcpServers": { "filesystem": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/dms"] } } }
```

## 4. Claude API — Use Inside the DMS Product Itself
The DMS uses Claude/LLM APIs for:
- Document Q&A (RAG) — Phase 12
- Document classification — Phase 10
- Field extraction — Phase 10
- Summarization — Phase 12
Use LiteLLM as the gateway so you can swap between Claude/OpenAI/local models per tenant.

## 5. Claude Projects (claude.ai)
Create a Claude Project called "DMS Architecture" and upload:
- The architecture blueprint (from previous conversation)
- This prompts file
- Your current codebase snapshots
This gives Claude persistent context about your project for architecture discussions, code reviews, and debugging.

## 6. Claude for Code Review
After each phase, paste the generated PR diff into Claude (claude.ai) with this prompt:
"Review this code for a multi-tenant DMS. Check: tenant isolation, SQL injection, permission checks, error handling, logging of secrets, pagination method, cascade deletes. Be ruthless."

## 7. Claude Artifacts
Use Claude Artifacts (in claude.ai) to:
- Generate Mermaid diagrams for each service's architecture
- Create interactive API documentation
- Build test data generators
- Create deployment checklists as interactive widgets

---

# HOW TO RUN EACH PROMPT

1. Open terminal in your project root
2. Run `claude` to start Claude Code
3. Paste the entire prompt block
4. Let Claude Code generate all files
5. Review the output, test it, commit to a feature branch
6. Run the security audit command: `/security-audit`
7. Create a PR, have team review

---

# ████████████████████████████████████████████
# PROMPT 1 OF 30: MONOREPO & INFRASTRUCTURE
# ████████████████████████████████████████████

```
I'm building an enterprise Document Management System (DMS) called "SeDoc". This is the foundation setup. Create a production-grade Go monorepo with all infrastructure.

PROJECT IDENTITY:
- Name: SeDoc
- Backend: Go 1.22+ with Go workspaces
- Frontend: React 18 + TypeScript + Vite
- Database: PostgreSQL 16
- Search: OpenSearch 2.12
- Vector DB: Qdrant 1.7
- Cache: Redis 7
- Object Storage: MinIO (S3-compatible)
- Event Bus: NATS 2.10 with JetStream
- Workflow: Temporal
- Auth Policy: OPA (embedded)

CREATE THIS EXACT DIRECTORY STRUCTURE:

vaultdms/
├── go.work                     # Go workspace linking all service modules
├── go.work.sum
├── docker-compose.yml          # Local dev environment — ALL dependencies
├── docker-compose.prod.yml     # Single-node on-prem production
├── Makefile                    # Targets: build, test, lint, migrate-up, migrate-down, proto-gen, docker-build, docker-push, seed
├── .golangci.yml               # Strict linting: gocritic, gosec, errcheck, staticcheck, exhaustive, nilerr, bodyclose, sqlclosecheck
├── .editorconfig
├── .gitignore
├── README.md
├── LICENSE
│
├── .github/
│   └── workflows/
│       ├── ci.yml              # On every PR: lint, test, build all services, run integration tests
│       └── release.yml         # On tag push: build+push Docker images, generate changelog
│
├── proto/                      # Protobuf definitions (shared contract)
│   ├── buf.yaml                # Buf configuration for linting + breaking change detection
│   ├── buf.gen.yaml            # Code generation config: Go + grpc-gateway + OpenAPI
│   └── vaultdms/v1/
│       ├── common.proto
│       ├── document.proto
│       ├── storage.proto
│       ├── search.proto
│       ├── auth.proto
│       ├── policy.proto
│       ├── intelligence.proto
│       ├── workflow.proto
│       ├── audit.proto
│       ├── notification.proto
│       ├── signature.proto
│       ├── billing.proto
│       └── collaboration.proto
│
├── pkg/                        # Shared Go libraries used by all services
│   ├── config/
│   │   └── config.go           # Viper-based config: env vars → YAML file → defaults. Struct-based with validation tags.
│   │
│   ├── logger/
│   │   └── logger.go           # zerolog wrapper. Every log line includes: timestamp, level, service_name, service_version, tenant_id (from context), correlation_id (from context), caller file:line. Methods: Info(), Error(), Warn(), Debug() all accept context as first param to auto-extract tenant_id and correlation_id. PII scrubbing function that hashes emails and IPs before logging.
│   │
│   ├── middleware/
│   │   ├── tenant.go           # TenantExtractor middleware for HTTP and gRPC.
│   │   │                       # HTTP: reads X-Tenant-ID header (internal) or extracts from session token.
│   │   │                       # gRPC: reads from metadata.
│   │   │                       # CRITICAL: On every database connection checkout from pgxpool, execute:
│   │   │                       #   conn.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tenantID)
│   │   │                       # This sets the Postgres session variable that RLS policies use.
│   │   │                       # If tenant_id is empty/missing, REJECT the request with 401.
│   │   │                       # Store tenant_id in context: context.WithValue(ctx, tenantIDKey, tenantID)
│   │   │
│   │   ├── correlation.go      # Generate UUIDv7 correlation ID if not present in incoming X-Correlation-ID header.
│   │   │                       # Store in context. Propagate to all outgoing HTTP/gRPC calls.
│   │   │                       # Add to all log lines via logger context extraction.
│   │   │
│   │   ├── requestlog.go       # Log every request: method, path, status_code, latency_ms, tenant_id, correlation_id,
│   │   │                       # request_size_bytes, response_size_bytes, user_agent (first 200 chars), remote_ip (hashed).
│   │   │                       # Use zerolog. Skip health check endpoints to reduce noise.
│   │   │
│   │   ├── recovery.go         # Panic recovery middleware. Log stack trace, return 500 with correlation_id (no internal details exposed).
│   │   │
│   │   ├── ratelimit.go        # Token bucket rate limiter using Redis. Key: ratelimit:{tenant_id}:{endpoint_group}.
│   │   │                       # Configurable per tenant (from tenant config in Redis).
│   │   │                       # Default: 1000 req/min. Return 429 with Retry-After header on limit.
│   │   │
│   │   └── region.go           # RegionEnforcer. For write operations on documents, read the document's region_pin
│   │   │                       # from context/request, validate that the target storage bucket / search index / cache
│   │   │                       # is in the correct region. Reject with 400 REGION_VIOLATION if mismatch.
│   │   │                       # This middleware wraps storage and search client calls.
│   │
│   ├── database/
│   │   ├── pool.go             # NewPool(ctx, databaseURL string) (*pgxpool.Pool, error)
│   │   │                       # Config: MaxConns=50, MinConns=5, MaxConnLifetime=1h, MaxConnIdleTime=30m
│   │   │                       # HealthCheck: 30s interval
│   │   │                       # AfterConnect: prepare common statements
│   │   │                       # BeforeAcquire: validate connection is alive
│   │   │
│   │   ├── tenant.go           # WithTenant(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, fn func(conn *pgxpool.Conn) error) error
│   │   │                       # Acquires connection, runs SET app.current_tenant, executes fn, returns connection.
│   │   │                       # CRITICAL: tenant must be set BEFORE any query runs.
│   │   │                       #
│   │   │                       # WithTenantTx(ctx, pool, tenantID, fn func(tx pgx.Tx) error) error
│   │   │                       # Same but within a transaction. Sets tenant, begins TX, executes fn, commits or rolls back.
│   │   │
│   │   ├── transaction.go      # WithTx(ctx, pool, fn func(tx pgx.Tx) error) error
│   │   │                       # Begin transaction, execute fn, commit on success, rollback on error.
│   │   │                       # Handles nested transactions via savepoints.
│   │   │
│   │   ├── outbox.go           # OutboxEvent struct: ID, TenantID, EventType, AggregateType, AggregateID, Payload(json.RawMessage), CreatedAt
│   │   │                       # InsertOutboxEvent(ctx, tx pgx.Tx, event OutboxEvent) error — insert within same TX as business write
│   │   │                       # OutboxPublisher: background goroutine that polls outbox table every 100ms:
│   │   │                       #   SELECT * FROM outbox WHERE NOT published ORDER BY created_at LIMIT 100 FOR UPDATE SKIP LOCKED
│   │   │                       #   For each batch: publish to NATS JetStream, mark as published, delete old published events (>24h)
│   │   │                       #   Handle partial failures: if NATS publish fails for one event, retry; if marking fails, event will be re-published (consumers must be idempotent)
│   │   │
│   │   └── migrate.go          # RunMigrations(databaseURL, migrationsDir) error — using golang-migrate/migrate/v4
│   │                           # Support: up, down, version, force
│   │
│   ├── events/
│   │   ├── publisher.go        # NATSPublisher: connects to NATS JetStream, creates streams on startup
│   │   │                       # Publish(ctx, subject string, event CloudEvent) error
│   │   │                       # CloudEvent struct following CloudEvents v1.0 spec:
│   │   │                       #   SpecVersion, ID (UUIDv7), Source, Type, Subject, Time, DataContentType,
│   │   │                       #   TenantID (extension), RegionPin (extension), CorrelationID (extension), Data (json.RawMessage)
│   │   │                       # Streams to create on startup:
│   │   │                       #   DOCUMENTS: subjects "dms.document.>", "dms.version.>"
│   │   │                       #   AUTH: subjects "dms.auth.>", "dms.permission.>"
│   │   │                       #   WORKFLOWS: subjects "dms.workflow.>"
│   │   │                       #   AUDIT: subjects "dms.audit.>"
│   │   │                       #   NOTIFICATIONS: subjects "dms.notify.>"
│   │   │                       #   BILLING: subjects "dms.billing.>"
│   │   │                       #   INTELLIGENCE: subjects "dms.intelligence.>"
│   │   │                       #   Retention: 7 days, MaxBytes: 10GB per stream
│   │   │
│   │   └── subscriber.go       # NATSSubscriber: durable consumer with manual ack
│   │                           # Subscribe(ctx, stream, subject, consumerName, handler func(msg *nats.Msg) error) error
│   │                           # Handler must return nil to ack, error to nack (with backoff)
│   │                           # DeliverPolicy: DeliverAll for new consumers, DeliverLast for recovery
│   │                           # AckWait: 30 seconds, MaxDeliver: 5
│   │
│   ├── errors/
│   │   └── errors.go           # Domain error types:
│   │                           # ErrNotFound — maps to gRPC NotFound / HTTP 404
│   │                           # ErrAlreadyExists — maps to gRPC AlreadyExists / HTTP 409
│   │                           # ErrForbidden — maps to gRPC PermissionDenied / HTTP 403
│   │                           # ErrUnauthorized — maps to gRPC Unauthenticated / HTTP 401
│   │                           # ErrValidation{Field, Message} — maps to gRPC InvalidArgument / HTTP 400
│   │                           # ErrConflict{Reason} — maps to gRPC FailedPrecondition / HTTP 409
│   │                           # ErrRateLimited — maps to gRPC ResourceExhausted / HTTP 429
│   │                           # ErrRegionViolation — maps to HTTP 400 with specific error code
│   │                           # ErrLegalHold — maps to HTTP 409 "Document under legal hold"
│   │                           # ErrInternal — maps to gRPC Internal / HTTP 500
│   │                           # ToGRPCError(err) → status.Error with appropriate code
│   │                           # ToHTTPError(err) → {code: int, error: {type, message, field?, correlation_id}}
│   │                           # FromPgError(err) → domain error (unique_violation → ErrAlreadyExists, no rows → ErrNotFound)
│   │
│   ├── crypto/
│   │   ├── envelope.go         # Envelope encryption:
│   │   │                       # EncryptData(plaintext []byte, dek []byte) (ciphertext []byte, nonce []byte, err error)
│   │   │                       #   — AES-256-GCM, random 12-byte nonce
│   │   │                       # DecryptData(ciphertext []byte, nonce []byte, dek []byte) (plaintext []byte, err error)
│   │   │                       # GenerateDEK() ([]byte, error) — 32 bytes from crypto/rand
│   │   │
│   │   └── kms.go              # KeyManager interface:
│   │                           #   GenerateDataKey(ctx, kekID string) (plaintextDEK, encryptedDEK []byte, err error)
│   │                           #   DecryptDataKey(ctx, kekID string, encryptedDEK []byte) (plaintextDEK []byte, err error)
│   │                           #   RotateKey(ctx, oldKEKID, newKEKID string) error
│   │                           # Implementations (stubs for now, full implementation in Phase 6):
│   │                           #   LocalKeyManager — static key from env var, for dev only, logs WARNING on startup
│   │                           #   VaultKeyManager — HashiCorp Vault Transit engine
│   │                           #   AWSKMSKeyManager — AWS KMS
│   │
│   ├── auth/
│   │   └── context.go          # Helper functions:
│   │                           # GetTenantID(ctx) (uuid.UUID, error)
│   │                           # GetUserID(ctx) (uuid.UUID, error)
│   │                           # GetUserRole(ctx) string
│   │                           # GetUserGroups(ctx) []uuid.UUID
│   │                           # GetCorrelationID(ctx) string
│   │                           # SetTenantID(ctx, id) context.Context
│   │                           # SetUserID(ctx, id) context.Context
│   │                           # UserInfo struct: ID, TenantID, Email, Role, Groups[]
│   │
│   ├── storage/
│   │   └── s3.go               # S3Client wrapping minio-go/v7:
│   │                           # NewS3Client(endpoint, accessKey, secretKey, useSSL) (*S3Client, error)
│   │                           # PutObject(ctx, bucket, key, reader, size, contentType) error
│   │                           # GetObject(ctx, bucket, key) (io.ReadCloser, error)
│   │                           # DeleteObject(ctx, bucket, key) error
│   │                           # GeneratePresignedPutURL(ctx, bucket, key, expiry time.Duration) (string, error)
│   │                           # GeneratePresignedGetURL(ctx, bucket, key, expiry time.Duration) (string, error)
│   │                           # BucketExists(ctx, bucket) (bool, error)
│   │                           # CreateBucket(ctx, bucket, region) error
│   │                           # CopyObject(ctx, srcBucket, srcKey, dstBucket, dstKey) error
│   │                           # GetObjectInfo(ctx, bucket, key) (ObjectInfo{Size, ContentType, ETag, LastModified}, error)
│   │
│   ├── health/
│   │   └── health.go           # HealthServer: runs on separate port (default :8081)
│   │                           # GET /healthz — returns 200 if process is alive
│   │                           # GET /readyz — returns 200 only if ALL dependencies are reachable:
│   │                           #   - Postgres: pool.Ping(ctx) with 2s timeout
│   │                           #   - Redis: client.Ping(ctx) with 2s timeout
│   │                           #   - NATS: conn.Status() == Connected
│   │                           #   - MinIO: client.BucketExists(ctx, "health-check") with 2s timeout
│   │                           #   Returns 503 with JSON listing which dependencies failed if any fail
│   │                           # GET /metrics — Prometheus metrics (promhttp.Handler())
│   │
│   └── testutil/
│       ├── containers.go       # Test helpers using testcontainers-go:
│       │                       # NewPostgresContainer(ctx) → connection string + cleanup func
│       │                       # NewRedisContainer(ctx) → address + cleanup func
│       │                       # NewMinIOContainer(ctx) → endpoint + access/secret + cleanup func
│       │                       # NewNATSContainer(ctx) → URL + cleanup func
│       │
│       ├── fixtures.go         # Test data factories:
│       │                       # NewTestTenant() → Organization with random slug
│       │                       # NewTestUser(tenantID) → User with random email
│       │                       # NewTestDocument(tenantID, workspaceID, folderID) → Document with defaults
│       │                       # NewTestWorkspace(tenantID) → Workspace
│       │                       # NewTestFolder(tenantID, workspaceID) → Folder
│       │
│       └── assertions.go       # Custom test assertions:
│                               # AssertEventPublished(t, natsConn, eventType, timeout) — wait for NATS event
│                               # AssertAuditEventCreated(t, pool, tenantID, action)
│                               # AssertTenantIsolation(t, pool, tenantA, tenantB) — insert as A, query as B, assert 0 rows

FOR EACH GO SERVICE (services/document, services/storage, services/search, services/auth, services/policy, services/workflow, services/notification, services/audit, services/signature, services/billing, services/connector), create:

cmd/server/main.go:
```go
package main

import (
    "context"
    "fmt"
    "net"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/rs/zerolog"
    "github.com/spf13/viper"
    "google.golang.org/grpc"
    // ... other imports
)

func main() {
    // 1. Load config
    cfg := config.Load()

    // 2. Initialize logger
    log := logger.New(cfg.ServiceName, cfg.ServiceVersion, cfg.LogLevel)

    // 3. Connect to Postgres
    pool, err := database.NewPool(ctx, cfg.DatabaseURL)
    if err != nil { log.Fatal().Err(err).Msg("failed to connect to database") }
    defer pool.Close()

    // 4. Connect to Redis
    rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisURL})
    defer rdb.Close()

    // 5. Connect to NATS JetStream
    nc, js, err := events.ConnectNATS(cfg.NATSURL)
    if err != nil { log.Fatal().Err(err).Msg("failed to connect to NATS") }
    defer nc.Close()

    // 6. Connect to MinIO
    s3Client, err := storage.NewS3Client(cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOUseSSL)
    if err != nil { log.Fatal().Err(err).Msg("failed to connect to MinIO") }

    // 7. Start health server on :8081
    healthServer := health.NewServer(pool, rdb, nc, s3Client)
    go healthServer.Start(":8081")

    // 8. Create gRPC server with middleware
    grpcServer := grpc.NewServer(
        grpc.ChainUnaryInterceptor(
            middleware.RecoveryInterceptor(log),
            middleware.CorrelationIDInterceptor(),
            middleware.TenantInterceptor(pool),
            middleware.RequestLogInterceptor(log),
        ),
    )
    // Register service-specific gRPC handlers here

    // 9. Start gRPC server
    grpcLis, _ := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
    go grpcServer.Serve(grpcLis)

    // 10. Start HTTP server (REST via grpc-gateway)
    mux := http.NewServeMux()
    // Register REST handlers
    httpServer := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: mux}
    go httpServer.ListenAndServe()

    // 11. Start outbox publisher
    outboxPublisher := database.NewOutboxPublisher(pool, js, log)
    go outboxPublisher.Start(ctx)

    // 12. Graceful shutdown
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
    <-quit
    log.Info().Msg("shutting down")

    shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    grpcServer.GracefulStop()
    httpServer.Shutdown(shutdownCtx)
    outboxPublisher.Stop()
}
```

internal/handler/   — gRPC handlers + REST handlers (grpc-gateway or chi router)
internal/service/   — Business logic layer
internal/repository/ — Database access (pgx queries)
internal/model/     — Domain models (Go structs)
migrations/         — SQL migration files (golang-migrate format: 000001_name.up.sql / .down.sql)
go.mod              — Module: github.com/aieera/sedoc/services/{name}
Dockerfile:
```dockerfile
# Build stage
FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /build
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY services/{name}/ services/{name}/
WORKDIR /build/services/{name}
RUN go build -ldflags="-w -s -X main.version=${VERSION}" -o /app ./cmd/server/

# Runtime stage
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 1000 appuser
USER appuser
COPY --from=builder /app /app
EXPOSE 8080 9090 8081
ENTRYPOINT ["/app"]
```

docker-compose.yml — include ALL of these services:
```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: sedoc
      POSTGRES_USER: sedoc
      POSTGRES_PASSWORD: devpassword
    ports: ["5432:5432"]
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./scripts/init-db.sql:/docker-entrypoint-initdb.d/init.sql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U sedoc"]
      interval: 5s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    ports: ["6379:6379"]
    command: redis-server --appendonly yes --maxmemory 256mb --maxmemory-policy allkeys-lru
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]

  opensearch:
    image: opensearchproject/opensearch:2.12.0
    environment:
      - discovery.type=single-node
      - plugins.security.disabled=true
      - OPENSEARCH_JAVA_OPTS=-Xms512m -Xmx512m
    ports: ["9200:9200"]

  minio:
    image: minio/minio
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    ports: ["9000:9000", "9001:9001"]
    volumes:
      - miniodata:/data

  minio-init:
    image: minio/mc
    depends_on: [minio]
    entrypoint: >
      /bin/sh -c "
      sleep 5;
      mc alias set local http://minio:9000 minioadmin minioadmin;
      mc mb --ignore-existing local/dms-us-east-1-hot;
      mc mb --ignore-existing local/dms-us-east-1-warm;
      mc mb --ignore-existing local/dms-us-east-1-previews;
      mc mb --ignore-existing local/dms-quarantine;
      "

  nats:
    image: nats:2.10
    command: -js -sd /data
    ports: ["4222:4222", "8222:8222"]
    volumes:
      - natsdata:/data

  qdrant:
    image: qdrant/qdrant:v1.7.4
    ports: ["6333:6333", "6334:6334"]
    volumes:
      - qdrantdata:/qdrant/storage

  temporal:
    image: temporalio/auto-setup:1.22
    environment:
      - DB=postgresql
      - DB_PORT=5432
      - POSTGRES_USER=sedoc
      - POSTGRES_PWD=devpassword
      - POSTGRES_SEEDS=postgres
    ports: ["7233:7233"]
    depends_on:
      postgres:
        condition: service_healthy

  temporal-ui:
    image: temporalio/ui:latest
    environment:
      - TEMPORAL_ADDRESS=temporal:7233
    ports: ["8233:8080"]
    depends_on: [temporal]

  clamav:
    image: clamav/clamav:1.2
    ports: ["3310:3310"]
    volumes:
      - clamdata:/var/lib/clamav

volumes:
  pgdata:
  miniodata:
  natsdata:
  qdrantdata:
  clamdata:
```

scripts/init-db.sql:
```sql
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "ltree";
-- Create application roles
CREATE ROLE dms_app LOGIN PASSWORD 'devpassword';
CREATE ROLE dms_readonly LOGIN PASSWORD 'devpassword';
GRANT CONNECT ON DATABASE sedoc TO dms_app, dms_readonly;
```

Makefile targets:
- build: go build all services
- test: go test ./... with race detector
- lint: golangci-lint run
- migrate-up: run all migrations
- migrate-down: rollback last migration
- migrate-create: create new migration (NAME=xxx)
- proto-gen: buf generate
- docker-build: build all Docker images
- docker-up: docker compose up -d
- docker-down: docker compose down
- seed: run test data seeder
- security-check: run gosec + govulncheck

Generate ALL files. Every file should be complete, compilable, and follow Go best practices. Use descriptive variable names. Add godoc comments to all exported types and functions.
```

---

# ████████████████████████████████████████████
# PROMPT 2 OF 30: DATABASE SCHEMA & RLS
# ████████████████████████████████████████████

```
Create the complete PostgreSQL schema for SeDoc. Generate migration files in services/document/migrations/ using golang-migrate format.

File: 000001_initial_schema.up.sql

This single migration creates ALL tables for the entire system. In production we'd split these, but for initial development we want one atomic migration.

ABSOLUTE RULES — VIOLATING THESE IS A SECURITY INCIDENT:
1. Every table (except organizations) has tenant_id UUID NOT NULL as the FIRST column
2. Every table uses PRIMARY KEY (tenant_id, id) — tenant_id FIRST for index scan efficiency
3. Every table has ROW LEVEL SECURITY enabled and forced
4. Every RLS policy uses: current_setting('app.current_tenant', true)::uuid — the 'true' parameter makes it return NULL (not error) when the setting doesn't exist, which means the policy matches NO rows, which is the safe default
5. NO CASCADE ON DELETE anywhere — all deletes are soft (deleted_at TIMESTAMPTZ column)
6. All IDs are UUID (using gen_random_uuid())
7. All timestamps are TIMESTAMPTZ (timezone-aware)
8. Cursor-based pagination only — NO OFFSET anywhere in the codebase
9. Every foreign key column has a corresponding index

Generate this SQL:

-- Enable required extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "ltree";

-- Utility: updated_at trigger function
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ language 'plpgsql';

-- ============================================================
-- TABLE 1: organizations (the tenant table — no RLS on this one, it IS the tenant)
-- ============================================================
CREATE TABLE organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    plan TEXT NOT NULL DEFAULT 'standard' CHECK (plan IN ('standard', 'enterprise', 'dedicated')),
    settings JSONB NOT NULL DEFAULT '{}',
    primary_region TEXT NOT NULL DEFAULT 'us-east-1',
    logo_url TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);
CREATE TRIGGER update_organizations_updated_at BEFORE UPDATE ON organizations FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE INDEX idx_organizations_slug ON organizations(slug) WHERE deleted_at IS NULL;

-- ============================================================
-- TABLE 2: users
-- ============================================================
CREATE TABLE users (
    tenant_id UUID NOT NULL REFERENCES organizations(id),
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    email TEXT NOT NULL,
    display_name TEXT NOT NULL,
    password_hash TEXT,  -- NULL for SSO-only users
    avatar_url TEXT,
    role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member', 'guest')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'deactivated')),
    mfa_enabled BOOLEAN NOT NULL DEFAULT false,
    mfa_secret_encrypted TEXT,  -- encrypted TOTP secret
    mfa_recovery_hashes TEXT[], -- bcrypt hashed recovery codes
    last_login_at TIMESTAMPTZ,
    locale TEXT DEFAULT 'en',
    timezone TEXT DEFAULT 'UTC',
    settings JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, email)
);
CREATE TRIGGER update_users_updated_at BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE INDEX idx_users_email ON users(tenant_id, email) WHERE deleted_at IS NULL;
CREATE INDEX idx_users_status ON users(tenant_id, status) WHERE deleted_at IS NULL;
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
CREATE POLICY users_tenant_isolation ON users USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY users_tenant_isolation_insert ON users FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Continue this pattern for ALL remaining tables. Generate the complete SQL for:

-- TABLE 3: groups (tenant_id, id PK, name UNIQUE per tenant, description)
-- TABLE 4: group_members (tenant_id, group_id, user_id PK, added_at, added_by)
-- TABLE 5: workspaces (tenant_id, id PK, name, description, settings JSONB, created_by)
-- TABLE 6: workspace_members (tenant_id, workspace_id, user_id PK, role CHECK admin/member/viewer)
-- TABLE 7: folders (tenant_id, id PK, workspace_id, parent_folder_id self-ref, name, path LTREE, depth INT, created_by) + GiST index on path
-- TABLE 8: documents (tenant_id, id PK, workspace_id, folder_id, title, description, lifecycle_state CHECK 8 states, region_pin NOT NULL, custom_metadata JSONB, tags TEXT[], current_version_id, document_class, classification_confidence FLOAT, sha256_hash, total_size_bytes BIGINT, mime_type, created_by, created_at, updated_at, deleted_at) + GIN on custom_metadata + GIN on tags + indexes on (tenant_id, workspace_id, folder_id), (tenant_id, lifecycle_state), (tenant_id, created_at DESC), (tenant_id, document_class)
-- TABLE 9: versions (tenant_id, id PK, document_id, version_number INT, content_blob_id, size_bytes BIGINT, mime_type, sha256_hash, created_by, change_summary, created_at) + UNIQUE(tenant_id, document_id, version_number)
-- TABLE 10: content_blobs (id UUID PK — NOT tenant-scoped PK for dedup, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, storage_class DEFAULT 'hot', size_bytes BIGINT, mime_type, encryption_key_id, reference_count INT DEFAULT 1, created_at) + UNIQUE(tenant_id, sha256_hash)
-- TABLE 11: permissions (tenant_id, id PK, resource_type CHECK doc/folder/workspace, resource_id, principal_type CHECK user/group, principal_id, capability CHECK view/edit/delete/share/admin, granted_by, granted_at, expires_at, valid_from DEFAULT now(), valid_to) + UNIQUE constraint + indexes on resource + principal
-- TABLE 12: share_links (tenant_id, id PK, document_id, created_by, token TEXT UNIQUE globally, password_hash, expires_at, max_views INT, view_count DEFAULT 0, permissions TEXT[], is_active DEFAULT true, created_at, accessed_at)
-- TABLE 13: comments (tenant_id, id PK, document_id, version_id nullable, parent_comment_id nullable for threading, author_id, body TEXT, is_resolved DEFAULT false, resolved_by, resolved_at, created_at, updated_at, deleted_at)
-- TABLE 14: annotations (tenant_id, id PK, document_id, version_id, page_number INT, annotation_type CHECK highlight/note/stamp/drawing, annotation_data JSONB, created_by, created_at, updated_at, deleted_at)
-- TABLE 15: tags_catalog (tenant_id, id PK, name TEXT UNIQUE per tenant, color TEXT, created_by, created_at)
-- TABLE 16: legal_holds (tenant_id, id PK, name, description, matter_reference, applied_by, applied_at, released_by, released_at, is_active DEFAULT true, created_at)
-- TABLE 17: legal_hold_documents (tenant_id, hold_id, document_id PK, applied_at, previous_lifecycle_state TEXT — store the state before hold was applied)
-- TABLE 18: retention_policies (tenant_id, id PK, name, description, document_class_filter TEXT, tag_filter TEXT[], workspace_filter UUID, retain_days INT NOT NULL, then_action CHECK archive/dispose, archive_days INT, is_active DEFAULT true, created_by, created_at, updated_at)
-- TABLE 19: audit_events (id UUID PK — NOT tenant-scoped for append perf, tenant_id, actor_id, actor_type CHECK user/system/api_key, action TEXT NOT NULL, resource_type, resource_id, metadata JSONB, ip_address INET, user_agent TEXT, previous_hash TEXT, event_hash TEXT NOT NULL, created_at DEFAULT now()) PARTITION BY RANGE (created_at) — create partitions for current month and next 3 months. Indexes: (tenant_id, created_at DESC), (tenant_id, resource_type, resource_id), (tenant_id, actor_id, created_at DESC)
-- TABLE 20: sessions (id UUID PK, tenant_id, user_id, token_hash TEXT UNIQUE, ip_address INET, user_agent TEXT, expires_at, last_activity_at, created_at)
-- TABLE 21: api_keys (tenant_id, id PK, user_id, name, key_hash TEXT UNIQUE, key_prefix TEXT, scopes TEXT[], last_used_at, expires_at, created_at, revoked_at)
-- TABLE 22: upload_sessions (tenant_id, id PK, document_id nullable, filename, total_size BIGINT, mime_type, upload_type CHECK single/multipart/tus, storage_region, s3_upload_id TEXT, status CHECK initiated/uploading/scanning/completed/failed/quarantined, parts_completed INT DEFAULT 0, parts_total INT, created_by, created_at, completed_at, expires_at)
-- TABLE 23: ocr_results (tenant_id, id PK, version_id, page_number INT, text_content TEXT, confidence FLOAT, language TEXT, bounding_boxes JSONB, processing_time_ms INT, engine TEXT, created_at) + INDEX(tenant_id, version_id, page_number)
-- TABLE 24: extraction_results (tenant_id, id PK, version_id, extraction_type TEXT, fields JSONB, confidence FLOAT, method CHECK regex/ml/llm, processing_time_ms INT, cost_cents INT, created_at)
-- TABLE 25: document_chunks (tenant_id, id PK, document_id, version_id, chunk_index INT, text_content TEXT, token_count INT, embedding_model TEXT, page_numbers INT[], created_at) + INDEX(tenant_id, document_id, chunk_index)
-- TABLE 26: entities (tenant_id, id PK, document_id, version_id, entity_type TEXT, entity_value TEXT, start_offset INT, end_offset INT, page_number INT, confidence FLOAT, is_pii BOOLEAN DEFAULT false, created_at)
-- TABLE 27: document_fingerprints (tenant_id, document_id PK, minhash_signature BYTEA, simhash BIGINT, created_at, updated_at)
-- TABLE 28: duplicate_candidates (tenant_id, id PK, document_id, candidate_document_id, similarity_score FLOAT, method TEXT, status CHECK pending/confirmed/dismissed, reviewed_by, reviewed_at, created_at)
-- TABLE 29: workflow_definitions (tenant_id, id PK, name, description, definition JSONB NOT NULL, version INT DEFAULT 1, is_active DEFAULT true, created_by, created_at, updated_at)
-- TABLE 30: workflow_instances (tenant_id, id PK, definition_id, document_id, status CHECK pending/running/completed/failed/cancelled, current_step_id TEXT, temporal_workflow_id TEXT, started_by, started_at, completed_at, result JSONB, error_message TEXT)
-- TABLE 31: workflow_tasks (tenant_id, id PK, instance_id, step_id TEXT, step_name TEXT, assignee_id, status CHECK pending/in_progress/completed/rejected/delegated/escalated/skipped, due_at, completed_at, completed_by, outcome TEXT, notes TEXT, delegated_to UUID, created_at) + INDEX(tenant_id, assignee_id, status)
-- TABLE 32: notification_preferences (tenant_id, user_id, channel CHECK email/push/in_app/slack/teams/sms, event_type TEXT, is_enabled DEFAULT true, quiet_start TIME, quiet_end TIME — PRIMARY KEY (tenant_id, user_id, channel, event_type))
-- TABLE 33: notifications (tenant_id, id PK, user_id, title, body, link, is_read DEFAULT false, event_type, resource_type, resource_id, created_at, read_at) + INDEX(tenant_id, user_id, is_read, created_at DESC)
-- TABLE 34: device_tokens (tenant_id, id PK, user_id, platform CHECK ios/android/web, token TEXT, created_at, last_used_at)
-- TABLE 35: signature_requests (tenant_id, id PK, document_id, version_id, requested_by, status CHECK draft/pending/in_progress/completed/declined/expired/cancelled, provider CHECK internal/docusign/adobe_sign, provider_envelope_id TEXT, message TEXT, created_at, completed_at, expires_at)
-- TABLE 36: signature_signers (tenant_id, id PK, request_id, signer_email, signer_name, role CHECK signer/witness/approver/cc, order_index INT, status CHECK pending/viewed/signed/declined, signing_url_token TEXT UNIQUE, signed_at, signature_data JSONB, certificate_id TEXT, ip_address INET)
-- TABLE 37: webhook_subscriptions (tenant_id, id PK, url TEXT NOT NULL, events TEXT[] NOT NULL, secret TEXT NOT NULL, is_active DEFAULT true, failure_count INT DEFAULT 0, last_success_at, last_failure_at, created_by, created_at, updated_at)
-- TABLE 38: webhook_deliveries (tenant_id, id PK, subscription_id, event_type, event_id TEXT, payload JSONB, status CHECK pending/delivered/failed/dead_letter, http_status INT, attempts INT DEFAULT 0, last_attempt_at, next_retry_at, error_message TEXT, created_at) + INDEX(status, next_retry_at) WHERE status IN ('pending', 'failed')
-- TABLE 39: connector_configs (tenant_id, id PK, connector_type CHECK salesforce/sap/m365/google/servicenow/workday/custom, display_name TEXT, config_encrypted JSONB, oauth_tokens_encrypted JSONB, is_active DEFAULT true, last_sync_at, sync_status TEXT, created_by, created_at, updated_at)
-- TABLE 40: subscriptions_billing (tenant_id UUID PK REFERENCES organizations, plan, stripe_customer_id, stripe_subscription_id, status CHECK active/trialing/past_due/cancelled/suspended, user_limit INT, storage_limit_gb INT, current_period_start, current_period_end, created_at, updated_at)
-- TABLE 41: usage_meters (tenant_id, id PK, metric CHECK storage_gb_days/ocr_pages/api_calls/signatures/ai_tokens/active_users, quantity NUMERIC NOT NULL, period_start DATE, period_end DATE, recorded_at DEFAULT now())
-- TABLE 42: outbox (id BIGSERIAL PK, tenant_id NOT NULL, event_type TEXT NOT NULL, aggregate_type TEXT NOT NULL, aggregate_id UUID NOT NULL, payload JSONB NOT NULL, published BOOLEAN DEFAULT false, created_at DEFAULT now(), published_at) + INDEX(published, created_at) WHERE NOT published
-- TABLE 43: tenant_metadata_schemas (tenant_id, id PK, name TEXT, json_schema JSONB NOT NULL, version INT DEFAULT 1, is_active DEFAULT true, created_by, created_at, updated_at)
-- TABLE 44: conversation_history (tenant_id, id PK, conversation_id UUID NOT NULL, user_id, turn_index INT, role CHECK user/assistant, content TEXT, citations JSONB, model_used TEXT, input_tokens INT, output_tokens INT, cost_cents INT, created_at) + INDEX(tenant_id, conversation_id, turn_index)
-- TABLE 45: sso_configs (tenant_id, id PK, provider_type CHECK saml/oidc, display_name, config JSONB NOT NULL — contains IdP metadata URL, certificate, client ID/secret etc., is_active DEFAULT true, created_at, updated_at)

FOR EVERY TABLE WITH tenant_id (tables 2-45 except where noted):
  ALTER TABLE {table_name} ENABLE ROW LEVEL SECURITY;
  ALTER TABLE {table_name} FORCE ROW LEVEL SECURITY;
  CREATE POLICY {table_name}_tenant_isolation ON {table_name}
      USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
  CREATE POLICY {table_name}_tenant_isolation_insert ON {table_name}
      FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA public TO dms_app;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO dms_readonly;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;

-- Apply updated_at triggers to all tables that have updated_at column

Also create 000001_initial_schema.down.sql that drops everything in reverse order.

Also create the Go repository layer in pkg/database/:

database/document_repo.go:
  DocumentRepository interface:
    Create(ctx, tenantID uuid.UUID, doc *model.Document) error
    GetByID(ctx, tenantID, docID uuid.UUID) (*model.Document, error)
    Update(ctx, tenantID uuid.UUID, doc *model.Document) error
    SoftDelete(ctx, tenantID, docID uuid.UUID, deletedBy uuid.UUID) error
    HardDelete(ctx, tenantID, docID uuid.UUID) error  // deletes all related data
    List(ctx, tenantID uuid.UUID, filter DocumentFilter) (*model.DocumentPage, error)
    UpdateLifecycleState(ctx, tenantID, docID uuid.UUID, newState, reason string, changedBy uuid.UUID) error
    CountByTenant(ctx, tenantID uuid.UUID) (int64, error)

  PostgresDocumentRepository implementation:
    - Every query starts with: WHERE tenant_id = $1 (even though RLS is active — defense in depth)
    - Uses cursor-based pagination: WHERE (created_at, id) < ($cursor_time, $cursor_id) ORDER BY created_at DESC, id DESC LIMIT $page_size
    - Maps pgx errors via FromPgError()
    - Every write includes InsertOutboxEvent in the same transaction

  DocumentFilter: WorkspaceID, FolderID, LifecycleState, DocumentClass, Tags, CreatedAfter, CreatedBefore, Query (ILIKE on title), SortBy, SortOrder, PageSize (max 100), PageToken

  DocumentPage: Documents []*model.Document, NextPageToken string, TotalCount int64

Generate the COMPLETE SQL for all 45 tables with all constraints, indexes, RLS policies, and triggers. This is the most critical file in the entire system — every security property depends on it.
```

---

# ████████████████████████████████████████████
# PROMPT 3 OF 30: AUTHENTICATION SERVICE
# ████████████████████████████████████████████

```
Build the complete Authentication Service in services/auth/.

This service handles: user registration, login with password, session management, MFA (TOTP + recovery codes), SSO (SAML 2.0 + OIDC), SCIM 2.0 user provisioning, API key management.

Tech stack: Go 1.22, pgx/v5, go-redis/v9, golang.org/x/crypto/bcrypt (cost factor 12), github.com/pquerna/otp/totp, github.com/go-webauthn/webauthn, github.com/crewjam/saml, github.com/coreos/go-oidc/v3, github.com/go-chi/chi/v5 (HTTP router)

File: services/auth/internal/handler/auth_handler.go
File: services/auth/internal/service/auth_service.go
File: services/auth/internal/repository/user_repo.go
File: services/auth/internal/repository/session_repo.go
File: services/auth/internal/model/user.go
File: services/auth/internal/model/session.go

ENDPOINTS — implement every single one with full validation, error handling, and audit logging:

═══ REGISTRATION ═══
POST /api/v1/auth/register
Request: { "email": "string", "password": "string", "display_name": "string", "tenant_slug": "string" }
Validation:
  - email: valid format (regexp), max 255 chars, lowercase before storing
  - password: min 12 chars, must contain: 1 uppercase, 1 lowercase, 1 digit, 1 special char (!@#$%^&*), max 128 chars
  - display_name: 1-100 chars, trim whitespace
  - tenant_slug: must exist in organizations table, must not be deleted
Business logic:
  1. Verify tenant exists and is active
  2. Check email doesn't already exist for this tenant (case-insensitive)
  3. Hash password with bcrypt, cost factor 12
  4. Create user with role='member', status='active'
  5. Publish event: dms.auth.user_registered.v1
Response 201: { "user_id": "uuid", "email": "string", "display_name": "string", "tenant_id": "uuid" }
Error 400: validation errors with field-level details
Error 409: email already registered (but DON'T say this — say "registration failed, please contact support")
Rate limit: 5 per minute per IP

═══ LOGIN ═══
POST /api/v1/auth/login
Request: { "email": "string", "password": "string", "tenant_slug": "string" }
Business logic:
  1. Find user by (tenant_slug → tenant_id) + email. If not found → generic error.
  2. Check user status is 'active'. If suspended/deactivated → generic error.
  3. Check login attempt count in Redis (key: login_attempts:{tenant_id}:{email}). If >= 5 in last 15 min → return 429 "account temporarily locked".
  4. Compare password with bcrypt hash using crypto/subtle.ConstantTimeCompare on the bcrypt result.
  5. If password wrong: increment attempt counter in Redis (TTL 15min), publish dms.auth.login_failed.v1, return generic "invalid credentials".
  6. If password correct AND mfa_enabled is true:
     - Generate temporary MFA session token (32 bytes, crypto/rand, hex encoded)
     - Store in Redis: mfa_session:{token_hash} → {user_id, tenant_id}, TTL 5 minutes
     - Return 200: { "mfa_required": true, "mfa_session_token": "hex_string" }
  7. If password correct AND mfa_enabled is false:
     - Create session (see session creation below)
     - Clear login attempt counter
     - Update user.last_login_at
     - Publish dms.auth.login_success.v1 with {user_id, ip, user_agent, method: "password"}
     - Return 200: { "session_token": "string", "expires_at": "timestamp", "user": { id, email, display_name, role, tenant_id } }

SESSION CREATION (used by login, MFA verify, SSO callback):
  1. Generate 256-bit random bytes using crypto/rand → hex encode → this is the session_token
  2. Compute token_hash = SHA-256(session_token) → hex string
  3. Insert into sessions table: { id: new UUID, tenant_id, user_id, token_hash, ip_address, user_agent, expires_at: now()+24h, last_activity_at: now(), created_at: now() }
  4. Store in Redis: session:{token_hash} → JSON{user_id, tenant_id, email, role, groups: [group_ids]} with TTL 24h
  5. Check concurrent session count: SELECT count(*) FROM sessions WHERE tenant_id=$1 AND user_id=$2 AND expires_at > now(). If > 5, delete oldest session.
  6. Set HTTP cookie: Set-Cookie: dms_session={session_token}; HttpOnly; Secure; SameSite=Strict; Path=/; Max-Age=86400
  7. Also return session_token in JSON body (client chooses cookie or header)

═══ MFA ═══
POST /api/v1/auth/mfa/verify
Request: { "mfa_session_token": "string", "totp_code": "string" }
  1. Hash mfa_session_token, lookup in Redis (mfa_session:{hash})
  2. If not found or expired → 401
  3. Load user, validate TOTP code using pquerna/otp with 1 step tolerance (30s window before and after)
  4. If valid → create session (same as login success), delete MFA session from Redis
  5. If invalid → increment MFA attempt counter. After 3 failures, delete MFA session (force re-login).
  Rate limit: 5 per minute per mfa_session_token

POST /api/v1/auth/mfa/setup (requires authenticated session)
  1. Generate TOTP secret using totp.Generate(totp.GenerateOpts{Issuer: "SeDoc", AccountName: user.Email})
  2. Encrypt the secret (using crypto/aes with a server-side key from config) before storing in mfa_secret_encrypted
  3. Generate 8 recovery codes: each is 8 chars, alphanumeric, uppercase. Hash each with bcrypt cost 10. Store hashes in mfa_recovery_hashes array.
  4. DO NOT enable MFA yet — user must verify a code first
  5. Return: { "secret": "base32_string", "qr_code_uri": "otpauth://...", "recovery_codes": ["CODE1", "CODE2", ...] }

POST /api/v1/auth/mfa/confirm (requires authenticated session)
  Request: { "totp_code": "string" }
  1. Validate code against the stored (but not yet active) secret
  2. If valid → set mfa_enabled = true
  3. Publish dms.auth.mfa_enabled.v1

POST /api/v1/auth/mfa/disable (requires authenticated session)
  Request: { "totp_code": "string" } OR { "recovery_code": "string" }
  1. Validate TOTP code OR recovery code
  2. Set mfa_enabled = false, clear mfa_secret_encrypted and mfa_recovery_hashes
  3. Publish dms.auth.mfa_disabled.v1

POST /api/v1/auth/mfa/recovery (used instead of TOTP when device is lost)
  Request: { "mfa_session_token": "string", "recovery_code": "string" }
  1. Find the matching recovery hash (try each with bcrypt.CompareHashAndPassword)
  2. If found → remove that hash from the array (one-time use), create session
  3. If user has 0 recovery codes left → warn them to generate new ones

═══ SESSION MANAGEMENT ═══
POST /api/v1/auth/logout
  1. Extract session token from Authorization header or cookie
  2. Hash it, delete from sessions table AND Redis
  3. Clear cookie: Set-Cookie: dms_session=; HttpOnly; Secure; SameSite=Strict; Path=/; Max-Age=0
  4. Publish dms.auth.logout.v1

POST /api/v1/auth/sessions/revoke-all (requires authenticated session)
  1. Delete ALL sessions for this user (except current one, optionally)
  2. Clear all from Redis

GET /api/v1/auth/sessions (requires authenticated session)
  1. List all active sessions for current user: id, ip_address, user_agent, created_at, last_activity_at, is_current
  2. Allows user to see where they're logged in

DELETE /api/v1/auth/sessions/{session_id} (requires authenticated session)
  1. Delete specific session (only if it belongs to current user)

SESSION VALIDATION MIDDLEWARE (used by ALL other services):
  This is a gRPC interceptor AND HTTP middleware.
  1. Extract token from Authorization: Bearer {token} header or dms_session cookie
  2. Hash the token with SHA-256
  3. Look up in Redis: session:{token_hash} → if found, parse JSON, populate context with UserInfo
  4. If Redis miss: look up in sessions table → if found AND not expired, repopulate Redis cache, populate context
  5. If not found or expired → return 401 Unauthenticated
  6. Sliding window: if session expires within 1 hour, extend by 24 hours (update both DB and Redis). Max absolute lifetime: 7 days from creation.
  7. Set context values: tenant_id, user_id, email, role, groups
  8. Set Postgres tenant: the TenantExtractor middleware (from pkg/middleware) handles this

═══ API KEYS ═══
POST /api/v1/auth/api-keys (requires admin role)
  Request: { "name": "string", "scopes": ["documents:read", "documents:write", "search:read", "upload", "webhooks:manage"], "expires_in_days": 365 }
  1. Generate API key: "vdms_" + 48 random alphanumeric chars (crypto/rand)
  2. Hash with SHA-256 before storing
  3. Store: key_hash, key_prefix (first 12 chars of original key for identification), name, scopes, expires_at
  4. Return 201: { "api_key": "vdms_...", "key_id": "uuid", "key_prefix": "vdms_aBcDeF", "name": "...", "scopes": [...], "expires_at": "...", "created_at": "..." }
  5. WARN in response: "This is the only time the full API key will be shown. Store it securely."
  6. Limit: max 20 API keys per user

DELETE /api/v1/auth/api-keys/{key_id}
  1. Set revoked_at = now() (don't hard delete)
  2. Remove from Redis cache if present

GET /api/v1/auth/api-keys (requires authenticated session)
  1. List user's API keys: key_id, key_prefix, name, scopes, created_at, last_used_at, expires_at, revoked_at

API KEY VALIDATION (alternative to session token):
  1. Extract from Authorization: Bearer vdms_...
  2. Detect it's an API key (starts with "vdms_")
  3. Hash it, look up in api_keys table
  4. Check not revoked, not expired
  5. Check scope matches the endpoint being accessed
  6. Update last_used_at (debounced — max once per minute)
  7. Populate context with user_id, tenant_id, role (from the key's user)

═══ SSO — SAML 2.0 ═══
Admin configures SAML in tenant settings, providing:
  - IdP Metadata URL or XML
  - IdP Certificate
  - Attribute mappings (which SAML attribute maps to email, display_name, groups)

GET /api/v1/auth/saml/{tenant_slug}/metadata
  1. Generate SP metadata XML with:
     - EntityID: https://app.vaultdms.com/api/v1/auth/saml/{tenant_slug}/metadata
     - ACS URL: https://app.vaultdms.com/api/v1/auth/saml/{tenant_slug}/acs
     - NameIDFormat: urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress
     - Signing certificate (SP's own certificate)

GET /api/v1/auth/saml/{tenant_slug}/login
  1. Load tenant's SAML config from sso_configs table
  2. Generate AuthnRequest with unique ID, store request ID in Redis for replay prevention
  3. Redirect to IdP SSO URL with encoded AuthnRequest

POST /api/v1/auth/saml/{tenant_slug}/acs
  1. Parse SAMLResponse from form POST
  2. Validate:
     a. Signature against IdP certificate
     b. Destination matches our ACS URL
     c. NotBefore <= now <= NotOnOrAfter (with 2 minute clock skew tolerance)
     d. InResponseTo matches a request ID we stored (prevent replay) — delete from Redis after use
     e. Audience matches our EntityID
  3. Extract attributes: email, display_name, groups (based on tenant's attribute mapping config)
  4. Find or create user:
     - If user with this email exists in this tenant → update display_name, sync groups
     - If user doesn't exist → create with role='member', status='active', no password (SSO-only)
  5. Sync groups: if SAML assertion includes group claims, map to DMS groups (based on tenant's group mapping config)
  6. Create session (same as login success)
  7. Redirect to: https://app.vaultdms.com/?session_setup=true (frontend reads cookie)

═══ SSO — OIDC ═══
GET /api/v1/auth/oidc/{tenant_slug}/login
  1. Load tenant's OIDC config (issuer URL, client_id, client_secret)
  2. Generate PKCE code_verifier (43-128 chars, crypto/rand), compute code_challenge (SHA-256, base64url)
  3. Store code_verifier in Redis: oidc_pkce:{state} → code_verifier, TTL 10 minutes
  4. Redirect to IdP authorize endpoint with: response_type=code, scope=openid profile email groups, redirect_uri, state, code_challenge, code_challenge_method=S256

GET /api/v1/auth/oidc/{tenant_slug}/callback?code=X&state=Y
  1. Validate state parameter, retrieve code_verifier from Redis
  2. Exchange code for tokens at IdP token endpoint (include code_verifier)
  3. Validate ID token: signature, issuer, audience, expiry, nonce
  4. Extract claims: email, name, groups (from userinfo endpoint if needed)
  5. Find or create user + sync groups (same as SAML)
  6. Create session, redirect to app

═══ SCIM 2.0 ═══
These endpoints are authenticated with a SCIM bearer token configured per tenant in sso_configs.

GET    /api/v1/scim/v2/Users?filter=userName eq "john@example.com"&startIndex=1&count=100
POST   /api/v1/scim/v2/Users — create user (map SCIM schema to our user model)
GET    /api/v1/scim/v2/Users/{id}
PUT    /api/v1/scim/v2/Users/{id} — full replace
PATCH  /api/v1/scim/v2/Users/{id} — partial update (SCIM PATCH operations)
DELETE /api/v1/scim/v2/Users/{id} — set status='deactivated', revoke all sessions

GET    /api/v1/scim/v2/Groups
POST   /api/v1/scim/v2/Groups
PATCH  /api/v1/scim/v2/Groups/{id} — add/remove members
DELETE /api/v1/scim/v2/Groups/{id}

SCIM response format must comply with RFC 7644. Include proper schemas, meta, and pagination.

═══ SECURITY RULES — ABSOLUTE, NON-NEGOTIABLE ═══
1. NEVER log: passwords, session tokens, API keys, MFA secrets, recovery codes, SAML assertions
2. NEVER return password_hash in ANY API response — remove from all serialization
3. Use crypto/subtle.ConstantTimeCompare for ALL secret comparisons (tokens, passwords, API keys)
4. All cookies: HttpOnly=true, Secure=true, SameSite=Strict
5. Session tokens generated with crypto/rand, NEVER math/rand
6. Failed login response is ALWAYS the same: {"error": "invalid credentials"} — never reveal if email exists
7. SAML assertions consumed exactly once (store assertion ID, reject duplicates)
8. All auth events produce audit log entries (success AND failure)
9. Password hash stored as string, bcrypt with cost 12 minimum
10. MFA secret encrypted with AES before DB storage (server-side key from Vault/config)

═══ TESTS ═══
Write comprehensive tests for:
- Registration: success, duplicate email, weak password, invalid tenant
- Login: success, wrong password, wrong email, locked account, MFA flow
- Session: create, validate, expire, sliding window, concurrent limit, revoke
- MFA: setup, confirm, verify, recovery code, disable
- API keys: create, use, revoke, scope enforcement, expiry
- SAML: metadata generation, assertion validation, replay prevention, user provisioning
- OIDC: PKCE flow, token validation, user provisioning
- SCIM: user CRUD, group management, filtering
- Rate limiting: verify lockout after 5 failures
- Security: no sensitive data in logs, constant-time comparison
```

---

Due to the extreme length required for 30 prompts at this detail level, I'll continue with the remaining 27 prompts at the same enterprise depth. Each prompt follows the same pattern: complete endpoint specs, business logic rules, security rules, error handling, and test requirements.

# ████████████████████████████████████████████
# PROMPT 4 OF 30: AUTHORIZATION — OPA POLICY ENGINE
# ████████████████████████████████████████████

```
Build the Policy Service in services/policy/. This is the authorization engine. Uses Open Policy Agent (OPA) embedded as a Go library — NOT as a sidecar process.

PERFORMANCE REQUIREMENT: p99 < 5ms for CheckPermission. This is the hottest path in the system.

Dependencies: github.com/open-policy-agent/opa/rego, github.com/open-policy-agent/opa/ast

Architecture:
- OPA policies compiled to Rego and evaluated in-process
- Permission data loaded from Postgres, cached in Redis (60s TTL)
- gRPC API only (no REST — this is internal-only, called by every other service)
- Stateless — can scale horizontally without limit

gRPC API (from proto/vaultdms/v1/policy.proto):

rpc CheckPermission(CheckPermissionRequest) returns (CheckPermissionResponse);
  CheckPermissionRequest:
    subject_type: string ("user" or "group")
    subject_id: string (UUID)
    action: string ("view", "edit", "delete", "share", "admin")
    resource_type: string ("document", "folder", "workspace")
    resource_id: string (UUID)
    context: google.protobuf.Struct — additional ABAC attributes:
      - workspace_id: string (for workspace admin check)
      - folder_id: string (for folder permission cascade)
      - lifecycle_state: string (for disposed document check)
      - region_pin: string (for region-based access)
      - user_role: string (org-level role: owner/admin/member/guest)
  CheckPermissionResponse:
    allowed: bool
    reason: string (empty if allowed, explanation if denied — for debugging only, NOT shown to end users)

rpc BatchCheckPermissions(BatchCheckPermissionsRequest) returns (BatchCheckPermissionsResponse);
  Up to 50 checks in a single call, evaluated in parallel.
  Used by: document list endpoints (check view permission on each result).

rpc GrantPermission(GrantPermissionRequest) returns (Permission);
  Creates a permission record in the database.
  Invalidates Redis cache for the affected resource.
  Publishes: dms.permission.granted.v1

rpc RevokePermission(RevokePermissionRequest) returns (google.protobuf.Empty);
  Soft-deletes the permission (sets valid_to = now()).
  Invalidates Redis cache.
  Publishes: dms.permission.revoked.v1

rpc ListPermissions(ListPermissionsRequest) returns (ListPermissionsResponse);
  Returns all permissions for a given resource, with resolved user/group names.
  Used by: the permission editor UI.

rpc ListUserPermissions(ListUserPermissionsRequest) returns (ListUserPermissionsResponse);
  Returns all effective permissions for a user (direct + inherited from groups).

rpc GetResourcePermissionGroups(GetResourcePermissionGroupsRequest) returns (GetResourcePermissionGroupsResponse);
  Returns the list of group IDs (and "everyone" if applicable) that have VIEW or higher access to a resource.
  CRITICAL: This is called by the Search Service during indexing to populate the "readable_by" field.
  Must include: direct user permissions (as individual IDs), group permissions, workspace admin access, org admin access.

IMPLEMENTATION:

1. OPA REGO POLICY — embed as a compiled bundle:

```rego
package vaultdms.authz

import future.keywords.in
import future.keywords.if

# Default: deny everything
default allow := false
default deny := false

# ─── ALLOW RULES (any one matching = access granted, unless a deny rule fires) ───

# Rule 1: Direct permission grant
allow if {
    some p in data.permissions
    p.principal_type == input.subject_type
    p.principal_id == input.subject_id
    p.resource_type == input.resource_type
    p.resource_id == input.resource_id
    capability_includes(p.capability, input.action)
    not permission_expired(p)
}

# Rule 2: Permission via group membership
allow if {
    input.subject_type == "user"
    some group_id in data.user_groups
    some p in data.permissions
    p.principal_type == "group"
    p.principal_id == group_id
    p.resource_type == input.resource_type
    p.resource_id == input.resource_id
    capability_includes(p.capability, input.action)
    not permission_expired(p)
}

# Rule 3: Folder permission cascades to documents in that folder
allow if {
    input.resource_type == "document"
    input.context.folder_id != ""
    some p in data.permissions
    matches_principal(p, input)
    p.resource_type == "folder"
    p.resource_id == input.context.folder_id
    capability_includes(p.capability, input.action)
    not permission_expired(p)
}

# Rule 4: Workspace permission cascades to all content
allow if {
    input.resource_type in {"document", "folder"}
    input.context.workspace_id != ""
    some p in data.permissions
    matches_principal(p, input)
    p.resource_type == "workspace"
    p.resource_id == input.context.workspace_id
    capability_includes(p.capability, input.action)
    not permission_expired(p)
}

# Rule 5: Workspace admin has all permissions on workspace contents
allow if {
    input.subject_type == "user"
    input.context.workspace_id != ""
    some wm in data.workspace_members
    wm.user_id == input.subject_id
    wm.workspace_id == input.context.workspace_id
    wm.role == "admin"
}

# Rule 6: Organization admin/owner has all permissions
allow if {
    input.subject_type == "user"
    input.context.user_role in {"admin", "owner"}
}

# ─── DENY RULES (override allow) ───

# Deny: disposed documents cannot be accessed by non-admins
deny if {
    input.resource_type == "document"
    input.context.lifecycle_state == "disposed"
    not input.context.user_role in {"admin", "owner"}
}

# Deny: deactivated users cannot access anything
deny if {
    input.context.user_status == "deactivated"
}

# ─── HELPER FUNCTIONS ───

matches_principal(p, inp) if {
    p.principal_type == inp.subject_type
    p.principal_id == inp.subject_id
}

matches_principal(p, inp) if {
    inp.subject_type == "user"
    p.principal_type == "group"
    some gid in data.user_groups
    p.principal_id == gid
}

# Capability hierarchy: admin > delete > edit > share > view
capability_includes(granted, requested) if {
    hierarchy := {"admin": 50, "delete": 40, "edit": 30, "share": 20, "view": 10}
    hierarchy[granted] >= hierarchy[requested]
}

permission_expired(p) if {
    p.expires_at != ""
    time.now_ns() > time.parse_rfc3339_ns(p.expires_at)
}

permission_expired(p) if {
    p.valid_to != ""
    time.now_ns() > time.parse_rfc3339_ns(p.valid_to)
}

# ─── FINAL DECISION ───
final_decision := "allow" if {
    allow
    not deny
}

final_decision := "deny" if {
    not allow
}

final_decision := "deny" if {
    deny
}
```

2. PERMISSION CACHE:
   On CheckPermission call:
   a. Build cache key: perm:{tenant_id}:{resource_type}:{resource_id}
   b. Check Redis for cached permissions for this resource
   c. If cache miss:
      - Query Postgres: SELECT * FROM permissions WHERE tenant_id=$1 AND resource_type=$2 AND resource_id=$3 AND (valid_to IS NULL OR valid_to > now()) AND (expires_at IS NULL OR expires_at > now())
      - Also load parent permissions (if document → load folder permissions, workspace permissions)
      - Cache in Redis with 60s TTL
   d. Build cache key for user groups: groups:{tenant_id}:{user_id}
   e. Check Redis for cached group memberships
   f. If cache miss:
      - Query: SELECT group_id FROM group_members WHERE tenant_id=$1 AND user_id=$2
      - Cache with 60s TTL
   g. Also load workspace membership: workspace_members for workspace admin check
   h. Prepare OPA input: {subject_type, subject_id, action, resource_type, resource_id, context}
   i. Prepare OPA data: {permissions: [...], user_groups: [...], workspace_members: [...]}
   j. Evaluate policy → return result

3. CACHE INVALIDATION:
   On GrantPermission / RevokePermission:
   - Write to Postgres
   - Delete Redis keys: perm:{tenant_id}:{resource_type}:{resource_id}
   - If it's a group permission change: delete groups:{tenant_id}:* for all users in that group
   - Publish NATS event: dms.permission.changed.v1 with {resource_type, resource_id, affected_user_ids[]}
   
   The Search Service listens for permission.changed events and re-indexes affected documents' readable_by field.

4. PERFORMANCE:
   - Rego policy compiled once at startup, reused for all evaluations
   - Permission data retrieved from Redis in < 1ms
   - OPA evaluation: < 0.5ms for a typical policy with 20 permissions
   - Total: p99 < 5ms with Redis hit, p99 < 20ms with Redis miss (Postgres query)
   - Batch: 50 permission checks in parallel using goroutines, p99 < 30ms

5. TEMPORAL PERMISSIONS (for compliance "time machine"):
   GetPermissionsAsOf(ctx, tenantID, resourceType, resourceID uuid.UUID, asOf time.Time) ([]Permission, error)
   - Query: WHERE valid_from <= $asOf AND (valid_to IS NULL OR valid_to >= $asOf)
   - Used for audit questions like "who had access to doc X on March 15?"

TESTS:
- Direct permission allows access
- Group membership grants access
- Permission hierarchy: admin can view, editor can view, viewer cannot edit
- Folder permission cascades to documents within
- Workspace permission cascades to folders and documents
- Workspace admin has all permissions
- Org admin has all permissions
- Expired permissions deny access
- Revoked permissions (valid_to set) deny access
- Disposed documents blocked for non-admins
- Deactivated users blocked
- Cache invalidation: revoke permission → user loses access within 60s
- Batch permission check: 50 mixed allow/deny → all correct
- Performance: 10,000 checks in < 5 seconds (benchmark test)
- GetResourcePermissionGroups returns correct group IDs for search indexing
- Temporal query: permissions at a past date returns correct set
```

---

I need to continue with prompts 5-30. Due to the massive size, let me create the remaining prompts as a continuation file.

# ████████████████████████████████████████████
# PROMPT 5 OF 30: DOCUMENT SERVICE
# ████████████████████████████████████████████

```
Build the Document Service in services/document/. This is the core service — document CRUD, versioning, folders, metadata, lifecycle state machine, tags, and share links.

Router: chi/v5 for REST, grpc-gateway for gRPC mapping.

FOLDER ENDPOINTS:
POST   /api/v1/workspaces/{workspace_id}/folders — {name, parent_folder_id?}. Create folder, generate ltree path = parent.path || '.' || sanitize(name). Max depth 20. Sanitize: lowercase, replace spaces/special chars with underscore, max 255 chars.
GET    /api/v1/workspaces/{workspace_id}/folders?parent_id=X — List children. Include: id, name, path, depth, document_count (subquery), child_folder_count.
GET    /api/v1/folders/{id} — Single folder with breadcrumb (ancestors from ltree path).
PATCH  /api/v1/folders/{id} — Rename or move. On move: update path of folder AND all descendants in a single UPDATE using ltree operations: UPDATE folders SET path = $new_parent_path || subpath(path, nlevel($old_path)-1) WHERE path <@ $old_path. Must be in a transaction.
DELETE /api/v1/folders/{id} — Soft delete. FAIL if folder contains documents or child folders (return 409 "folder not empty").

DOCUMENT ENDPOINTS:
POST   /api/v1/documents — {workspace_id, folder_id, title, description?, region_pin, custom_metadata?, tags?[]}. Permission: edit on folder. Validate region_pin is in allowed list. Validate custom_metadata against tenant's JSON Schema. State starts as 'draft'. Publish document.created.v1.
GET    /api/v1/documents/{id} — Full document with: current version, thumbnail URL, permission summary (what can the current user do), workflow status if active. Permission: view.
PATCH  /api/v1/documents/{id} — Update title, description, custom_metadata, tags. Permission: edit. BLOCKED if lifecycle_state=legal_hold (return 409). Validate custom_metadata against schema. Publish document.updated.v1 with changed_fields[].
DELETE /api/v1/documents/{id} — Soft delete. Permission: delete. BLOCKED if under legal hold. Publish document.deleted.v1.
POST   /api/v1/documents/{id}/move — {target_folder_id, target_workspace_id?}. BLOCKED if region would change. Permission: edit on source AND target.

DOCUMENT LISTING:
GET /api/v1/workspaces/{workspace_id}/documents?folder_id=X&lifecycle_state=active&document_class=contract&tags=urgent,finance&created_after=2025-01-01&created_before=2026-01-01&sort_by=created_at&sort_order=desc&page_size=50&page_token=eyJ...
  Response: { documents: [...], next_page_token: "...", total_count: 157 }
  Page token is base64-encoded JSON: {created_at: "timestamp", id: "uuid"} for cursor pagination.
  SQL: WHERE tenant_id=$1 AND workspace_id=$2 AND folder_id=$3 AND lifecycle_state=$4 ... AND (created_at, id) < ($cursor_time, $cursor_id) ORDER BY created_at DESC, id DESC LIMIT $page_size+1
  If result count = page_size+1, there are more results: return page_size results and encode last item as next_page_token.

VERSION MANAGEMENT:
GET  /api/v1/documents/{id}/versions — List all versions. Include: version_number, size, mime_type, created_by (name+email), created_at, change_summary.
POST /api/v1/documents/{id}/versions — {content_blob_id, change_summary}. Called after upload completes. Creates version with next version_number. Sets document.current_version_id. If document was 'active', previous version's document stays active (this version becomes current). Publish version.created.v1.

LIFECYCLE STATE MACHINE:
POST /api/v1/documents/{id}/lifecycle — {action: "submit_for_review"|"approve"|"reject"|"archive"|"dispose"|"apply_hold"|"release_hold", reason?: string}
  
  VALID TRANSITIONS (enforce in service layer with exhaustive switch):
  ┌─────────────┬──────────────────┬──────────────────┬─────────────────────────────────────────────┐
  │ Current     │ Action           │ New State        │ Requirements                                │
  ├─────────────┼──────────────────┼──────────────────┼─────────────────────────────────────────────┤
  │ draft       │ submit_for_review│ in_review        │ Must have at least 1 version                │
  │ in_review   │ approve          │ active           │ Requires edit+ permission                   │
  │ in_review   │ reject           │ draft            │ Requires edit+ permission, reason required  │
  │ active      │ archive          │ retained         │ Requires admin permission OR system trigger  │
  │ superseded  │ archive          │ retained         │ Same                                        │
  │ retained    │ archive          │ archived         │ After archive_days from retention policy     │
  │ retained    │ restore          │ active           │ Admin only, if doc is still current          │
  │ archived    │ dispose          │ disposed         │ Admin only, reason required                  │
  │ ANY (not disposed) │ apply_hold│ legal_hold       │ Admin + legal_hold.apply permission          │
  │ legal_hold  │ release_hold     │ (previous state) │ Admin + legal_hold.release permission        │
  └─────────────┴──────────────────┴──────────────────┴─────────────────────────────────────────────┘
  
  Any transition not in this table → return 400 "Invalid state transition from {current} via {action}"
  
  On apply_hold: store current lifecycle_state in legal_hold_documents.previous_lifecycle_state
  On release_hold: restore the previous state
  
  LEGAL HOLD BLOCKS (enforce with middleware or service-layer check):
  - DELETE (soft or hard)
  - PATCH (metadata changes that affect retention)
  - Version creation (content modification)
  - Moving to different folder/workspace
  ALLOWS: viewing, downloading, commenting, annotating

  Publish document.state_changed.v1 with {from_state, to_state, action, reason, changed_by}

CUSTOM METADATA:
GET /api/v1/tenants/metadata-schema — Returns the tenant's JSON Schema definition for custom fields.
PUT /api/v1/tenants/metadata-schema — Update schema (admin only). Body is a JSON Schema object defining allowed fields with types, required fields, enums.
  On write to document.custom_metadata: validate against schema using github.com/santhosh-tekuri/jsonschema/v5
  Required fields enforced only when lifecycle_state transitions to 'active' (drafts can have incomplete metadata).

TAGS:
GET    /api/v1/tags — List tenant's tag catalog with document counts.
POST   /api/v1/tags — {name, color} — Create tag. Validate: unique per tenant, max 50 chars, valid hex color.
DELETE /api/v1/tags/{id} — Remove tag from catalog AND from all documents that use it (background job).
  Documents.tags is TEXT[] — tags are stored as strings, not FK references. This is intentional for query performance (GIN index on array).

SHARE LINKS:
POST   /api/v1/documents/{id}/share-links — {password?, expires_in_hours?, max_views?, permissions?: ["view"|"download"]}. Permission: share. Generate token: 32 bytes crypto/rand → base64url. If password provided, hash with bcrypt. Return: {id, token, url: "https://app.vaultdms.com/shared/{token}", expires_at, max_views}.
GET    /api/v1/documents/{id}/share-links — List active share links for document (admin/share permission).
DELETE /api/v1/share-links/{id} — Deactivate link.

PUBLIC SHARE ACCESS (NO AUTH REQUIRED):
GET /api/v1/shared/{token} — Validate token, check expiry, check max_views. If password_hash exists, return {password_required: true}. Otherwise return document metadata + download URL.
POST /api/v1/shared/{token}/verify-password — {password} → if correct, return document metadata + download URL, increment view_count.
  SECURITY: constant-time token comparison. Rate limit: 10/min/IP.

EVERY WRITE OPERATION must:
1. Check permission via Policy Service gRPC call
2. Execute business logic in a database transaction
3. Insert outbox event in the same transaction
4. Return the result
5. The outbox publisher will async-deliver the event to NATS

Write tests for EVERY state transition (valid AND invalid), every CRUD operation, pagination, folder operations, metadata validation, share link lifecycle, and permission enforcement.
```

# ████████████████████████████████████████████
# PROMPT 6 OF 30: STORAGE SERVICE
# ████████████████████████████████████████████

```
Build the Storage Service in services/storage/. Handles all file upload/download, deduplication, virus scanning, encryption, and storage lifecycle management.

Dependencies: minio-go/v7, h2non/filetype (MIME detection from magic bytes), ClamAV TCP client

UPLOAD FLOW:
POST /api/v1/uploads/initiate
  Request: {document_id, filename, size_bytes, mime_type, sha256_hash, region_pin}
  Logic:
  1. Permission check: user has 'edit' on document_id
  2. Validate size: max 5GB (standard plan), 50GB (enterprise). Return 413 if exceeded.
  3. Validate MIME type against blocklist: .exe, .bat, .cmd, .sh, .ps1, .com, .scr, .pif, .msi, .dll, .sys → reject with 415
  4. Check dedup: SELECT id FROM content_blobs WHERE tenant_id=$1 AND sha256_hash=$2 AND storage_region=$3
     If exists → return {deduplicated: true, content_blob_id: existing_id} (skip upload)
  5. Determine strategy:
     - size < 100MB: single presigned PUT URL, 1 hour expiry
     - 100MB-5GB: S3 multipart. Create multipart upload via MinIO. Generate presigned PUT URL per part (100MB parts). Return all URLs.
     - >5GB: return TUS endpoint URL
  6. Create upload_session record with status='initiated'
  7. Return: {upload_id, upload_type: "single"|"multipart"|"tus", presigned_urls: [{part_number: 1, url: "https://..."}], part_size_bytes: 104857600, expires_at}
  
  Bucket selection: dms-{region_pin}-hot (e.g., dms-us-east-1-hot)
  Object key: {tenant_id}/{document_id}/{version_uuid}/{filename}

POST /api/v1/uploads/{upload_id}/complete
  For multipart: receive {parts: [{part_number, etag}]} and call S3 CompleteMultipartUpload
  For single: just verify the object exists in S3
  Then trigger async pipeline:
  1. Update upload_session status = 'scanning'
  2. VIRUS SCAN: connect to ClamAV daemon via TCP (port 3310), send INSTREAM command, stream the file, read response. If "FOUND" → quarantine.
  3. MIME VERIFICATION: download first 8KB, use h2non/filetype to detect real MIME type from magic bytes. If detected type doesn't match declared type AND detected type is executable → quarantine.
  4. SHA-256 VERIFICATION: stream entire file through sha256.New(), compare hash with client-provided hash. If mismatch → reject (possible corruption or tampering).
  5. ENCRYPTION: generate DEK via KeyManager.GenerateDataKey(tenant_kek_id). Encrypt file in-stream using AES-256-GCM. Write encrypted file back to S3 (overwrite). Store encrypted DEK reference in content_blob.encryption_key_id.
  6. Create content_blob record: {id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, storage_class: "hot", size_bytes, mime_type, encryption_key_id, reference_count: 1}
  7. Update upload_session status = 'completed'
  8. Publish version.uploaded.v1 → triggers OCR, preview, embedding pipelines
  
  QUARANTINE FLOW:
  1. Move object to dms-quarantine bucket
  2. Update upload_session status = 'quarantined'
  3. Publish upload.quarantined.v1
  4. Notify user via notification service
  5. Log audit event
  6. Return error to client: {error: "MALWARE_DETECTED", message: "File quarantined by security scan"}

DOWNLOAD FLOW:
GET /api/v1/downloads/{document_id}/versions/{version_id}
  1. Permission check: view on document
  2. Load version → content_blob
  3. If share link context → proxied mode (no presigned URL, stream through service)
  4. If authenticated user → presigned GET URL mode:
     a. Decrypt DEK: KeyManager.DecryptDataKey(kek_id, encrypted_dek)
     b. Cannot give presigned URL to encrypted file directly — must proxy and decrypt
     c. Stream: S3 GET → AES-GCM decrypt in-stream → HTTP response
     d. Headers: Content-Type, Content-Disposition: attachment; filename="original_name", Content-Length, ETag
  5. If watermark requested (share links, DLP policy):
     a. If PDF: apply watermark overlay with accessor email + timestamp + doc ID using go PDF library
     b. Stream watermarked version
  6. Audit event: document.downloaded.v1

DELETE /api/v1/blobs/{content_blob_id} (internal only)
  1. Decrement reference_count
  2. If reference_count = 0: schedule S3 deletion (delayed 24h for safety)
  3. A daily cleanup job actually deletes objects with reference_count=0 and updated_at > 24h ago

STORAGE LIFECYCLE (daily cron job):
  1. Documents with lifecycle_state='active' AND no access in 90 days → transition blob to warm:
     S3 CopyObject with StorageClass=STANDARD_IA (or MinIO tier), update content_blobs.storage_class
  2. Documents with lifecycle_state='archived' → cold: Glacier Instant Retrieval / MinIO cold tier
  3. Documents with lifecycle_state='disposed' AND retention still active → deep cold

TUS PROTOCOL (for >5GB files):
  POST   /api/v1/tus/ → Create upload, return Location header
  HEAD   /api/v1/tus/{id} → Return Upload-Offset (how many bytes received)
  PATCH  /api/v1/tus/{id} → Receive chunk, append to S3 multipart
  DELETE /api/v1/tus/{id} → Cancel upload
  Implement TUS v1.0.0 spec headers: Tus-Resumable, Upload-Length, Upload-Offset, Upload-Metadata

ENCRYPTION ARCHITECTURE:
  Every file is encrypted with AES-256-GCM using envelope encryption:
  - DEK (Data Encryption Key): random 256-bit key, unique per file
  - KEK (Key Encryption Key): managed by KMS, unique per tenant
  - DEK is encrypted with KEK and stored in content_blobs.encryption_key_id (which references the KEK, not the DEK itself — the encrypted DEK is stored alongside the blob or in a separate key table)
  
  Actually, store encrypted DEK as a separate column: content_blobs.encrypted_dek BYTEA
  On download: get encrypted_dek from DB → decrypt with KEK via KMS → decrypt file in-stream

KeyManager implementations:
  LocalKeyManager: uses a static master key from env var. FOR DEV ONLY. Logs WARNING on startup.
  VaultKeyManager: uses HashiCorp Vault Transit engine. Encrypt/decrypt DEK via Vault API.
  AWSKMSKeyManager: uses AWS KMS GenerateDataKey and Decrypt APIs.

Write tests for: single upload, multipart upload, dedup detection, virus scan quarantine, SHA-256 mismatch rejection, MIME type mismatch detection, encryption round-trip (encrypt on upload → decrypt on download), storage class transitions, TUS resume after disconnect, presigned URL expiry, watermarked download.
```

# ████████████████████████████████████████████
# PROMPT 7 OF 30: PREVIEW SERVICE (Python)
# ████████████████████████████████████████████

```
Build the Preview Service in services/preview/ using Python 3.12, FastAPI, and Celery with Redis broker.

This is a WORKER service — consumes events from NATS, processes files asynchronously, stores previews in S3.

Project structure:
services/preview/
├── app/
│   ├── main.py          # FastAPI app + startup (NATS consumer registration)
│   ├── config.py        # Settings from env vars using pydantic-settings
│   ├── worker.py        # Celery app configuration
│   ├── tasks/
│   │   ├── preview.py   # generate_preview Celery task
│   │   └── video.py     # generate_hls Celery task (for video transcoding)
│   ├── processors/
│   │   ├── pdf.py       # PDF thumbnail + page rendering
│   │   ├── office.py    # Office format conversion via LibreOffice
│   │   ├── image.py     # Image resize + EXIF strip
│   │   ├── video.py     # Video thumbnail + HLS
│   │   └── text.py      # Text/code snippet extraction
│   ├── nats_consumer.py # NATS JetStream consumer
│   ├── s3_client.py     # MinIO/S3 wrapper
│   └── api/
│       └── routes.py    # REST endpoints for on-demand preview
├── requirements.txt
├── Dockerfile
└── tests/

NATS Consumer: subscribe to dms.version.uploaded.v1
On event: enqueue Celery task generate_preview(tenant_id, document_id, version_id, content_blob_id, mime_type, region_pin, storage_bucket, storage_key)

Celery task generate_preview:
1. Download file from S3 to temp directory (use tempfile.mkdtemp())
2. Determine processor based on mime_type:

   PDF (application/pdf):
   - Use pdf2image (poppler): convert_from_path(path, dpi=150, first_page=1, last_page=1, fmt='jpeg', jpegopt={'quality': 80, 'optimize': True})
   - Thumbnail: resize to 256x256, center crop, save as JPEG
   - Preview pages: convert first 10 pages at 1200px width, save as PNG
   - Extract page_count using PyMuPDF: doc = fitz.open(path); page_count = len(doc)

   Office (application/vnd.openxmlformats-officedocument.*, application/msword, application/vnd.ms-*):
   - Convert to PDF: subprocess.run(['soffice', '--headless', '--convert-to', 'pdf', '--outdir', tmpdir, filepath], timeout=60, capture_output=True)
   - If LibreOffice fails (corrupt file, timeout) → set status='failed', log error, return
   - Then process the PDF output same as above

   Images (image/jpeg, image/png, image/gif, image/webp, image/tiff):
   - Use Pillow: img = Image.open(path)
   - Strip EXIF: from PIL.ExifTags import Base; img_data = list(img.getdata()); clean_img = Image.new(img.mode, img.size); clean_img.putdata(img_data) — OR use img.info.pop('exif', None) then save
   - Thumbnail: img.thumbnail((256, 256), Image.LANCZOS), save JPEG quality 80
   - Preview: resize max dimension to 2000px, maintain aspect ratio

   Video (video/mp4, video/webm, video/quicktime):
   - Thumbnail: ffmpeg -i input -ss 00:00:05 -vframes 1 -vf scale=256:-1 thumb.jpg
   - 5 preview frames: at 0%, 20%, 40%, 60%, 80% of duration
   - subprocess.run with timeout=120

   Text/Code (text/plain, text/csv, application/json, text/xml, text/markdown, text/x-python, etc.):
   - Read first 5000 chars, store as text_preview (no image needed)
   - Detect language for syntax highlighting hint

   Email (message/rfc822, application/vnd.ms-outlook):
   - Parse with email.parser (EML) or extract-msg (MSG)
   - Extract: from, to, subject, date, body (text or HTML), attachment list
   - If HTML body: convert to PDF using weasyprint, then thumbnail
   - Store parsed metadata as JSON

3. Upload previews to S3:
   Bucket: dms-{region_pin}-previews
   Keys:
   - {tenant_id}/{document_id}/{version_id}/thumb.jpg
   - {tenant_id}/{document_id}/{version_id}/page_{n}.png (n = 1..10)
   - {tenant_id}/{document_id}/{version_id}/metadata.json (page_count, has_text, dimensions)

4. Store preview metadata in Redis:
   Key: preview:{tenant_id}:{version_id}
   Value: JSON{thumbnail_key, page_count, preview_page_keys[], status: "ready", generated_at}
   TTL: 24 hours

5. Publish event: dms.version.preview_ready.v1

6. ALWAYS clean up temp directory in a finally block. Even on exceptions.

REST API (for on-demand requests):
GET /api/v1/previews/{document_id}/thumbnail → redirect 302 to presigned S3 URL (5 min expiry)
GET /api/v1/previews/{document_id}/pages/{page_number} → redirect to presigned URL
POST /api/v1/previews/{document_id}/regenerate → re-enqueue preview generation
GET /api/v1/previews/{document_id}/status → {status: "ready"|"processing"|"failed", page_count, error?}

Error handling:
- Password-protected files → status='password_protected', notify user
- Corrupt files → status='failed', log error with file details (NOT file content)
- Files > 500MB → status='too_large', skip preview
- LibreOffice timeout (>60s) → status='conversion_timeout'
- Retry: 3 attempts with exponential backoff (5s, 30s, 120s)

Celery config:
  broker = redis://redis:6379/1
  result_backend = redis://redis:6379/2
  task_time_limit = 600  # 10 min hard limit
  task_soft_time_limit = 540  # 9 min soft limit (raises SoftTimeLimitExceeded)
  worker_concurrency = 4  # per worker instance
  worker_max_memory_per_child = 2048000  # 2GB, restart worker if exceeded
  task_acks_late = True  # re-queue if worker crashes mid-task

Dockerfile:
FROM python:3.12-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libreoffice-core libreoffice-writer libreoffice-calc libreoffice-impress \
    poppler-utils ffmpeg libmagic1 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY app/ app/
RUN useradd -m appuser && chown -R appuser:appuser /app
USER appuser
# Run as web server: CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8080"]
# Run as worker: CMD ["celery", "-A", "app.worker", "worker", "--loglevel=info"]

requirements.txt:
fastapi==0.109.0, uvicorn==0.27.0, celery==5.3.6, redis==5.0.1,
Pillow==10.2.0, PyMuPDF==1.23.0, pdf2image==1.16.3, python-pptx==0.6.21,
ffmpeg-python==0.2.0, boto3==1.34.0, nats-py==2.6.0, pydantic-settings==2.1.0,
python-magic==0.4.27, extract-msg==0.46.0, weasyprint==61.0

Write tests for each format processor with sample files.
```

# ████████████████████████████████████████████
# PROMPT 8 OF 30: SEARCH SERVICE (OpenSearch)
# ████████████████████████████████████████████

```
Build the Search Service in services/search/. Hybrid lexical + semantic search using OpenSearch 2.12 and Qdrant (Qdrant integration stubbed for now, completed in Phase 11).

Dependencies: github.com/opensearch-project/opensearch-go/v3

On startup: create index template if not exists.

INDEX TEMPLATE (create via OpenSearch API on service startup):
Template name: dms-documents-template
Index pattern: dms-documents-*

Mapping:
{
  "mappings": {
    "dynamic": "strict",
    "properties": {
      "tenant_id": {"type": "keyword"},
      "document_id": {"type": "keyword"},
      "workspace_id": {"type": "keyword"},
      "folder_id": {"type": "keyword"},
      "folder_path": {"type": "keyword"},
      "title": {
        "type": "text", "analyzer": "standard",
        "fields": {
          "keyword": {"type": "keyword", "ignore_above": 256},
          "autocomplete": {"type": "text", "analyzer": "autocomplete_analyzer", "search_analyzer": "standard"}
        }
      },
      "description": {"type": "text"},
      "content": {"type": "text", "analyzer": "standard"},
      "content_snippet": {"type": "text", "index": false},
      "tags": {"type": "keyword"},
      "document_class": {"type": "keyword"},
      "lifecycle_state": {"type": "keyword"},
      "region_pin": {"type": "keyword"},
      "mime_type": {"type": "keyword"},
      "size_bytes": {"type": "long"},
      "created_by": {"type": "keyword"},
      "created_by_name": {"type": "keyword"},
      "created_at": {"type": "date"},
      "updated_at": {"type": "date"},
      "custom_metadata": {"type": "object", "dynamic": true},
      "readable_by": {"type": "keyword"},
      "has_thumbnail": {"type": "boolean"},
      "version_count": {"type": "integer"},
      "extracted_entities": {
        "type": "object",
        "properties": {
          "people": {"type": "keyword"},
          "organizations": {"type": "keyword"},
          "locations": {"type": "keyword"},
          "dates": {"type": "date", "format": "yyyy-MM-dd||yyyy-MM||yyyy"},
          "amounts": {"type": "keyword"}
        }
      }
    }
  },
  "settings": {
    "number_of_shards": 3,
    "number_of_replicas": 1,
    "analysis": {
      "analyzer": {
        "autocomplete_analyzer": {
          "type": "custom",
          "tokenizer": "standard",
          "filter": ["lowercase", "autocomplete_filter"]
        }
      },
      "filter": {
        "autocomplete_filter": {
          "type": "edge_ngram",
          "min_gram": 2,
          "max_gram": 20
        }
      }
    },
    "index.mapping.total_fields.limit": 500
  }
}

ROUTING: every document indexed with routing=tenant_id (ensures all tenant's docs are on same shard for efficient queries).

NATS CONSUMERS:
1. dms.document.created.v1, dms.document.updated.v1 → index/update document
   Fetch full doc metadata from Document Service, fetch readable_by from Policy Service, upsert into OpenSearch.
2. dms.document.deleted.v1 → delete from index
3. dms.version.ocr_completed.v1 → update content field with OCR text
4. dms.permission.changed.v1 → re-index readable_by for affected documents
   If folder permission changed → re-index ALL documents in that folder
   If workspace permission changed → re-index ALL documents in that workspace
5. dms.version.classified.v1 → update document_class field
6. dms.version.entities_detected.v1 → update extracted_entities

REST API:
POST /api/v1/search
  Request body: {
    query: "quarterly revenue report 2025",
    filters: {
      workspace_id: "uuid",
      folder_id: "uuid",
      document_class: ["report", "spreadsheet"],
      lifecycle_state: ["active", "in_review"],
      tags: ["finance"],
      created_after: "2025-01-01T00:00:00Z",
      created_before: "2026-01-01T00:00:00Z",
      mime_type: ["application/pdf"],
      size_min_bytes: 0,
      size_max_bytes: 104857600,
      created_by: "user-uuid",
      custom_metadata: {"department": "Finance"},
      has_content: true
    },
    facets: ["document_class", "tags", "created_by_name", "lifecycle_state", "workspace_id", "mime_type"],
    sort_by: "relevance",
    sort_order: "desc",
    page_size: 20,
    page_token: "base64...",
    search_mode: "hybrid",
    highlight: true,
    explain: false
  }

  BUILD OPENSEARCH QUERY:
  {
    "query": {
      "bool": {
        "must": [
          {
            "multi_match": {
              "query": "quarterly revenue report 2025",
              "fields": ["title^3", "title.keyword^5", "description^1.5", "content^1", "tags^2"],
              "type": "best_fields",
              "fuzziness": "AUTO"
            }
          }
        ],
        "filter": [
          {"term": {"tenant_id": "current-tenant-uuid"}},
          {"terms": {"readable_by": ["user-id", "group-1", "group-2", "everyone"]}},
          {"terms": {"lifecycle_state": ["active", "in_review"]}},
          {"terms": {"document_class": ["report", "spreadsheet"]}},
          {"terms": {"tags": ["finance"]}},
          {"range": {"created_at": {"gte": "2025-01-01", "lte": "2026-01-01"}}},
          {"terms": {"mime_type": ["application/pdf"]}},
          {"term": {"workspace_id": "workspace-uuid"}}
        ]
      }
    },
    "aggs": {
      "document_class": {"terms": {"field": "document_class", "size": 20}},
      "tags": {"terms": {"field": "tags", "size": 50}},
      "created_by_name": {"terms": {"field": "created_by_name", "size": 20}},
      "lifecycle_state": {"terms": {"field": "lifecycle_state", "size": 10}},
      "mime_type": {"terms": {"field": "mime_type", "size": 20}}
    },
    "highlight": {
      "fields": {
        "title": {"number_of_fragments": 1, "fragment_size": 200},
        "content": {"number_of_fragments": 3, "fragment_size": 150}
      },
      "pre_tags": ["<mark>"],
      "post_tags": ["</mark>"]
    },
    "size": 20,
    "from": 0,
    "routing": "tenant-uuid",
    "_source": ["document_id", "title", "description", "document_class", "lifecycle_state", "workspace_id", "folder_id", "tags", "created_by", "created_by_name", "created_at", "updated_at", "size_bytes", "mime_type", "has_thumbnail", "version_count", "content_snippet"]
  }

  CRITICAL: readable_by filter MUST be in the filter clause of EVERY search query AND every aggregation.
  For aggregations, use a global aggregation with a filter that includes readable_by — this prevents facet counts from leaking.

  Actually, since the readable_by filter is in the main query's filter clause, the aggregations will automatically respect it (they run on the filtered result set). But VERIFY this in tests.

GET /api/v1/search/autocomplete?q=quar&limit=10
  Use OpenSearch multi_match on title.autocomplete field with the query text.
  Also query Redis for user's recent searches: recent_searches:{tenant_id}:{user_id} (sorted set by timestamp).
  Combine and deduplicate. Return within 50ms.
  On every full search execution, store the query in recent_searches (max 50 per user).

SAVED SEARCHES:
POST   /api/v1/saved-searches — {name, query, filters, notify: bool, notify_interval_minutes: 15}
GET    /api/v1/saved-searches — list user's saved searches
DELETE /api/v1/saved-searches/{id}
  If notify=true, a background job runs the search every notify_interval_minutes and notifies the user if new documents match.

Response format:
{
  "results": [{
    "document_id": "uuid",
    "title": "Q4 Revenue Report 2025",
    "description": "Quarterly financial summary...",
    "highlights": {
      "title": ["Q4 <mark>Revenue</mark> <mark>Report</mark> 2025"],
      "content": ["...total <mark>revenue</mark> grew 15%...", "...<mark>quarterly</mark> results exceeded..."]
    },
    "document_class": "report",
    "lifecycle_state": "active",
    "workspace_id": "uuid",
    "folder_id": "uuid",
    "tags": ["finance", "q4-2025"],
    "created_by_name": "Jane Smith",
    "created_at": "2025-10-15T10:30:00Z",
    "size_bytes": 2456789,
    "mime_type": "application/pdf",
    "has_thumbnail": true,
    "score": 15.7
  }],
  "facets": {
    "document_class": [{"value": "report", "count": 45}, {"value": "spreadsheet", "count": 12}],
    "tags": [{"value": "finance", "count": 38}, {"value": "q4-2025", "count": 27}]
  },
  "total_count": 157,
  "page_token": "next-page-token",
  "latency_ms": 42,
  "search_mode": "lexical"
}

Write tests: search with various filter combinations, permission filtering (user A can't see user B's docs even through facets), autocomplete, indexing pipeline, empty results, highlight formatting.
```

# ████████████████████████████████████████████
# PROMPTS 9-12: INTELLIGENCE, SEMANTIC SEARCH, RAG, NOTIFICATIONS
# ████████████████████████████████████████████

```
Build the Intelligence Service in services/intelligence/ using Python 3.12, FastAPI, Celery.

This is the AI/ML service handling: OCR, classification, extraction, NER, embedding generation, duplicate detection, RAG Q&A, summarization, and redaction.

Structure:
services/intelligence/
├── app/
│   ├── main.py
│   ├── config.py
│   ├── worker.py              # Celery config
│   ├── nats_consumer.py       # Event consumer
│   ├── llm_gateway.py         # LiteLLM wrapper with per-tenant routing + billing
│   ├── tasks/
│   │   ├── ocr.py             # OCR processing
│   │   ├── classify.py        # Document classification (3-tier)
│   │   ├── extract.py         # Structured field extraction (regex + LLM)
│   │   ├── ner.py             # Named entity recognition
│   │   ├── embed.py           # Embedding generation + Qdrant upsert
│   │   ├── duplicate.py       # Duplicate detection (MinHash + SimHash)
│   │   ├── rag.py             # RAG Q&A pipeline
│   │   ├── summarize.py       # Document summarization
│   │   └── redact.py          # PII redaction
│   ├── models/                # ML model loading
│   │   ├── ocr_model.py       # Surya + PaddleOCR loader
│   │   ├── classifier.py      # DistilBERT classifier
│   │   ├── embedder.py        # sentence-transformers loader
│   │   ├── ner_model.py       # SpaCy transformer NER
│   │   └── reranker.py        # Cross-encoder re-ranker
│   └── api/
│       └── routes.py

NATS Consumer chains (event-driven pipeline):
  version.uploaded.v1 → [ocr.py] if file is OCR-able (PDF, images, TIFF)
  version.ocr_completed.v1 → [classify.py, extract.py, ner.py, embed.py, duplicate.py] (all in parallel)

═══ OCR (ocr.py) ═══
Celery task: process_ocr(tenant_id, document_id, version_id, content_blob_id, region_pin)
1. Download file from S3
2. Check if PDF has embedded text layer: doc=fitz.open(path); has_text = any(page.get_text().strip() for page in doc)
   If has_text → extract text directly with PyMuPDF (no OCR needed), much faster + cheaper
3. If no text (scanned) or image:
   - Import surya: from surya.ocr import run_ocr; from surya.model.detection.model import load_det_model; from surya.model.recognition.model import load_rec_model
   - Load models (cached in module-level variables, loaded once per worker process)
   - Detect language: from surya.languages import detect_languages
   - Run OCR per page with detected language hint
   - For each page: extract text, confidence, bounding_boxes [{x1,y1,x2,y2,text,confidence}]
   - If any page confidence < 0.7: re-run that page with PaddleOCR and keep the higher-confidence result
4. Store in ocr_results table: one row per page
5. Concatenate all pages' text → publish version.ocr_completed.v1 with {version_id, page_count, total_chars, avg_confidence, languages: ["en", "ar"]}
6. Also update documents table: total_size_bytes (content size for search)

Arabic OCR: Surya handles Arabic natively with RTL ordering. Store text with correct Unicode directional markers. Test with real Arabic documents.
Performance: <3s/page CPU, <0.5s/page GPU. Celery concurrency: 2 per GPU worker, 4 per CPU worker.

═══ CLASSIFICATION (classify.py) ═══
Celery task: classify_document(tenant_id, document_id, version_id)
Fetch OCR text from ocr_results.
Three tiers, run in order:
  Tier 1 RULES ($0/doc): keyword matching
    rules = {
      "invoice": ["invoice number", "invoice #", "amount due", "bill to", "invoice date"],
      "contract": ["agreement", "hereby", "parties", "governing law", "whereas"],
      "report": ["executive summary", "findings", "recommendations", "analysis"],
      "policy": ["policy number", "coverage", "premium", "effective date", "insured"],
      "resume": ["experience", "education", "skills", "objective", "references"],
      "receipt": ["receipt", "total", "paid", "transaction", "change due"],
      "letter": ["dear", "sincerely", "regards", "to whom it may concern"],
      "memo": ["memorandum", "memo", "from:", "to:", "subject:", "date:"],
      "form": ["please fill", "applicant", "date of birth", "signature"],
      "certificate": ["certify", "awarded", "certificate of", "completion"]
    }
    Score each class by count of matching keywords in text (case-insensitive).
    If top score > 3 AND top score is 2x the second-highest → use this class, confidence = min(score/10, 0.99)

  Tier 2 ML ($0.001/doc): if Tier 1 confidence < 0.85
    from transformers import pipeline
    classifier = pipeline("text-classification", model="distilbert-base-uncased", device=0 if torch.cuda.is_available() else -1)
    # Fine-tuned model loaded from local path (trained on labeled docs)
    result = classifier(text[:512])
    Use if confidence > 0.8

  Tier 3 LLM ($0.02/doc): if Tier 2 confidence < 0.8
    Call LLM via llm_gateway:
    prompt = f"""Classify this document into exactly ONE category from this list:
    [Invoice, Contract, Report, Policy, Resume, Receipt, Letter, Memo, Form, Certificate, Correspondence, Specification, Manual, Other]
    
    Document text (first 2000 characters):
    {text[:2000]}
    
    Respond with ONLY a JSON object: {{"class": "...", "confidence": 0.0-1.0, "reasoning": "one sentence"}}"""

Store in documents table: document_class, classification_confidence. Also in extraction_results with method used.
Publish: version.classified.v1

═══ EXTRACTION (extract.py) ═══
Celery task: extract_fields(tenant_id, document_id, version_id, document_class)

Based on document_class, apply extraction rules:

INVOICE:
  Fields: invoice_number, vendor_name, vendor_address, invoice_date, due_date, po_number, subtotal, tax_amount, total_amount, currency, line_items[{description, quantity, unit_price, amount}]
  
  Regex patterns (try first):
    invoice_number: r'(?:Invoice|Inv)[\s#.:]*([A-Z0-9][\w-]{2,20})'
    total_amount: r'(?:Total|Amount Due|Grand Total|Balance Due)[\s:]*\$?([\d,]+\.?\d{0,2})'
    date: r'(?:Date|Invoice Date|Issued)[\s:]*(\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|\w+ \d{1,2},? \d{4})'
    po_number: r'(?:PO|Purchase Order|P\.O\.)[\s#.:]*([A-Z0-9][\w-]{2,20})'
  
  Validate: dates parse correctly, amounts are positive numbers, invoice_number is non-empty.
  If any required field missing or validation fails (confidence < 0.8): fall back to LLM.
  
  LLM prompt:
  "Extract these fields from the invoice text. Return ONLY valid JSON:
   {invoice_number, vendor_name, invoice_date (YYYY-MM-DD), due_date (YYYY-MM-DD), total_amount (number), currency (ISO 4217 code), tax_amount (number), line_items: [{description, quantity, unit_price, amount}]}
   
   Invoice text: {text}"

CONTRACT:
  Always use LLM (contracts are too variable for regex):
  Fields: parties[], effective_date, expiration_date, governing_law, contract_value, payment_terms, auto_renewal, notice_period_days, termination_clause_summary, key_obligations[]
  
GENERIC (for other types): extract basic entities only (handled by NER task)

Store results in extraction_results table with: method (regex/llm/hybrid), confidence, cost_cents, processing_time_ms.
Publish: version.extraction_completed.v1

═══ NER (ner.py) ═══
Celery task: detect_entities(tenant_id, document_id, version_id)
Load SpaCy: nlp = spacy.load("en_core_web_trf") — cached per worker process
Also apply regex for structured PII:
  SSN: r'\b\d{3}-\d{2}-\d{4}\b'
  Credit card: r'\b(?:\d{4}[\s-]?){3}\d{4}\b' — validate with Luhn algorithm
  Email: standard email regex
  Phone: r'\b(?:\+\d{1,3}[\s-]?)?\(?\d{3}\)?[\s.-]?\d{3}[\s.-]?\d{4}\b'
  Date of birth: look for "DOB", "Date of Birth", "Born" near a date pattern

For each entity found: store in entities table with {entity_type, entity_value, start_offset, end_offset, page_number, confidence, is_pii: true/false}
If PII entities found → flag document for DLP review. Publish version.entities_detected.v1 with {pii_count, entity_types_found[]}.

═══ EMBEDDINGS (embed.py) ═══
Celery task: generate_embeddings(tenant_id, document_id, version_id)
1. Fetch OCR text from ocr_results
2. Chunk: split into ~512 token chunks with 64-token overlap.
   - Use tiktoken: enc = tiktoken.get_encoding("cl100k_base"); tokens = enc.encode(text)
   - Split at sentence boundaries (re.split on [.!?] followed by space)
   - Each chunk: {chunk_index, text, token_count, page_numbers}
3. Embed: from sentence_transformers import SentenceTransformer
   model = SentenceTransformer("sentence-transformers/all-MiniLM-L6-v2")  # 384 dims
   embeddings = model.encode(chunk_texts, batch_size=32, show_progress_bar=False)
4. Store chunks in document_chunks table
5. Upsert to Qdrant:
   from qdrant_client import QdrantClient
   client.upsert(collection_name=f"dms_vectors", points=[
     PointStruct(id=chunk_uuid, vector=embedding.tolist(), payload={
       "tenant_id": str(tenant_id),
       "document_id": str(document_id),
       "version_id": str(version_id),
       "chunk_index": i,
       "page_numbers": chunk.page_numbers,
       "readable_by": readable_by_group_ids  # CRITICAL for permission-filtered search
     })
   ])
6. Publish: version.embedded.v1

═══ RAG Q&A (rag.py) ═══
POST /api/v1/intelligence/ask
Request: {question, scope: "workspace"|"folder"|"document"|"tenant", scope_id, conversation_id?, model_preference?}

Pipeline:
1. Permission check: user must have access to scope
2. Embed question: model.encode([question])
3. Retrieve from Qdrant (top 50):
   client.search(collection_name="dms_vectors", query_vector=q_embedding, limit=50,
     query_filter=Filter(must=[
       FieldCondition(key="tenant_id", match=MatchValue(value=str(tenant_id))),
       FieldCondition(key="readable_by", match=MatchAny(any=user_group_ids)),
       # Add scope filter based on scope type
     ]))
4. Retrieve from OpenSearch (BM25, top 50): same query with permission filter
5. RRF fusion: for each doc in either list, score = sum(1/(60 + rank_in_list))
6. Re-rank top 20 with cross-encoder:
   from sentence_transformers import CrossEncoder
   reranker = CrossEncoder("cross-encoder/ms-marco-MiniLM-L-6-v2")
   scores = reranker.predict([(question, chunk.text) for chunk in top_20])
   Take top 5
7. Build context: concatenate top 5 chunks with source info, max 4000 tokens
8. If conversation_id: load last 3 turns from conversation_history
9. LLM call via llm_gateway:
   system = "You are a document assistant for SeDoc. Answer based ONLY on the provided documents. If the answer isn't in the documents, say 'I couldn't find this in your documents.' Always cite sources as [Document: title, Page: X]."
   user = f"Documents:\n{formatted_chunks}\n\nQuestion: {question}"
10. Parse response: extract [Document: ...] citations, map to document IDs
11. Store turn in conversation_history table
12. Meter: publish billing.llm_usage.v1 with {tenant_id, model, input_tokens, output_tokens, cost}

POST /api/v1/intelligence/summarize
  {document_id, length: "short"|"medium"|"detailed"}
  If text < 4000 tokens: single LLM call
  If text > 4000 tokens: map-reduce (split into chunks, summarize each, meta-summarize)

POST /api/v1/intelligence/redact
  {document_id, version_id, entities_to_redact: ["SSN", "CREDIT_CARD"], auto_detect: true}
  If auto_detect: run NER first. Return candidates for human review.

POST /api/v1/intelligence/redact/{redaction_id}/apply
  {confirmed_entities: ["entity-id-1", ...]}
  Download PDF, use PyMuPDF: for each entity, get bounding box, draw black rect, remove text.
  Upload as new version with "(Redacted)" change_summary.
  Restrict access to original version.

═══ LLM GATEWAY (llm_gateway.py) ═══
Per-tenant routing via LiteLLM:
  Load tenant config from connector_configs: {llm_provider, model, api_key_encrypted, base_url}
  Decrypt API key using crypto module.
  Call: litellm.completion(model=tenant_config.model, messages=[...], api_key=decrypted_key, api_base=tenant_config.base_url)
  Track usage: {tenant_id, model, input_tokens, output_tokens, cost_usd}
  Rate limit: max 10 concurrent LLM calls per tenant
  Error handling: timeout (30s) → retry once, rate limit (429) → exponential backoff, model unavailable → fallback to default model

═══ NOTIFICATIONS SERVICE (separate Go service: services/notification/) ═══
NATS consumer: dms.notify.*
For each event type, resolve recipients and their preferences:
  document.shared → shared-with user
  workflow.step_assigned → assignee
  workflow.completed → initiator
  comment.created → document owner + @mentioned users
  comment.mentioned → @mentioned user
  signature.requested → signer
  upload.quarantined → uploader
  alert.saved_search → search owner

Delivery channels:
  IN_APP: insert to notifications table + publish to Redis pub/sub: notif:{tenant_id}:{user_id}
  EMAIL: render HTML template (Go html/template), send via SES/SendGrid. Respect quiet hours. Batch if >5 in 15min.
  PUSH: FCM for Android, APNs for iOS. Lookup device_tokens table.
  SLACK: POST to webhook URL from connector_configs. Rich message format.
  TEAMS: POST to webhook URL. Adaptive card format.
  SMS: Twilio API. Critical alerts only. Max 10/day/user.

REST API: GET /api/v1/notifications (paginated, filter by read/unread), PATCH /{id}/read, POST /read-all, GET /unread-count, GET/PUT /preferences

Write comprehensive tests for all intelligence tasks, RAG pipeline, notification delivery.
```

# ████████████████████████████████████████████
# PROMPTS 13-18: COMPLETE FRONTEND
# ████████████████████████████████████████████

```
Build the complete SeDoc web application in web/ using React 18, TypeScript 5.3, Vite 5, TanStack Router, TanStack Query v5, Tailwind CSS 3.4, Zustand, and Radix UI primitives.

SETUP:
npm create vite@latest web -- --template react-ts
cd web && npm install @tanstack/react-query @tanstack/react-router @tanstack/router-vite-plugin \
  tailwindcss @tailwindcss/vite @radix-ui/react-dialog @radix-ui/react-dropdown-menu \
  @radix-ui/react-select @radix-ui/react-tabs @radix-ui/react-tooltip @radix-ui/react-popover \
  @radix-ui/react-checkbox @radix-ui/react-switch @radix-ui/react-avatar \
  @radix-ui/react-separator @radix-ui/react-scroll-area @radix-ui/react-context-menu \
  zustand react-hot-toast lucide-react dayjs react-dropzone @dnd-kit/core @dnd-kit/sortable \
  zod react-intl axios react-pdf pdfjs-dist @tanstack/react-table clsx tailwind-merge \
  @reactflow/core @reactflow/minimap @reactflow/controls cmdk recharts

COMPLETE FILE STRUCTURE:
web/src/
├── main.tsx                   # React root + providers (QueryClient, Router, IntlProvider, ToastProvider)
├── routeTree.gen.ts           # Auto-generated by TanStack Router
├── routes/
│   ├── __root.tsx             # Root layout: error boundary, toast container
│   ├── login.tsx              # Login page: email/password form, SSO buttons, MFA step
│   ├── register.tsx           # Registration page
│   ├── forgot-password.tsx
│   ├── shared.$token.tsx      # Public share link viewer (no auth required)
│   ├── _authenticated.tsx     # Auth guard layout: checks session, redirects to /login if not authenticated
│   └── _authenticated/
│       ├── index.tsx          # Dashboard: welcome, recent docs, storage usage chart, quick actions
│       ├── workspaces/
│       │   ├── index.tsx      # Workspace grid: cards with name, doc count, member count, last activity
│       │   └── $workspaceId/
│       │       ├── index.tsx  # Workspace view: folder tree sidebar + document list main area
│       │       └── documents/
│       │           └── $documentId.tsx  # Document detail: full-page viewer + metadata panel
│       ├── search.tsx         # Full search page: search bar + filters sidebar + results + facets
│       ├── tasks.tsx          # My Tasks: pending approvals, assignments across all workflows
│       ├── notifications.tsx  # Notification center: grouped by date, read/unread filter
│       ├── trash.tsx          # Deleted items (admin only): restore or permanently delete
│       └── admin/
│           ├── index.tsx      # Admin dashboard: user count, storage, usage metrics
│           ├── users.tsx      # User management table with invite/edit/suspend/MFA reset
│           ├── groups.tsx     # Group management with member assignment
│           ├── sso.tsx        # SSO configuration (SAML/OIDC setup wizard)
│           ├── workflows.tsx  # Workflow definition list + designer
│           ├── retention.tsx  # Retention policy CRUD
│           ├── legal-holds.tsx # Active legal holds management
│           ├── audit-log.tsx  # Audit log viewer with filters + export
│           ├── compliance.tsx # Compliance dashboard: residency, encryption, retention stats
│           ├── api-keys.tsx   # API key management
│           ├── webhooks.tsx   # Webhook subscription management
│           ├── connectors.tsx # Third-party connector configuration
│           ├── billing.tsx    # Usage + billing (SaaS only)
│           └── settings.tsx   # Tenant settings: branding, metadata schema, feature flags
│
├── components/
│   ├── ui/                    # Design system primitives (Radix + Tailwind)
│   │   ├── Button.tsx         # Variants: primary, secondary, outline, ghost, destructive. Sizes: sm, md, lg. Loading state.
│   │   ├── Input.tsx          # Text input with label, error message, icon prefix/suffix
│   │   ├── Textarea.tsx
│   │   ├── Select.tsx         # Radix Select with search filter for long lists
│   │   ├── Dialog.tsx         # Radix Dialog with standard sizes (sm, md, lg, xl, full)
│   │   ├── DropdownMenu.tsx   # Radix DropdownMenu with icon + shortcut key display
│   │   ├── ContextMenu.tsx    # Right-click context menu
│   │   ├── Tabs.tsx
│   │   ├── Table.tsx          # TanStack Table wrapper with sorting, column resize, row selection
│   │   ├── DataTable.tsx      # Full-featured data table: sort, filter, select, paginate
│   │   ├── Badge.tsx          # Status badges with semantic colors
│   │   ├── Avatar.tsx         # User avatar with fallback initials
│   │   ├── Tooltip.tsx
│   │   ├── Toast.tsx          # react-hot-toast styled to match design system
│   │   ├── Skeleton.tsx       # Loading skeleton with shimmer animation
│   │   ├── Spinner.tsx        # Animated spinner
│   │   ├── EmptyState.tsx     # Icon + title + description + action button
│   │   ├── ErrorState.tsx     # Error display with retry button
│   │   ├── ConfirmDialog.tsx  # "Are you sure?" dialog with configurable text + destructive styling
│   │   ├── CommandPalette.tsx # cmdk-powered command palette (Cmd+K): search docs, navigate, actions
│   │   ├── FileIcon.tsx       # File type icon based on mime_type (PDF=red, DOCX=blue, image=green, etc.)
│   │   ├── SearchInput.tsx    # Search input with debounce, clear button, keyboard shortcut hint
│   │   ├── Pagination.tsx     # Page size selector + prev/next for cursor pagination
│   │   └── Sheet.tsx          # Slide-in panel (right side) for detail views
│   │
│   ├── layout/
│   │   ├── AppLayout.tsx      # Main layout: collapsible sidebar (280px) + header (56px) + main content
│   │   ├── Sidebar.tsx        # Logo, workspace selector, folder tree, quick links (Tasks, Trash, Admin)
│   │   ├── Header.tsx         # Global search bar (center), notification bell with unread count, user avatar dropdown (profile, settings, theme toggle, logout)
│   │   ├── FolderTree.tsx     # Recursive tree component: expand/collapse, lazy load children, drag-drop reorder, context menu (rename, move, delete, new subfolder), active folder highlight
│   │   ├── Breadcrumb.tsx     # Clickable breadcrumb: Workspace > Folder > Subfolder > Document
│   │   └── WorkspaceSelector.tsx # Dropdown to switch workspace in sidebar
│   │
│   ├── documents/
│   │   ├── DocumentGrid.tsx   # Grid of DocumentCard components, configurable columns (3/4/5)
│   │   ├── DocumentTable.tsx  # Table view with sortable columns: name, type, size, modified, owner, state
│   │   ├── DocumentCard.tsx   # Card: thumbnail, title (truncated), badge (state), size, modified date, avatar of owner. Hover: quick actions (download, share, more). Click: navigate to detail.
│   │   ├── DocumentList.tsx   # Container: view mode toggle (grid/table), sort controls, bulk action bar (appears when items selected: move, delete, tag, download ZIP)
│   │   ├── DocumentUpload.tsx # Full-page drop zone (react-dropzone): drag-drop overlay, file picker button, upload queue with progress bars (per file: filename, size, progress %, cancel button)
│   │   ├── UploadProgress.tsx # Floating upload manager (bottom-right): shows active uploads, completed, failed. Collapsible.
│   │   ├── MetadataPanel.tsx  # Right panel (Sheet): document info (title editable inline, description, metadata fields from schema, tags, created/modified dates, size, owner, version count), tab sections: Info | Versions | Activity | Comments | Permissions
│   │   ├── VersionHistory.tsx # Version list: version number, date, author, change summary, size. Actions: preview, download, restore, compare.
│   │   ├── ShareDialog.tsx    # Dialog: create share link (settings: password, expiry, max views, permissions), copy link button, list existing links with revoke button.
│   │   ├── MoveDialog.tsx     # Dialog: folder tree picker for move destination, with create-new-folder inline
│   │   ├── TagEditor.tsx      # Inline tag editor: existing tags as chips with remove, autocomplete input for adding
│   │   └── BulkActionBar.tsx  # Floating bar when items selected: "X items selected" + action buttons
│   │
│   ├── viewer/
│   │   ├── DocumentViewer.tsx # Full-page modal viewer: toolbar (zoom, fit, rotate, download, print, share, annotate, fullscreen) + main view area + metadata sidebar (collapsible)
│   │   ├── PDFViewer.tsx      # react-pdf/pdfjs wrapper: page navigation (input + prev/next), zoom (fit-width, fit-page, 50%-200%), text selection, search within PDF (Ctrl+F), page thumbnails sidebar
│   │   ├── ImageViewer.tsx    # Image with zoom (scroll wheel), pan (drag), fit to screen, rotate
│   │   ├── TextViewer.tsx     # Code viewer with syntax highlighting (Prism or Shiki), line numbers
│   │   ├── VideoPlayer.tsx    # HTML5 video player with controls, or hls.js for HLS streams
│   │   ├── AnnotationLayer.tsx # Overlay on PDF/image: highlight (yellow), text note (sticky), stamp, freehand drawing. Toolbar to select tool. Click to place. Stored as JSON overlay.
│   │   ├── CompareView.tsx    # Side-by-side version comparison: two PDF viewers synced scroll, diff highlights
│   │   └── UnsupportedFormat.tsx # Fallback: file icon + name + download button
│   │
│   ├── search/
│   │   ├── SearchPage.tsx     # Layout: search bar top + filter sidebar left + results center + facets right
│   │   ├── SearchBar.tsx      # Large search input with: search mode toggle (All/Semantic/Exact), keyboard shortcut (Cmd+K)
│   │   ├── SearchResults.tsx  # Result cards: title with highlights, snippet with highlights, metadata chips (type, date, workspace), thumbnail
│   │   ├── FacetPanel.tsx     # Collapsible facet groups: Document Type (checkboxes with counts), Tags (checkboxes), Date Range (date picker), Author (checkbox list), Workspace (checkbox list), File Type (checkboxes). "Clear all" button.
│   │   ├── SavedSearches.tsx  # Saved search list with: name, run button, notification toggle, delete
│   │   └── SearchSuggestions.tsx # Autocomplete dropdown: recent searches, suggested documents, suggested queries
│   │
│   ├── workflow/
│   │   ├── WorkflowDesigner.tsx # ReactFlow canvas: drag node types from palette (approval, review, notification, condition, signature), connect with edges, properties panel for selected node. Save/load as JSON.
│   │   ├── WorkflowNodePalette.tsx # Draggable node type list
│   │   ├── WorkflowNode.tsx   # Custom ReactFlow node: icon, name, assignee, status indicator
│   │   ├── WorkflowPropertiesPanel.tsx # When node selected: edit assignee, timeout, condition, escalation
│   │   ├── TaskList.tsx       # "My Tasks" view: pending tasks grouped by workflow, with: doc title, task name, assigned date, due date, action buttons (approve/reject/delegate)
│   │   ├── ApprovalCard.tsx   # Inline approval card: document preview thumbnail, metadata, approve/reject/delegate buttons, comment input
│   │   └── WorkflowTimeline.tsx # Visual timeline of workflow instance: completed steps (green), current (blue), pending (gray)
│   │
│   ├── ai/
│   │   ├── AIChatPanel.tsx    # Slide-in chat panel: message thread, input box, "Ask about this document/workspace". Shows citations as clickable links.
│   │   ├── SummaryButton.tsx  # "Summarize" button on document card → shows summary in popover
│   │   ├── ClassificationBadge.tsx # Shows auto-detected document class with confidence indicator
│   │   └── RedactionTool.tsx  # Redaction workflow: detected PII list with checkboxes, preview of redactions, apply button
│   │
│   ├── admin/
│   │   ├── UserTable.tsx      # DataTable: email, name, role, status, MFA, last login, actions (edit, suspend, reset MFA)
│   │   ├── InviteUserDialog.tsx # Dialog: email, role selector, workspace assignment
│   │   ├── GroupEditor.tsx    # Group list + member management (add/remove users via autocomplete)
│   │   ├── PermissionEditor.tsx # For a resource: show who has access (users + groups), add/edit/remove, capability selector
│   │   ├── AuditLogTable.tsx  # Filterable log: date range, actor, action, resource. Export CSV/JSON button.
│   │   ├── RetentionPolicyForm.tsx # Form: name, target (class/tags/workspace), retain days, action, archive days
│   │   ├── LegalHoldManager.tsx # Active holds list, create new (select docs or search), release with confirmation
│   │   ├── ComplianceDashboard.tsx # Charts (recharts): docs by lifecycle state, storage by region, retention timeline, encryption coverage
│   │   ├── MetadataSchemaEditor.tsx # JSON Schema editor: add/remove fields, set types (text/number/date/enum/boolean), set required
│   │   ├── SSOConfigWizard.tsx # Step-by-step: choose SAML/OIDC, enter IdP details, test connection, configure attribute mapping
│   │   └── WebhookManager.tsx # CRUD webhooks: URL, event selection, secret display, delivery log
│   │
│   └── shared/
│       ├── ErrorBoundary.tsx  # Catch render errors, show friendly error + retry
│       ├── LoadingScreen.tsx  # Full-page centered spinner with SeDoc logo
│       ├── ProtectedRoute.tsx # Route guard: check auth, check permission, redirect if denied
│       └── PageHeader.tsx     # Consistent page header: title, description, action buttons
│
├── hooks/
│   ├── useAuth.ts             # Auth state + actions: login, logout, register, getCurrentUser, isAuthenticated, hasPermission
│   ├── useDocuments.ts        # TanStack Query hooks: useDocuments(filter), useDocument(id), useCreateDocument(), useUpdateDocument(), useDeleteDocument()
│   ├── useSearch.ts           # useSearch(query, filters), useAutocomplete(query), useSavedSearches()
│   ├── useUpload.ts           # Upload manager: useUpload() returns {uploadFiles, uploads (state), cancelUpload}. Handles presigned URL flow, multipart, progress tracking.
│   ├── useFolders.ts          # useFolders(workspaceId, parentId), useCreateFolder(), useMoveFolder()
│   ├── usePermissions.ts      # usePermissions(resourceType, resourceId), useCheckPermission(action, resourceType, resourceId)
│   ├── useWebSocket.ts        # WebSocket connection to collaboration service. Auto-reconnect with exponential backoff. Subscribe/unsubscribe to document channels.
│   ├── useNotifications.ts    # Real-time notifications via WebSocket + polling fallback. useNotifications(), useUnreadCount(), useMarkAsRead()
│   ├── useWorkflows.ts        # Workflow hooks: useMyTasks(), useStartWorkflow(), useCompleteStep()
│   ├── useWorkspaces.ts       # useWorkspaces(), useCreateWorkspace(), useWorkspaceMembers()
│   ├── useAI.ts               # useAskQuestion(question, scope), useSummarize(docId), useRedact(docId)
│   └── useTheme.ts            # Dark/light mode toggle, persisted in localStorage
│
├── api/
│   ├── client.ts              # Axios instance with baseURL, interceptors:
│   │                          # Request: add Authorization header from auth store
│   │                          # Response: 401 → clear auth, redirect to /login. 403 → toast "Access denied". 429 → toast "Rate limited, try again". 5xx → toast "Server error" with retry.
│   ├── documents.ts           # All document API calls
│   ├── auth.ts                # All auth API calls
│   ├── search.ts
│   ├── upload.ts              # Presigned URL upload flow
│   ├── folders.ts
│   ├── workspaces.ts
│   ├── permissions.ts
│   ├── workflows.ts
│   ├── notifications.ts
│   ├── intelligence.ts        # AI endpoints (ask, summarize, redact)
│   ├── admin.ts               # Admin endpoints (users, groups, settings)
│   └── signatures.ts
│
├── store/
│   ├── authStore.ts           # Zustand: user, sessionToken, tenant, login(), logout(), isAuthenticated
│   ├── uiStore.ts             # Zustand: sidebarCollapsed, viewMode (grid/table), theme (light/dark), activeWorkspaceId, activeFolderId
│   └── uploadStore.ts         # Zustand: activeUploads Map<id, {file, progress, status, error}>
│
├── lib/
│   ├── permissions.ts         # Client-side permission checks (cache from server response)
│   ├── formatters.ts          # formatFileSize(bytes), formatDate(date, locale), formatRelativeTime(date), getMimeTypeLabel(mime)
│   ├── constants.ts           # Lifecycle states, document classes, permission capabilities, event types
│   ├── cn.ts                  # clsx + tailwind-merge utility
│   └── validators.ts          # Zod schemas for all forms
│
├── styles/
│   └── globals.css            # @tailwind directives + CSS custom properties:
│                              # --color-bg, --color-bg-secondary, --color-text, --color-text-secondary,
│                              # --color-primary (#1E40AF), --color-primary-hover, --color-border,
│                              # --color-accent, --color-danger, --color-success, --color-warning
│                              # Dark theme overrides via .dark class
│
├── locales/
│   ├── en.json                # English strings (500+ keys)
│   └── ar.json                # Arabic strings (RTL support)
│
└── types/
    └── api.ts                 # TypeScript types matching all API response shapes

DESIGN REQUIREMENTS:
- Color: slate-50/900 backgrounds, blue-700 (#1E40AF) primary, amber-500 warnings, red-500 errors, emerald-500 success
- Typography: "Inter" for UI, "JetBrains Mono" for code/metadata. Load from Google Fonts.
- Spacing: 4px base unit. Consistent padding: 16px (cards), 24px (pages), 8px (tight)
- Borders: 1px border-slate-200 (light) / border-slate-700 (dark). Rounded: 6px (buttons), 8px (cards), 12px (modals)
- Shadows: sm for dropdowns, md for cards, lg for modals
- Animations: 150ms transitions on hover/focus. Skeleton loading for all data fetches. Toast slide-in from top-right.
- Dark mode: toggle in header. Preference saved in localStorage. CSS variables swap.
- RTL: Tailwind rtl: prefix throughout. CSS logical properties (margin-inline-start not margin-left).
- Responsive: min-width 1024px (desktop). Sidebar collapses to icons at 1024-1280px.
- Keyboard: Cmd+K command palette, Cmd+/ toggle sidebar, Esc close modals, Tab navigation everywhere, Enter to submit forms.
- Accessibility: all interactive elements focusable, aria-labels on icons-only buttons, color contrast 4.5:1 minimum, screen reader announcements for toasts.

Build ALL components and pages. Every component should be complete and functional. Use TypeScript strict mode. All API calls use TanStack Query with proper cache invalidation.

The design should feel like Linear meets Notion — clean, fast, professional. NOT generic Material UI or Bootstrap.
```

# ████████████████████████████████████████████
# PROMPTS 19-24: WORKFLOW, COLLAB, AUDIT, SIGNATURES, COMPLIANCE, MOBILE
# ████████████████████████████████████████████

```
Build remaining backend services for SeDoc. Each service follows the standard pattern established in previous prompts.

═══ WORKFLOW SERVICE (services/workflow/, Go + Temporal SDK) ═══

Add temporal Go SDK dependency. Connect to Temporal server on startup.

Temporal Workflows:
1. ApprovalWorkflow(ctx, ApprovalInput{DocumentID, Steps[], InitiatedBy}):
   For each step: create task → notify assignee → wait for signal("step_completed") OR timer(timeout_hours).
   Signals: approve{outcome, notes}, reject{reason}, delegate{to_user_id}, escalate.
   On reject: set document lifecycle to 'draft', return rejected.
   On timeout: send escalation notification to step.escalate_to, extend timer.
   On all approved: set document lifecycle to 'active', return approved.

2. ParallelApprovalWorkflow: multiple approvers via workflow.Group. Modes: require_all, require_any.

3. ConditionalWorkflow: evaluate conditions against document metadata (e.g., custom_metadata.contract_value > 100000) using expr-lang/expr evaluator. Branch to different step sequences.

REST API: CRUD definitions (JSON schema), POST start instance, POST complete step (sends Temporal signal), GET instance status, GET /tasks?assignee=me&status=pending (across all instances), POST cancel instance.

═══ COLLABORATION SERVICE (services/collaboration/, Node.js 22 + ws) ═══

WebSocket server on :8083/ws. Auth: first message {type:"auth", token} → validate via Auth Service gRPC → reject with code 4001 if invalid.

Connection map: Map<tenantId, Map<userId, WebSocket[]>>. Track online users in Redis SET: online:{tenantId}.

Channels: subscribe/unsubscribe to "document:{docId}". Messages:
  Client→Server: auth, subscribe, unsubscribe, presence{doc_id, page, cursor}, comment_create{doc_id, body, parent_id}
  Server→Client: notification, presence_update{doc_id, users[]}, comment_added, document_updated, permission_changed

Redis pub/sub for cross-instance: channel dms:{tenantId}:doc:{docId}. When comment created on instance A, publish to Redis → instance B picks up and forwards to subscribed clients.

Heartbeat: 30s ping, 10s pong timeout → close connection. On disconnect: if last connection for user, remove from online set.

═══ AUDIT SERVICE (services/audit/, Go) ═══

NATS consumer: ALL domain events (dms.document.*, dms.auth.*, dms.permission.*, dms.workflow.*, dms.signature.*).
Derive audit entries from each event. Hash chain: event_hash = SHA-256(previous_hash + tenant_id + actor + action + resource + timestamp). Per-tenant mutex via Redis SETNX for ordering.

REST: GET /api/v1/audit/events (paginated, filterable by actor, action, resource, date range). GET /export (CSV/JSON stream). POST /verify-integrity (walk chain, verify hashes). GET /compliance/report (SOC 2 evidence). POST /data-subject/export (GDPR Art.15). POST /data-subject/anonymize (GDPR Art.17).

═══ SIGNATURE SERVICE (services/signature/, Go) ═══

POST /api/v1/signatures/requests — create request with signers[{email, name, role, order}].
Internal signing: generate signing URL per signer → signing page renders doc + capture pad → embed signature in PDF using unidoc/unipdf (Go PDF library) or call Python subprocess with pymupdf → PAdES signature with PKCS#7 + TSA timestamp.
External: DocuSign API (create envelope, register webhook for status), Adobe Sign API.
On completion: create new version with signed PDF. Publish signature.completed.v1.
GET /verify/{document_id} — extract and verify PDF signatures.

═══ COMPLIANCE ENGINE (extend Document Service) ═══

Daily cron: retention policy enforcement. Query policies → find matching docs past retain_days → NOT under legal hold → transition lifecycle.
Legal holds: POST create (apply to doc IDs or search results), DELETE release.
Data residency dashboard: per-region counts + violations.
Data subject requests: export/erase with legal hold precedence.

═══ MOBILE APP (mobile/, React Native + Expo SDK 51) ═══

Expo Router: (auth)/login, (tabs)/{home, search, upload, notifications, profile}, workspace/[id], folder/[id], document/[id].
Core features: auth (password + SSO redirect), document browser, PDF viewer (react-native-pdf), search, camera capture (expo-camera + perspective correction), file upload from gallery, push notifications (expo-notifications + device token registration), offline mode (expo-sqlite metadata cache + pinned doc storage).
Design: match web app colors and typography. Bottom tab navigation. Pull-to-refresh on lists.

Build ALL services completely. Each with full error handling, logging, tests, and Dockerfile.
```

# ████████████████████████████████████████████
# PROMPTS 25-30: ON-PREM, SAAS, SECURITY, OBS, INTEGRATIONS, TESTING
# ████████████████████████████████████████████

```
Build the remaining infrastructure and platform services for SeDoc.

═══ PROMPT 25: HELM CHART (deploy/helm/) ═══

Chart.yaml: name=vaultdms, version=1.0.0
values.yaml: global.imageRegistry, global.storageClass, global.tenantIsolation (shared/schema/database).

Per service: replicas, resources (requests+limits), env vars, HPA config.
Subcharts: bitnami/postgresql, bitnami/redis, opensearch/opensearch, minio/minio, nats/nats, qdrant/qdrant, temporalio/temporal.

Templates per service: Deployment (liveness: HTTP GET /healthz, readiness: HTTP GET /readyz, startup probe with 60s failureThreshold, PodDisruptionBudget minAvailable=1, anti-affinity preferredDuringScheduling), Service ClusterIP, HPA (targetCPU 70%), ConfigMap, external Secret reference, ServiceMonitor (Prometheus), NetworkPolicy (default deny ingress, allow only necessary).

Ingress: nginx-ingress with TLS termination, path-based routing to services.

values-onprem.yaml: MinIO instead of S3, local SMTP, telemetry disabled, local LLM endpoint.
values-airgapped.yaml: all images from local registry, offline license JWT validation, OCR models bundled, LLM via local vLLM.

Hooks: pre-install Job (DB migration), post-install Job (create default admin, create default buckets).
CronJob: daily backup (pg_dump to MinIO).
NOTES.txt: post-install instructions with URLs.

═══ PROMPT 26: SAAS CONTROL PLANE (services/controlplane/) ═══

Internal-only service (admin API key auth):
POST /internal/v1/tenants/provision — {org_name, admin_email, plan, region}. Steps: create org → create schema (if schema-per-tenant) → create OpenSearch index → create Qdrant collection → create MinIO buckets → create Stripe customer+subscription → create admin user → send welcome email. Return {tenant_id, admin_user_id, login_url}.

Stripe integration (stripe-go): POST /internal/v1/stripe/webhook — handle invoice.paid, payment_failed, subscription.updated, subscription.deleted. On payment failure: 7-day grace → suspend tenant (all APIs return 403).

Usage metering (hourly cron): per tenant: storage_gb (sum content_blobs.size_bytes), ocr_pages (count ocr_results in period), api_calls (from gateway metrics), active_users (distinct session users), ai_tokens (sum conversation_history tokens). Push to Stripe as usage records.

Feature flags: per-tenant in org settings JSONB. Flags: ai_enabled, advanced_workflow, sso_enabled, e_signatures, custom_branding, api_access, data_rooms. Defaults by plan. Override per tenant.

Tenant routing (Redis): tenant_route:{id} → {db_host, db_schema, search_cluster, vector_collection, storage_region}. Read by all services on every request.

═══ PROMPT 27: SECURITY HARDENING ═══

Apply across ALL services:
1. API Gateway: rate limiting per tenant+IP+endpoint (Redis token bucket), WAF patterns (SQL injection, XSS, path traversal), 10MB body limit, security headers (HSTS max-age=63072000, CSP default-src 'self', X-Frame-Options DENY, X-Content-Type-Options nosniff, Referrer-Policy strict-origin-when-cross-origin), CORS per tenant.
2. Input validation: go-playground/validator on all request structs, string trim+max length, UUID format validation, JSON depth limit 10, null byte rejection.
3. DLP pipeline: on document share/download, scan for credit card/SSN/custom keywords. Actions: warn, block, quarantine.
4. Secrets rotation CLI: dms-admin rotate-secrets — rotates DB passwords, JWT signing keys, API keys, KEKs. 24h dual-validity window.
5. CI/CD: Semgrep SAST, Trivy container scan, gosec, govulncheck, npm audit. Block on HIGH/CRITICAL.
6. Dependency pinning: go.sum, package-lock.json, pip freeze requirements.txt. Dependabot enabled.

═══ PROMPT 28: OBSERVABILITY ═══

All Go services:
  Structured logging: zerolog, every line has timestamp+level+service+tenant_id+correlation_id. PII scrubbing registered for email/IP patterns. DEBUG sampled 1% in prod.

  Prometheus metrics (promhttp): http_requests_total{method,path,status,tenant_id}, http_request_duration_seconds{method,path} (histogram buckets 10/50/100/250/500/1000/5000ms), grpc_requests_total, grpc_duration, db_query_duration{query}, db_connections{pool,state}, search_latency{mode}, ocr_pages_total{engine}, upload_bytes_total, download_bytes_total, websocket_connections, cache_hits{cache}, cache_misses{cache}, event_bus_published{topic}, event_bus_consumed{topic,group}, event_bus_lag{topic,group}.

  OpenTelemetry tracing: auto-instrument HTTP/gRPC/pgx/Redis. Custom spans for OPA eval, S3 ops, LLM calls, OCR. W3C Trace Context headers. Export to Tempo/Jaeger. Sample 1% normal, 100% errors.

  Grafana dashboards (JSON provisioning): Platform Overview (RPS, error rate, p50/95/99), Per-Service Health, Search Performance, Intelligence Pipeline, Tenant Health (noisy neighbor).

  Alerting (Prometheus rules): P1 pages (error>1% 5min, DB down, cross-tenant detection), P2 Slack (latency>500ms, queue>10K, disk>85%), P3 dashboard (cache<80%, job fail>5%).

═══ PROMPT 29: INTEGRATIONS ═══

Connector Service (services/connector/):
  Webhook system: POST /api/v1/webhooks — create subscription. Validate URL (HEAD request, no internal IPs). Generate HMAC-SHA256 secret. Delivery worker: POST with X-DMS-Signature + X-DMS-Timestamp headers. Retry: 5s, 30s, 2m, 15m, 1h, 6h. Dead letter after 6 failures. Delivery log in webhook_deliveries table.

  MCP Server: expose DMS as MCP tool. Tools: search_documents, get_document, upload_document, start_workflow, ask_question. JSON-RPC over SSE. API key auth.

  Microsoft 365 connector: Graph API OAuth, email ingestion (poll inbox), SharePoint migration (bulk import), Teams notifications.
  Salesforce connector: OAuth, attach docs to records, bidirectional metadata sync.
  Google Workspace connector: OAuth, Gmail ingestion, Drive migration.
  Each: encrypted config, auto token refresh, rate limit respect, error retry + admin notification.

  Public API docs: generate OpenAPI 3.1 spec via swag or manually maintained. API docs site with Redocly.

═══ PROMPT 30: LOAD TESTING & PRODUCTION READINESS ═══

Build k6 load tests in tests/load/:
  Scenario 1: Document CRUD — 1000 concurrent users, 500 creates/min, 5000 reads/min, sustained 30 min.
  Scenario 2: Search — 200 concurrent searches/sec, mixed modes, sustained 15 min.
  Scenario 3: Upload — 100 concurrent uploads (10-100MB), verify all complete with correct SHA-256.
  Scenario 4: OCR pipeline — 1000 docs queued, verify all OCR complete <30 min on 4 GPU workers.
  Scenario 5: WebSocket — 5000 concurrent connections, 100 broadcasts/sec, measure delivery latency.
  Scenario 6: Mixed realistic workload — 10K users, 1 hour, measure all SLIs.

  Target verification: search p99<300ms, upload init p99<200ms, OCR p95<30s, download URL p99<100ms, doc read p99<150ms, authz p99<5ms, autocomplete p99<50ms.

  Chaos tests (use Litmus or manual):
    - Kill random pod during load → verify no data loss, auto-recovery
    - Kill DB primary → verify Patroni failover <30s
    - Kill OpenSearch node → verify search still works (degraded)
    - Network partition between services → verify circuit breakers fire

  Cross-tenant isolation test: during load test with 10 tenants, verify ZERO cross-tenant data access.

  Production readiness checklist: all SLIs green, all security scans clean, backups tested, monitoring active, runbooks written, on-call rotation defined.

Build ALL k6 scripts, Makefile targets for running them, and a results dashboard in Grafana.
```

---

# FINAL NOTES

After completing all 30 prompts, you will have:
- 12 Go microservices
- 2 Python services (intelligence, preview)
- 1 Node.js service (collaboration)
- 1 React web app
- 1 React Native mobile app
- 1 Helm chart for Kubernetes deployment
- Complete CI/CD, observability, and security tooling
- Load tests verifying enterprise-scale performance

**Next steps after the build:**
1. External penetration test by a security firm
2. SOC 2 Type II readiness assessment
3. Bug bounty program on HackerOne
4. Beta deployment with 3-5 friendly customers
5. Production launch
