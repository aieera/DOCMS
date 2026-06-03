# VaultDMS Audit — Phase B: Consistency Check

**Date:** 2026-04-16

---

## 1. Logger Consistency

**Status: CLEAN** — no violations found.

All 11 Go services use `logger.New()` from `pkg/logger`. No `log.Println`, `log.Printf`, `log.Fatal`, or `fmt.Printf` used for logging in service code. `fmt.Fprintf(os.Stderr, ...)` is used only in main.go fatal paths (correct pattern).

---

## 2. Config Consistency — 11 violations

Every service uses `config.Load()` but several also call `os.Getenv()` directly, bypassing the centralized config:

| # | File | Line | Call | Should be |
|---|------|------|------|-----------|
| 1 | services/auth/cmd/server/main.go | 117 | `os.Getenv("SEDOC_PUBLIC_URL")` | Add to pkg/config.Config |
| 2 | services/auth/cmd/server/main.go | 131 | `os.Getenv("SEDOC_PUBLIC_URL")` | Same |
| 3 | services/auth/cmd/server/main.go | 138 | `os.Getenv("SEDOC_PUBLIC_URL")` | Same |
| 4 | services/auth/cmd/server/main.go | 196 | `os.Getenv("SEDOC_LOCAL_KEK")` | Already in cfg.LocalKEK |
| 5 | services/document/cmd/server/main.go | 81 | `os.Getenv("POLICY_SERVICE_ADDR")` | Add to Config |
| 6 | services/document/cmd/server/main.go | 105 | `os.Getenv("SEDOC_PUBLIC_URL")` | Add to Config |
| 7 | services/storage/cmd/server/main.go | 84 | `os.Getenv("POLICY_SERVICE_ADDR")` | Add to Config |
| 8 | services/storage/cmd/server/main.go | 108 | `os.Getenv("SEDOC_LOCAL_KEK")` | Already in cfg.LocalKEK |
| 9 | services/storage/cmd/server/main.go | 136 | `os.Getenv("SEDOC_S3_PUBLIC_BASE")` | Add to Config |
| 10 | services/billing/internal/handler/handler.go | 29 | `os.Getenv("SEDOC_INTERNAL_API_KEY")` | Add to Config |
| 11 | services/billing/internal/stripe/webhook.go | 37 | `os.Getenv("STRIPE_WEBHOOK_SECRET")` | Add to Config |

**Hardcoded fallback addresses (lower severity):**

| File | Line | Value |
|------|------|-------|
| services/document/cmd/server/main.go | 83 | `"localhost:9091"` |
| services/storage/cmd/server/main.go | 74 | `"clamav:3310"` |
| services/storage/cmd/server/main.go | 86 | `"localhost:9091"` |
| services/search/cmd/server/main.go | 72 | `"http://opensearch:9200"` |
| services/workflow/cmd/server/main.go | 68 | `"localhost:7233"` |

---

## 3. Error Handling — 44 violations

### 3a. `errors.New()` instead of `pkg/errors` domain types (11 instances)

| File | Line | Error string |
|------|------|-------------|
| services/auth/internal/service/service.go | 180 | `"invalid credentials"` |
| services/auth/internal/service/service.go | 184 | `"account temporarily locked"` |
| services/auth/internal/sso/saml.go | 76 | `"sp key PEM: empty or malformed"` |
| services/auth/internal/sso/saml.go | 80 | `"sp cert PEM: empty or malformed"` |
| services/auth/internal/sso/saml.go | 92 | `"sp key is not RSA"` |
| services/auth/internal/sso/saml.go | 341 | `"idp metadata URL/XML missing"` |
| services/auth/internal/sso/saml.go | 404 | `"empty root"` |
| services/auth/internal/sso/oidc.go | 263 | `"oidc config missing issuer_url..."` |
| services/auth/cmd/server/main.go | 198 | `"SEDOC_LOCAL_KEK not set"` |
| services/document/internal/handler/mappers.go | 161 | `"unsupported lifecycle action"` |
| services/storage/internal/scanner/clamav.go | 139 | `"clamav unparseable response"` |

### 3b. `fmt.Errorf()` without `%w` wrapping (33 instances)

Top offenders by service:
- **connector**: 14 instances (webhook, mcp, providers)
- **auth**: 6 instances (mfa, main)
- **workflow**: 4 instances (service.go)
- **document**: 3 instances (lifecycle, cursor, service)
- **audit**: 2 instances
- **signature**: 2 instances
- **billing**: 1 instance
- **search**: 1 instance

---

## 4. Middleware Chain — 1 violation

**Expected order on every gRPC server:**
```
RecoveryInterceptor → CorrelationInterceptor → TenantInterceptor → RequestLogInterceptor
```

| Service | Recovery | Correlation | Tenant | RequestLog | Status |
|---------|----------|-------------|--------|------------|--------|
| audit | ✓ L75 | ✓ L76 | ✓ L77 | ✓ L78 | OK |
| **auth** | HTTP only | HTTP only | **No gRPC** | HTTP only | **QUESTION** — auth uses HTTP middleware stack, no gRPC server. Intentional? |
| **billing** | ✓ L93 | **MISSING** | **MISSING** | **MISSING** | **VIOLATION** — only Recovery |
| connector | ✓ L89 | ✓ L90 | ✓ L91 | ✓ L92 | OK |
| document | ✓ L118 | ✓ L119 | ✓ L120 | ✓ L121 | OK |
| notification | ✓ L75 | ✓ L76 | ✓ L77 | ✓ L78 | OK |
| policy | ✓ L93 | ✓ L94 | ✓ L95 | ✓ L96 | OK |
| search | ✓ L111 | ✓ L112 | ✓ L113 | ✓ L114 | OK |
| signature | ✓ L80 | ✓ L81 | ✓ L82 | ✓ L83 | OK |
| storage | ✓ L150 | ✓ L151 | ✓ L152 | ✓ L153 | OK |
| workflow | ✓ L102 | ✓ L103 | ✓ L104 | ✓ L105 | OK |

**Violations:**
- `services/billing/cmd/server/main.go:92-93` — gRPC chain has only `RecoveryInterceptor`. Missing Correlation, Tenant, RequestLog.

**Questions:**
- Auth service uses HTTP middleware chain (chi/mux based), no gRPC server. Is this intentional? All other services have both gRPC + HTTP.

---

## 5. Database Access — 5 cross-tenant queries

All are in operator/cron functions, likely intentional:

| File | Line | Function | Justification |
|------|------|----------|---------------|
| services/document/internal/compliance/retention.go | 58 | `enforce()` | Cross-tenant retention scan — operator cron |
| services/document/internal/compliance/retention.go | 90 | `enforce()` | Lifecycle transition update |
| services/billing/internal/repository/repository.go | 164 | `ListAllTenants()` | Usage metering cron |
| services/billing/internal/provisioner/provisioner.go | 98 | `createAdminUser()` | Tenant provisioning (no tenant context exists yet) |
| services/storage/internal/repository/content_blobs.go | 90 | `ListZeroRefOlderThan()` | Reaper — uses `SET LOCAL row_security = off` |

**QUESTION:** Should these be flagged or are they accepted operator-level queries?

---

## 6. Outbox Pattern — 4 violations

Services publishing NATS events directly instead of through the outbox table:

| # | File | Line | Subject | Risk |
|---|------|------|---------|------|
| 1 | services/signature/internal/service/service.go | 157 | `dms.notify.signature_requested.v1` | Event lost if process crashes after DB commit |
| 2 | services/signature/internal/service/service.go | 176 | `dms.signature.completed.v1` | Same |
| 3 | services/workflow/internal/activities/activities.go | 60 | `dms.notify.workflow_assigned.v1` | Same |
| 4 | services/workflow/internal/activities/activities.go | 79 | Variable subject via `PublishEvent` | Same |

**Note:** `services/notification/internal/service/service.go:59` uses `rdb.Publish` (Redis pub/sub for real-time WebSocket push). This is correct — it's not a durable event, it's a fanout notification.

---

## 7. Port Consistency

**Status: CLEAN** — All services use `cfg.HTTPPort`, `cfg.GRPCPort`, `cfg.HealthPort` from the config struct. No hardcoded ports in server bind calls. Default values (8080/9090/8081) are set in `pkg/config/config.go`.

---

## 8. gRPC Registration

Only 3 of 11 Go services register gRPC service handlers:

| Service | Registers gRPC handler | Proto service |
|---------|----------------------|---------------|
| document | ✓ `RegisterDocumentServiceServer` | document.proto |
| policy | ✓ `RegisterPolicyServiceServer` | policy.proto |
| storage | ✓ `RegisterStorageServiceServer` | storage.proto |
| audit | ✗ | audit.proto exists |
| auth | ✗ (HTTP-only) | auth.proto exists |
| billing | ✗ | billing.proto exists |
| connector | ✗ | — |
| notification | ✗ | notification.proto exists |
| search | ✗ | search.proto exists |
| signature | ✗ | signature.proto exists |
| workflow | ✗ | workflow.proto exists |

**Root cause:** Proto codegen never ran, so there are no generated `Register*Server` functions for these services. They all have empty gRPC servers (listener + `grpc.NewServer()` but no registered handlers).

---

## Summary

| Category | Violations | Severity |
|----------|-----------|----------|
| Logger | 0 | — |
| Config (os.Getenv) | 11 | MEDIUM |
| Config (hardcoded fallbacks) | 5 | LOW |
| errors.New (should be typed) | 11 | MEDIUM |
| fmt.Errorf without %w | 33 | LOW |
| Middleware chain | 1 (billing) | HIGH |
| Cross-tenant DB queries | 5 (intentional?) | QUESTION |
| Outbox pattern bypass | 4 | HIGH |
| Port consistency | 0 | — |
| gRPC registration | 8 services unregistered | HIGH (blocked by proto gen) |
| **Total** | **78** | |
