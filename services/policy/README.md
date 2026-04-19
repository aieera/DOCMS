# policy

OPA-backed authorization (ABAC), permission management, session-authed
REST for the frontend.

## Responsibilities

- Evaluate every `CheckPermission(subject, action, resource, context)`
  via a compiled Rego policy (`policy.rego`).
- Cache permission lookups + user group memberships in Redis
  (tenant-prefixed keys).
- Grant / revoke ACL rows in `permissions`; emit
  `dms.permission.{granted,revoked}.v1` via outbox.
- REST face for the web app — see
  [04b remediation](../../docs/audit/remediation/04b-frontend-contracts.md).

## API surface

gRPC (service-to-service hot path):

- `PolicyService.CheckPermission` / `BatchCheckPermission`

REST (session-authed, via `pkg/middleware.SessionAuth`):

- `GET /api/v1/permissions/{resource_type}/{resource_id}` — list ACL
- `POST /api/v1/permissions/check` — allowed?
- `POST /api/v1/permissions/{resource_type}/{resource_id}` — grant
- `DELETE /api/v1/permissions/{resource_type}/{resource_id}/{principal_id}`

## Dependencies

- **Postgres** tables: `permissions`, `groups`, `group_members`,
  `workspace_members`.
- **Redis**: per-resource + per-user permission caches, tenant-scoped
  key prefixes.
- **NATS** stream `AUTH` (publishes `dms.permission.*`).

## Configuration

`VAULTDMS_DATABASE_URL`, `_REDIS_URL`, `_NATS_URL`,
`_GRPC_PORT` (9090), `_HTTP_PORT` (8080).

## Running locally

```bash
make up
( cd services/policy && VAULTDMS_HTTP_PORT=8082 VAULTDMS_GRPC_PORT=9082 go run ./cmd/server )
```

## Testing

```bash
go test ./services/policy/...  # includes opa engine tests
```

## Deployment

`deploy/helm/vaultdms/templates/policy/` — full 6-resource set.

## Metrics

- `grpc_request_duration_seconds{method=/vaultdms.v1.PolicyService/*}`
- Custom: `rego_eval_duration_seconds` (histogram, logs slow evals > 20ms)

## Troubleshooting

**Policy evaluations slow** — the `opa_policy compiled` log runs once
on boot; if the rego file changes, the service must restart. In-flight
evals use the precompiled AST. If > 20ms, check `loadResourcePermissions`
Postgres latency — the cache should hit on hot paths.

**`column "effect" does not exist`** — pre-existing bug fixed in
[remediation 04b live run](../../docs/audit/remediation/04b-frontend-contracts.md)
— the 000001 schema has no `effect` column; the repo was rewritten to
treat capability as TEXT and effect as implicit allow.
