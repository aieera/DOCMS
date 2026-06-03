# SeDoc Audit — Executive Summary

**Date:** 2026-04-16
**Auditor:** Claude Code automated audit
**Scope:** Full monorepo — 14 services, 17 shared packages, React frontend, React Native mobile, Helm chart, CI/CD

---

## At a Glance

| Metric | Value |
|--------|-------|
| Total services | 14 (11 Go, 2 Python, 1 Node.js) |
| Total Go source files | 180 |
| Total Python source files | 57 |
| Total TypeScript/TSX files | 124 (107 web + 17 mobile) |
| Proto definitions | 13 files, 123 RPCs |
| Proto generated code | **0 files** |
| Go services that compile | **8 of 11** |
| Go test coverage | **<5%** |
| Frontend test coverage | **0%** |
| Services with real business logic | **14 of 14** (no empty scaffolds remain) |
| Helm chart | 69 templates, 3 value files |
| Security scan findings | 1 CRITICAL, 1 HIGH |

## What Works

The core architecture is sound. Shared packages (`pkg/`) are well-designed — tenant isolation via RLS, outbox pattern for transactional events, zerolog with PII scrubbing, AES-256-GCM envelope encryption. The four "Phase 1" services (document, auth, policy, storage) have the deepest implementation. Search service has the best test coverage and cleanest code. The React frontend compiles with zero TypeScript errors in strict mode. Helm chart is production-structured with HPA, PDB, NetworkPolicy, and ServiceMonitor per service.

## What's Broken

**Proto codegen has never been run.** This is the single largest blocker — it prevents 3 of 11 Go services from compiling and makes 120 of 123 proto RPCs orphaned. The frontend calls 10 REST endpoints that don't exist on any backend. Seven services have zero tests. MFA recovery codes use bcrypt cost 10 instead of 12. SAML X.509 cert generation uses `math/rand` instead of `crypto/rand`.

## Estimated Remediation: 18–22 developer-days

| Item | Effort |
|------|--------|
| Run proto codegen + fix buf lint errors | 1 day |
| Fix build failures (4 modules) | 0.5 day |
| Add missing REST endpoints for frontend (10) | 2 days |
| Fix security issues (math/rand, bcrypt cost, localStorage) | 1 day |
| Fix consistency issues (os.Getenv, middleware, outbox) | 1 day |
| Wire OCR pipeline (3 gaps) | 1 day |
| Add tests for 7 untested Go services | 5 days |
| Add frontend tests | 3 days |
| Add missing migrations for 9 services | 2 days |
| Missing Helm templates (5 services incomplete) | 1 day |
| Documentation (14 service READMEs) | 1 day |

---

## Top 10 CRITICAL Issues (security + data integrity)

| # | Issue | File:Line | Phase |
|---|-------|-----------|-------|
| 1 | `math/rand` for X.509 serial number — must be `crypto/rand` | services/auth/internal/sso/saml.go:52 | C |
| 2 | `sessionToken` stored in localStorage via Zustand persist — XSS vulnerable | web/src/store/authStore.ts:15 | C |
| 3 | Proto codegen never ran — 3 services can't compile, 120 RPCs orphaned | proto/gen/go/ (empty) | A,D,E |
| 4 | Shared KEK across all tenants (`vaultdms-storage-default`) | services/storage/cmd/server/main.go:137 | C |
| 5 | MFA recovery codes use bcrypt cost 10 (should be 12) | services/auth/internal/service/mfa.go:368 | C |
| 6 | 4 direct NATS publishes bypass outbox (event loss on crash) | services/signature/internal/service/service.go:157,176; services/workflow/internal/activities/activities.go:60,79 | B |
| 7 | Billing service gRPC chain missing 3 interceptors (no tenant isolation on gRPC) | services/billing/cmd/server/main.go:92 | B |
| 8 | 14 NATS message handlers use `context.Background()` without timeout | services/search/internal/service/indexer.go (12 instances), services/audit, services/connector | C |
| 9 | Redis session/MFA keys lack tenant_id prefix (`session:{hash}`, `mfa_session:{hash}`) | services/auth/internal/service/session.go:242, mfa.go:155 | C |
| 10 | OCR pipeline disconnected — results never persisted, events never published | services/intelligence/app/tasks/ocr.py (return only) | A |

---

## Top 10 HIGH Issues (broken builds, missing features)

| # | Issue | Location | Phase |
|---|-------|----------|-------|
| 1 | services/document won't compile (proto gen) | cmd/server/main.go:31 | E |
| 2 | services/policy won't compile (proto gen) | cmd/server/main.go:25 | E |
| 3 | services/storage won't compile (proto gen) | cmd/server/main.go:28 | E |
| 4 | services/auth compile error: unused import | internal/scim/repo.go:5 | E |
| 5 | 10 frontend API calls hit nonexistent backend endpoints | web/src/api/permissions.ts, admin.ts | D |
| 6 | buf lint: 26 errors (missing googleapis dep, duplicate ShareLink) | proto/vaultdms/v1/document.proto:10, collaboration.proto:54 | E |
| 7 | cmd/dms-admin not in go.work — cannot build | cmd/dms-admin/main.go | E |
| 8 | 9 services have zero migrations (tables in mega-migration only) | services/{auth,policy,storage,...}/migrations/ | A |
| 9 | npm audit: 6 vulnerabilities (4 HIGH) in web dependencies | web/package.json (transitive) | E |
| 10 | Missing services: gateway, controlplane | services/ | A |

---

## Top 10 MEDIUM Issues (consistency, code quality)

| # | Issue | Count | Phase |
|---|-------|-------|-------|
| 1 | `os.Getenv()` bypassing pkg/config | 11 calls across 4 services | B |
| 2 | `errors.New()` instead of pkg/errors domain types | 11 instances in auth, document, storage | B |
| 3 | `fmt.Errorf()` without `%w` wrapping | 33 instances across 8 services | B |
| 4 | Hardcoded fallback addresses in main.go | 5 instances | B |
| 5 | 7 of 11 Go services have ZERO test files | audit, billing, connector, notification, signature, storage, workflow | F |
| 6 | 15 of 17 pkg packages have ZERO tests | all except dlp (84%) and storage (10.7%) | F |
| 7 | 0 frontend test files (107 TS/TSX files untested) | web/src/, mobile/ | F |
| 8 | 14 service READMEs missing | all services/ | A |
| 9 | ~26 frontend components missing from spec | workflow designer, search facets, admin dialogs, etc. | A |
| 10 | Helm templates incomplete for 5 services (billing, collaboration, connector, intelligence, preview) | deploy/helm/vaultdms/templates/ | A |

---

## Recommended Remediation Order

Fix in this sequence — each step unblocks the next:

### Week 1: Unblock builds (3 days)

1. **Run `buf generate`** — fix googleapis dep in buf.yaml, remove duplicate ShareLink. This unblocks document, policy, storage compilation and 120 proto RPCs.
2. **Fix auth unused import** (`scim/repo.go:5` remove `encoding/json`).
3. **Add cmd/dms-admin to go.work.**
4. **Run `npm audit fix`** on web/.

### Week 1: Fix security (2 days)

5. **Replace `math/rand` with `crypto/rand`** in saml.go:52.
6. **Bump MFA bcrypt cost** from 10 to 12 in mfa.go:368.
7. **Move session token to httpOnly cookie** or sessionStorage (authStore.ts).
8. **Add tenant_id prefix** to Redis session/MFA keys.

### Week 2: Fix consistency (2 days)

9. **Move all `os.Getenv()` calls into pkg/config** (11 violations).
10. **Add missing interceptors** to billing gRPC chain.
11. **Replace 4 direct NATS publishes** with outbox pattern (signature, workflow).
12. **Wrap NATS handlers** with `context.WithTimeout` (14 instances).

### Week 2: Wire broken pipelines (2 days)

13. **Wire OCR pipeline**: persist results to `ocr_results` table, publish `dms.version.ocr_completed.v1`, fix event subject mismatch.
14. **Add missing REST endpoints** for frontend (permissions, admin users, settings path alignment).

### Week 3: Tests (5 days)

15. **Add unit tests** for 7 untested Go services (minimum: handler validation + service business logic).
16. **Add pkg tests** for crypto, middleware, validation, errors.
17. **Run integration tests** with Docker (document, outbox).

### Week 3–4: Polish (4 days)

18. **Replace `errors.New`** with typed domain errors (11 instances).
19. **Add `%w` wrapping** to fmt.Errorf calls (33 instances).
20. **Complete Helm templates** for 5 services.
21. **Add service READMEs** (14 services).
22. **Add missing frontend components** (26 items from spec).

---

## Architecture Strengths (preserve these)

- Multi-tenant RLS isolation pattern — consistent across all services
- Outbox pattern for transactional events — eliminates dual-write problem
- zerolog with PII scrubbing (HashEmail, HashIP) — production-grade logging
- SHA-256 hashed session tokens — never stores plaintext
- Cursor-based pagination everywhere (except SCIM, which requires OFFSET per RFC)
- DLP pipeline with configurable rules and actions
- Comprehensive Helm chart with HPA, PDB, NetworkPolicy per service
- 7 k6 load test scenarios including cross-tenant isolation verification
- 13-rule Prometheus alerting in 3 severity tiers

---

## Audit File Index

| File | Phase | Content |
|------|-------|---------|
| [01-inventory.md](01-inventory.md) | A | Full repo inventory — services, packages, protos, frontend |
| [02-missing.md](02-missing.md) | A | Missing items vs. playbook spec |
| [03-inconsistencies.md](03-inconsistencies.md) | B | Logger, config, error, middleware, DB, outbox, port checks |
| [04-antipatterns.md](04-antipatterns.md) | C | Security + quality pattern sweep (20 categories) |
| [05-contracts.md](05-contracts.md) | D | Proto RPC → handler → client cross-reference |
| [06-build.md](06-build.md) | E | Compiler, linter, validator results |
| [07-tests.md](07-tests.md) | F | Test coverage per service and package |
| [00-summary.md](00-summary.md) | G | This file |
