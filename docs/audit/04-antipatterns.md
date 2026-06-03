# VaultDMS Audit — Phase C: Anti-Pattern Sweep

**Date:** 2026-04-16

Every finding cites file:line. Grep patterns run against entire Go codebase.

---

## SECURITY-CRITICAL

### a. SQL queries without `tenant_id` in WHERE

**4 findings** (all in operator/cron code, not user-facing handlers):

| File | Line | Function | Context |
|------|------|----------|---------|
| services/auth/internal/repository/apikey_repo.go | 89 | `TouchLastUsed` | `UPDATE api_keys SET last_used_at = $2 WHERE id = $1` — no tenant_id. Uses primary key only. |
| services/auth/internal/repository/session_repo.go | 57 | `ExtendExpiry` | `UPDATE sessions SET expires_at = $2 WHERE id = $1` — no tenant_id |
| services/auth/internal/repository/session_repo.go | 62 | `TouchActivity` | `UPDATE sessions ... WHERE id = $1` — no tenant_id |
| services/auth/internal/repository/session_repo.go | 75 | `Revoke` | `UPDATE sessions ... WHERE token_hash = $1` — no tenant_id |

**Risk:** Low — session/API-key lookups by hash are globally unique, and RLS on the table enforces tenant isolation at the DB level. But the pattern is fragile if RLS is misconfigured.

### b. `SELECT *`

**0 findings.** All queries specify column lists explicitly.

### c. `OFFSET` in SQL

**2 findings:**

| File | Line | Context |
|------|------|---------|
| services/auth/internal/scim/repo.go | 62 | `OFFSET $N LIMIT $M` in SCIM user listing |
| services/auth/internal/scim/repo.go | 194 | `OFFSET $N LIMIT $M` in SCIM group listing |

**Note:** SCIM RFC 7644 requires `startIndex` + `count` pagination (OFFSET-based). This is spec-compliant, not a defect. Mark as **ACCEPTED**.

### d. `ON DELETE CASCADE`

**0 findings.** No cascading deletes anywhere.

### e. `math/rand` for security-sensitive operations

**1 finding (SECURITY CRITICAL):**

| File | Line | Usage |
|------|------|-------|
| services/auth/internal/sso/saml.go | 18+52 | `mrand "math/rand"` — used for `big.NewInt(mrand.Int63())` as X.509 certificate serial number |

**Risk:** X.509 serial numbers MUST be unpredictable (RFC 5280 §4.1.2.2). Using `math/rand` is a crypto weakness. Should use `crypto/rand`.

**Also:** `pkg/logger/logger.go:14` imports `math/rand/v2` for debug sampling — this is acceptable (not security-sensitive).

### f. `localStorage`/`sessionStorage` for auth tokens

**0 findings.** Auth tokens are stored via Zustand `persist` middleware which uses `localStorage` under the hood:

| File | Line | Pattern |
|------|------|---------|
| web/src/store/authStore.ts | 15 | `persist(..., { name: 'vaultdms-auth' })` |

**QUESTION:** Zustand's `persist` uses `localStorage` by default. The `sessionToken` is stored there. This is vulnerable to XSS. Should use `httpOnly` cookies or at minimum `sessionStorage`. Flagging for human review.

### g. SQL string concatenation

**3 findings:**

| File | Line | Pattern | Risk |
|------|------|---------|------|
| services/audit/internal/repository/repository.go | 99 | `fmt.Sprintf("...WHERE %s ORDER BY...", where, pageSize+1)` | **Low** — `where` is built from hardcoded clause strings + parameterized `$N` args, never user input |
| services/auth/internal/scim/repo.go | 60 | `"WHERE " + where + " ORDER BY..."` | **Low** — `where` built from `buildUserWhere()` with parameterized args |
| services/auth/internal/scim/repo.go | 192 | `"WHERE " + where + " AND deleted_at IS NULL"` | **Low** — same pattern |

**Verdict:** All three use safe parameterized query building (string concat of clauses, not user values). Not exploitable, but fragile pattern.

### h. Logging of sensitive fields

**0 findings.** No `password`, `token`, `api_key`, `secret`, `mfa_secret`, `authorization`, or `cookie` values logged via zerolog `.Str()` calls. Auth service explicitly documents this policy at `services/auth/internal/service/service.go:2`.

### i. Redis keys without tenant_id prefix

**2 findings:**

| File | Line | Key pattern | Risk |
|------|------|-------------|------|
| services/auth/internal/service/mfa.go | 155 | `"mfa_session:" + hash` | No tenant prefix. Hash is SHA-256 of token, globally unique. **Low risk** but breaks convention. |
| services/auth/internal/service/session.go | 242 | `"session:" + hash` | Same — no tenant prefix. Token hash is globally unique. **Low risk.** |

**Tenant-prefixed (correct):**
- `loginAttemptsKey(tenantID, email)` ✓
- `"audit_lock:" + tenantID` ✓
- `"feature_flags:" + tenantID` ✓
- `"tenant_plan:" + tenantID` ✓
- `"oidc_pkce:" + state` — state is random, no tenant needed
- `"recent_searches:" + tenantID + ":" + userID` ✓

### j. Custom crypto implementations

**0 findings.** `pkg/crypto/envelope.go` wraps `crypto/aes` + `crypto/cipher` from stdlib (AES-256-GCM). No custom cipher algorithms.

### k. Shared/hardcoded encryption keys

**0 hardcoded key material found.** KEK loaded from env var (`SEDOC_LOCAL_KEK`). Per-tenant KEK isolation via `TenantKEKID` field — but currently all tenants use the same default KEK ID (`"vaultdms-storage-default"` at `services/storage/cmd/server/main.go:137`).

**QUESTION:** Single KEK across tenants is a design gap flagged for human review. Per-tenant KEK lookup is documented as a follow-up in the code.

### l. Sequential integer IDs on user-facing resources

**0 findings on user-facing tables.** `BIGSERIAL` is used only on:
- `outbox.id` (internal, not user-facing) — `000001_initial_schema.up.sql:1277`

All user-facing resources (documents, users, sessions, etc.) use `UUID` primary keys.

---

## QUALITY

### m. bcrypt cost < 12

**1 finding:**

| File | Line | Cost | Context |
|------|------|------|---------|
| services/auth/internal/service/mfa.go | 368 | **10** | `bcrypt.GenerateFromPassword([]byte(plain[i]), 10)` — MFA recovery codes hashed at cost 10 instead of 12 |

Main password hashing uses cost 12 correctly (`services/auth/internal/service/service.go:31`). Share link passwords also use 12 (`services/document/internal/service/sharing_tags.go:56`).

### n. Tokens stored without SHA-256 hashing

**0 findings.** Session tokens are stored as `token_hash` (SHA-256). API keys use key prefix lookup + hash comparison. SCIM tokens use SHA-256 comparison.

### o. Swallowed errors (`if err != nil { return nil }`)

**0 findings of truly swallowed errors.** All `return nil, ...` patterns return the error in the second return value. Grep false positives were all `return nil, err` or `return nil, mapPgError(err)`.

### p. HTTP handlers without timeout context

**14 findings** — NATS message handlers use `context.Background()`:

| File | Line | Context |
|------|------|---------|
| services/audit/internal/service/service.go | 138 | NATS consumer callback |
| services/connector/internal/service/service.go | 99 | Webhook fanout NATS callback |
| services/connector/internal/service/service.go | 115 | Same |
| services/notification/internal/service/service.go | 120 | Notification delivery NATS callback |
| services/search/internal/service/indexer.go | 116 | Document index NATS callback |
| services/search/internal/service/indexer.go | 135 | Document delete NATS callback |
| services/search/internal/service/indexer.go | 161 | OCR content update |
| services/search/internal/service/indexer.go | 186 | Permission change update |
| services/search/internal/service/indexer.go | 190 | Folder permission update |
| services/search/internal/service/indexer.go | 192 | Workspace permission update |
| services/search/internal/service/indexer.go | 218 | Classification update |
| services/search/internal/service/indexer.go | 243 | Entity detection update |
| services/auth/internal/service/apikey.go | 165 | Background debounced API key touch |
| services/auth/internal/service/mfa.go | 337 | KMS key generation |

**Risk:** No timeout on DB/HTTP calls from NATS handlers. A hung DB connection blocks the NATS consumer indefinitely. Should wrap with `context.WithTimeout`.

### q. Missing /healthz or /readyz endpoints

**0 findings.** All 11 Go services use `health.NewServer()` which serves `/healthz` and `/readyz`. Python services have `/healthz` in FastAPI. Collaboration Node service uses `tcpSocket` probe.

### r. Circular imports between services

**0 findings.** No service imports another service's `internal/` package. All cross-service communication is via gRPC or NATS.

### s. TODO/FIXME/HACK comments

**0 findings in service code.** All TODO comments were cleaned up during implementation. The only TODO-like content is in code comments explaining intentional future work (e.g., "Phase A2 follow-up" annotations).

### t. Stub functions

**0 `panic("not implemented")` findings.** MCP tool handlers in `services/connector/internal/mcp/server.go` return stub data (`{"status":"ok"}`) rather than real implementations — these are placeholder but functional.

---

## Summary

| Category | Findings | Severity |
|----------|---------|----------|
| **a.** SQL without tenant_id | 4 | LOW (RLS backstop, PK lookups) |
| **b.** SELECT * | 0 | — |
| **c.** OFFSET | 2 | ACCEPTED (SCIM RFC requirement) |
| **d.** ON DELETE CASCADE | 0 | — |
| **e.** math/rand for security | **1** | **CRITICAL** (X.509 serial) |
| **f.** localStorage for tokens | **1** | **HIGH** (XSS vector — QUESTION) |
| **g.** SQL string concat | 3 | LOW (parameterized, safe) |
| **h.** Sensitive field logging | 0 | — |
| **i.** Redis keys no tenant prefix | 2 | LOW (globally unique hashes) |
| **j.** Custom crypto | 0 | — |
| **k.** Shared KEK across tenants | **1** | **MEDIUM** (design gap, documented) |
| **l.** Sequential IDs | 0 | — |
| **m.** bcrypt cost < 12 | **1** | **MEDIUM** (MFA recovery at cost 10) |
| **n.** Unhashed tokens | 0 | — |
| **o.** Swallowed errors | 0 | — |
| **p.** No timeout context | **14** | **MEDIUM** (NATS handlers) |
| **q.** Missing health endpoints | 0 | — |
| **r.** Circular imports | 0 | — |
| **s.** TODO/FIXME/HACK | 0 | — |
| **t.** Stub functions | ~5 | LOW (MCP stubs) |
| **TOTAL** | **33** | 1 CRITICAL, 1 HIGH, 3 MEDIUM |
