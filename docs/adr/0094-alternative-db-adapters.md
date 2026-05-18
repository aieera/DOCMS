# ADR 0094 — Alternative database adapters (§13.3) — design plan

**Status:** Planning. No driver implementations shipped. This ADR is the
design artifact for a future multi-quarter engineering effort. The
playbook entry's "ADR 0085" reference was stale (taken by saved-search
alerts). The companion `/admin/platform/db-info` page IS shipped in
this same commit series and is intentionally honest about today's
state: only PostgreSQL 16 is supported.

**Date:** 2026-05-18

## Context

Blueprint §13.3 calls for MySQL 8, Oracle 19c, and SQL Server 2019+
support alongside the current PostgreSQL 16 backend. This is the
single largest item in the §13 release-readiness section, and it
**cannot honestly be shipped as a single-session deliverable**. This
ADR captures the design decisions so the next engineer (or future
contributor) starts against a written plan rather than reverse-
engineering the current PostgreSQL-coupled code.

The decision to write a plan instead of code-while-pretending was
deliberate. See "Why this is a plan, not an implementation" below.

## Reality of today's PostgreSQL coupling

A repo-wide audit (2026-05-18) shows:

| Coupling source | Count | Why each matters |
|---|---|---|
| Go files that use `pgx.Tx` or `pgxpool.Pool` directly | **143** | Every repository function signature changes when we abstract. Not a "thin wrapper" job. |
| Migrations using `ENABLE ROW LEVEL SECURITY` | **37** | Tenant isolation security model. PostgreSQL RLS is **defense in depth** — a query that omits `WHERE tenant_id = ?` fails-closed (0 rows). MySQL has no equivalent; Oracle uses VPD (different API); SQL Server uses Security Policies (also different). |
| Migrations using JSONB | **18** | MySQL `JSON`, Oracle `JSON storage`, SQL Server `nvarchar(max) + JSON_VALUE` each have different operator syntax + indexing. Cross-driver `SELECT data->'foo'` doesn't exist. |
| Migrations using LTREE (folder paths) | **1** | `services/document/migrations/000001_initial_schema.up.sql` — folder hierarchy. **No equivalent on any other database.** Replacement: recursive CTEs (works everywhere; perf characteristics differ). |
| Migrations using partial indexes | **2** | MySQL doesn't support `CREATE INDEX ... WHERE`. Oracle 12c+ does (function-based indexes); SQL Server has filtered indexes. Need per-driver migration variants. |
| Total migrations | **56** | × 3 alt drivers = **168 parallel migration files** to author + maintain forever. Schema drift between drivers is a permanent source of bugs. |

## Why this is a plan, not an implementation

A single-session "alt DB adapters" PR would necessarily ship:

- A `pkg/db` interface that wraps 20-30 of the most common pgx use
  cases. Leaves 100+ files still depending on pgx directly.
- MySQL/Oracle/SQL Server "drivers" that compile but fail at runtime
  on real queries.
- A migration scheme that translates **some** PostgreSQL schema to
  each driver, missing dialect-specific features without compile-time
  warnings.
- A claim of completeness in the changelog.

The risk is not aesthetic. The security model in [CLAUDE.md](../../CLAUDE.md)
explicitly relies on PostgreSQL RLS as defense-in-depth: *"a buggy
query that omits the tenant predicate fails-closed (0 rows) rather
than leaking."* On MySQL, that property disappears. Every raw SQL
query in the codebase (200+) becomes a potential cross-tenant data
leak if a developer forgets the `WHERE tenant_id = ?`. Catching that
needs a CI gate that doesn't exist yet, a refactor of every
repository function, AND time to find the queries that hide in
edge cases (subqueries, CTEs, dynamic SQL).

Shipping a half-built abstraction that someone might trust is **worse
than not shipping** for this class of feature.

## Decisions (for the eventual implementation)

### 1. Three-layer abstraction, not a single interface

Don't try to make every database look the same. Instead:

**Layer 1 — Connection / transaction abstraction (`pkg/db`).**
- Wrap `pgx.Tx` behind a `DB` interface with `Exec`, `Query`, `QueryRow`,
  `BeginTx`, `WithTenantTx`. This is the "drop-in pgx-equivalent"
  surface.
- All 143 files migrate to this interface in one pass — straightforward
  search-and-replace because the method names are kept identical.
- Each driver implements `DB` for its own native client (`go-sql-driver/mysql`,
  `godror`, `denisenkom/go-mssqldb`).
- Estimate: 1-2 weeks of focused refactor + test.

**Layer 2 — Migration engine (`pkg/migrations`).**
- Per-driver migration directories: `services/<svc>/migrations/{pg,mysql,oracle,mssql}/`.
- Existing `services/<svc>/migrations/*.sql` move into `pg/`. Other
  drivers get hand-authored equivalents.
- Migration runner reads `DB_DRIVER` env var to pick the directory.
- Schema versioning bookkeeping table stays per-driver
  (`<svc>_schema_migrations` becomes per-driver-table).
- Estimate: 3-4 weeks for the first non-PG driver; 1-2 weeks each
  for the next two (most patterns reusable).

**Layer 3 — Feature gates (`pkg/dbcapability`).**
- A read-only registry that exposes "what works on this driver":
  - `RLS`: `postgres-only`. MySQL falls back to app-enforced; Oracle
    uses VPD; SQL Server uses Security Policies.
  - `JSONOps`: `native` on Postgres/MySQL/SQL Server, `partial` on
    Oracle pre-21c.
  - `Vector`: `pgvector-builtin` on Postgres; `external-qdrant` on
    everything else.
  - `LTREE`: `native` on Postgres only; `recursive-cte-emulation`
    on everything else.
  - `PartialIndex`: `native` on Postgres + SQL Server (filtered);
    `function-based-emulation` on Oracle; **not supported** on
    MySQL — affected indexes drop or convert to full indexes.
- Services that need a feature MUST check capability and degrade
  cleanly. Example: search service falls back to lexical-only when
  `Vector` is `external-qdrant` and Qdrant is unreachable.
- This registry is what `/admin/platform/db-info` reads to render
  the feature matrix on screen.

### 2. Tenant isolation strategy (the security-critical part)

On PostgreSQL, RLS is the primary line of defense:

```sql
CREATE POLICY ... USING (tenant_id = current_setting('app.current_tenant')::uuid);
ALTER ROLE vaultdms NOBYPASSRLS;
```

A buggy query without `WHERE tenant_id` returns 0 rows, not someone
else's data. This is intentional defense-in-depth.

On MySQL — there is no equivalent. The replacement strategy MUST be:

1. **Repository-layer enforcement:** every `DB.Query`/`DB.Exec` call goes
   through a wrapper that requires a tenant ID and prepends `WHERE
   tenant_id = ?` to every statement. Raw SQL is **forbidden** at
   the application layer.
2. **CI gate (mandatory):** static analysis that fails the build on
   any raw SQL string that doesn't include `tenant_id` (with a small
   allowlist for legitimately-tenant-free queries like the
   `organizations` table itself).
3. **Triple-redundant integration test suite:** for every repository
   function, a "wrong tenant" test that asserts the query returns
   nothing or rejects.

On Oracle: use VPD policies (`DBMS_RLS.ADD_POLICY`) that prepend the
predicate at the database layer. Closest match to PostgreSQL RLS.

On SQL Server: use Security Policies (`CREATE SECURITY POLICY`).
Different syntax, same intent.

**Implementation order is non-negotiable:** Oracle and SQL Server
ship before MySQL, because they retain DB-level defense in depth.
MySQL is the **hardest** of the three because it requires the
application-side rewrite.

### 3. Vector search on non-Postgres drivers

The search service already uses Qdrant for vector storage (per the
PROJECT_STATUS audit, the vector path is "half-built" today — ingest
writes to Qdrant, query side hasn't been wired). On non-Postgres
drivers, Qdrant becomes the **only** vector store, not an option.
This is actually simpler than the current dual-path code; the
capability flag drives whether `pgvector` is even compiled in.

### 4. Per-driver feature matrix (the values exposed by the UI)

| Feature | Postgres 16 | MySQL 8 | Oracle 19c | SQL Server 2019+ |
|---|---|---|---|---|
| Row-level security | ✅ RLS + NOBYPASSRLS | 🟡 App-enforced + CI gate | ✅ VPD policies | ✅ Security Policies |
| JSON columns | ✅ JSONB + GIN | ✅ JSON (no indexing of nested keys without virtual columns) | 🟡 21c+ for full support | ✅ JSON_VALUE + JSON_QUERY |
| Vector search | ✅ pgvector | 🟡 External Qdrant only | 🟡 External Qdrant only | 🟡 External Qdrant only |
| Folder hierarchy (LTREE) | ✅ Native | 🟡 Recursive CTE | 🟡 Recursive CTE | 🟡 Recursive CTE |
| Partial indexes | ✅ `WHERE` clauses | 🔴 Drop / full index | 🟡 Function-based emulation | ✅ Filtered indexes |
| Generated columns | ✅ | ✅ | ✅ (12c+) | ✅ |
| Trigram search | ✅ pg_trgm | 🔴 Not supported (fall back to OpenSearch) | 🔴 Not supported | 🔴 Not supported |

🟡 means degraded but functional. 🔴 means the feature is unavailable
on that driver — services using it must check `dbcapability` and
degrade gracefully.

### 5. Phased rollout (suggested 12-month plan)

- **Phase 1 (4 weeks):** Build Layer 1 abstraction. Refactor all 143
  files. Land a `DB_DRIVER=postgres` env var that's the only valid
  value. No new drivers yet. **Acceptance:** all tests pass; no
  behaviour change.
- **Phase 2 (8 weeks):** Build Layer 2 + 3. Ship Oracle as the first
  alternate driver — it has VPD, so it keeps DB-level tenant isolation.
  Per-driver migration directory `pg/` + `oracle/`. CI runs all
  integration tests against both. **Acceptance:** Oracle install
  passes the same RLS-violation tests as Postgres.
- **Phase 3 (8 weeks):** SQL Server. Same shape as Oracle (Security
  Policies for tenant isolation). **Acceptance:** same.
- **Phase 4 (12 weeks):** MySQL — the hardest. Application-side tenant
  enforcement, CI gate against raw SQL without `tenant_id`, triple-
  redundant integration tests, and security review of every repository
  function. **Acceptance:** dedicated red-team test attempting cross-
  tenant data leaks via every public API surface.

Total: **~8 months** of one engineer's time. Comparable to the actual
DBAs at companies like Mailchimp / Atlassian who maintain multi-
backend platforms.

## What ships in THIS commit series

| Path | Purpose |
|---|---|
| `docs/adr/0094-alternative-db-adapters.md` | This document (design + phased plan + security notes) |
| `services/document/internal/handler/db_info_handler.go` | `GET /api/v1/admin/platform/db-info` returning current driver + version + feature matrix |
| `services/document/cmd/server/main.go` | Route mount |
| `web/src/api/platform.ts` | Typed client |
| `web/src/routes/_authenticated/admin/platform/db-info.tsx` | Read-only admin page |
| `web/src/routes/_authenticated/admin/index.tsx` | New tile |

**Explicitly NOT in this commit series:**

- `pkg/db` abstraction layer
- Any alt-driver code
- Any non-Postgres migrations
- Any change to tenant isolation enforcement
- Any change to CI

The frontend page reports today's reality: PostgreSQL 16 only, full
feature support, all alt drivers in the matrix marked "not implemented."
Future commits that land each phase update the same matrix; the UI
surfaces the drift automatically.

## Acceptance for this commit series

```bash
# Browser
open http://localhost:3000/admin/platform/db-info
# Expected:
#   Driver: PostgreSQL
#   Version: 16.x (live from SELECT version())
#   Feature matrix table with green checkmarks for Postgres column,
#   "Not implemented" badges for the other three columns.

# Direct probe
curl -H "Cookie: dms_session=..." http://localhost:3000/api/v1/admin/platform/db-info
# Expected:
#   {"driver":"postgres","version":"PostgreSQL 16.x ...","capabilities":{...},
#    "alternate_drivers":[{"name":"mysql","status":"not_implemented",...}, ...]}
```

The future-readiness acceptance (alt-driver support shipped) is
explicitly out of scope.
