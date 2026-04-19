# Per-service database migrations

**Blueprint §:** 4.1 · **Status:** enforced for every service since §4.1 / A5

Every VaultDMS service owns its own schema-migrations bookkeeping table.
Touching service **X**'s migrations must not advance or regress the
schema version recorded for service **Y**.

## The rule

Service `<name>` records applied migrations in
`<name>_schema_migrations` — e.g. `audit_schema_migrations`,
`document_schema_migrations`. The default `schema_migrations` table
shipped by `golang-migrate` is **never** used on its own, because a
single global table means version 2 of service A overwrites the
"current version" read by service B.

## How it's enforced

### At the Makefile

`make migrate-up SERVICE=x` and `make migrate-down SERVICE=x` pass
`x-migrations-table=x_schema_migrations` as a URL query parameter to the
`migrate` CLI. No change to per-service migration files is required.

```bash
make migrate-up   SERVICE=audit    # uses audit_schema_migrations
make migrate-up   SERVICE=connector # uses connector_schema_migrations
make migrate-down SERVICE=audit    # rolls back one, still in audit_schema_migrations
```

### At service runtime

Services that auto-migrate on boot call the helper in
[pkg/database/migrate.go](../../pkg/database/migrate.go):

```go
if err := database.RunServiceMigrations(cfg.DatabaseURL, "./migrations", "audit"); err != nil {
    log.Fatal().Err(err).Msg("migrate")
}
```

`RunServiceMigrations` rewrites the URL with
`x-migrations-table=<service>_schema_migrations`, keeping the runtime and
the Makefile on the same bookkeeping table.

### At test time

`pkg/database/migrate_test.go` pins the URL-rewriting behaviour. A
future refactor that accidentally drops the query parameter fails the
`subjects-coverage` and `test` jobs together — the collision bug can't
come back silently.

## Worked example — adding a new migration to the audit service

```bash
# 1. New migration file.
make migrate-create SERVICE=audit NAME=add_retention_column
#    writes services/audit/migrations/NNNNNN_add_retention_column.{up,down}.sql

# 2. Edit both files. Up migrations must be idempotent (IF NOT EXISTS /
#    information_schema guards — see services/audit/migrations/000001 for
#    the pattern).

# 3. Apply locally.
make migrate-up SERVICE=audit
#    INSERTs (filename, created_at) into audit_schema_migrations.

# 4. Confirm the table exists and no other service's version moved.
psql $DATABASE_URL -c '\dt *_schema_migrations'
psql $DATABASE_URL -c 'SELECT * FROM audit_schema_migrations ORDER BY version DESC LIMIT 1;'

# 5. Rollback works in the same table.
make migrate-down SERVICE=audit
```

## Migration ownership map

| Bookkeeping table              | Owning service | Migrations dir               |
|--------------------------------|----------------|------------------------------|
| `audit_schema_migrations`      | audit          | services/audit/migrations    |
| `auth_schema_migrations`       | auth           | services/auth/migrations     |
| `billing_schema_migrations`    | billing        | services/billing/migrations  |
| `connector_schema_migrations`  | connector      | services/connector/migrations |
| `document_schema_migrations`   | document       | services/document/migrations |
| `notification_schema_migrations` | notification | services/notification/migrations |
| `policy_schema_migrations`     | policy         | services/policy/migrations   |
| `search_schema_migrations`     | search         | services/search/migrations   |
| `signature_schema_migrations`  | signature      | services/signature/migrations |
| `storage_schema_migrations`    | storage        | services/storage/migrations  |
| `workflow_schema_migrations`   | workflow       | services/workflow/migrations |

The document service owns the shared baseline schema (tenants, users,
audit_events, webhook_subscriptions, …) — its `000001_initial_schema`
creates the tables every other service reads or writes. Per-service
migrations are strictly additive (ADD COLUMN IF NOT EXISTS, CREATE
TABLE IF NOT EXISTS, CREATE INDEX IF NOT EXISTS) so the order of
application across services does not matter. Document's migrations run
first in CI; everything else runs in any order after.

## Anti-patterns to reject at code review

- ❌ `CREATE TABLE x (...)` without `IF NOT EXISTS` — breaks a re-run
  after partial failure.
- ❌ Modifying another service's migrations dir from this service's PR.
- ❌ Adding `schema_migrations` (no prefix) anywhere in a migrate URL.
- ❌ Bumping the golang-migrate version without regenerating
  `pkg/database/migrate_test.go` expectations.
