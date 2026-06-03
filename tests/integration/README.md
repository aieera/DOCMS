# Integration tests

Wave 13.1. Every `*_integration_test.go` across the repo uses the
`integration` Go build tag + `tests/integration/harness` helper.

## Running locally

```bash
docker compose -f deploy/docker-compose.integration.yml up -d

# Export the addresses the harness reads. The compose file binds
# everything on localhost with fixed ports.
export DATABASE_URL='postgres://sedoc:devpassword@localhost:5432/sedoc_test?sslmode=disable'
export REDIS_URL='localhost:6379'
export NATS_URL='nats://localhost:4222'
export OPENSEARCH_URL='http://localhost:9200'

go test -tags=integration ./...
```

Tests that don't find the env vars **skip** rather than fail, so a
plain `go test ./...` still works on a dev laptop without docker
running.

## Running in CI

The `integration` job in `.github/workflows/ci.yml` brings up the
same compose file as GitHub Actions services, injects the env
vars, runs `go test -tags=integration ./...`. Budget: 12 minutes
p95 per spec §13.1 DoD.

## Harness API

See [../../pkg/testharness/harness.go](../../pkg/testharness/harness.go) for the full
surface. At a glance:

```go
h := testharness.New(t)                            // skips if env not set
h.RunMigrations(t, "../../services/document/migrations")
tenantID := h.SeedTenant(t, "Acme Corp")
h.Pool           // *pgxpool.Pool — tenant-scoped writes via WithTenantTx
h.Redis          // redis client
h.NATS / h.JetStream
h.OpenSearchURL  // raw URL; each test wires its own client
msg := h.WaitForNATSMsg(t, "dms.document.created.v1", 5*time.Second)
```

## Fixture data

Spec §13.1 calls for a 50-PDF / 20-DOCX / 10-email / 5-image
corpus. Fixtures belong under `tests/integration/fixtures/` (not
in this initial drop — each test should commit the minimum it
needs). A shared fixture loader in `harness.Fixtures(name)` can
follow once there's enough reuse to justify it.

## Writing a new integration test

```go
//go:build integration
// +build integration

package whatever_test

import (
    "testing"
    "time"
    "github.com/stretchr/testify/require"
    "github.com/aieera/sedoc/pkg/testharness"
)

func TestFoo_RoundTrip(t *testing.T) {
    h := testharness.New(t)
    h.RunMigrations(t, "../../services/document/migrations")
    tid := h.SeedTenant(t, "T")
    // real write via WithTenantTx …
    msg := h.WaitForNATSMsg(t, "dms.document.created.v1", 3*time.Second)
    require.NotNil(t, msg)
}
```

Integration tests are **slow** — don't add them for something a
unit test can cover. Reserve them for genuine cross-service or
real-transport invariants (outbox → NATS → consumer, RLS under a
real pg role, OpenSearch index shape).
