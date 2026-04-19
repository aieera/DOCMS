// Package testharness is the Wave 13.1 integration-test helper.
//
// Every `*_integration_test.go` across the repo (build tag
// `integration`) constructs a *Harness that exposes pre-wired
// pgxpool / redis / nats / opensearch clients pointed at the
// docker-compose services the CI `integration` job stands up.
//
// Design invariants:
//
//   - One shared set of services per CI job run (fast). Per-test
//     isolation comes from a tenant UUID minted at NewHarness and
//     RLS-wrapped writes — tests can run in parallel.
//   - Every service address comes from env (POSTGRES_URL, REDIS_URL,
//     NATS_URL, OPENSEARCH_URL). The compose file + CI workflow
//     set those; local runs read the same env.
//   - No magic. Tests that need a policy-service client dial it
//     themselves — the harness stops at the storage-layer primitives
//     because wiring every service's gRPC client here would couple
//     the harness to 14 implementations.
//
// A typical integration test:
//
//	//go:build integration
//	package foo
//
//	func TestFoo(t *testing.T) {
//	    h := harness.New(t)
//	    h.RunMigrations(t, "../../services/document/migrations")
//	    tenantID := h.SeedTenant(t, "Acme Corp")
//	    // … real calls, real NATS publishes, real DB inserts.
//	}
//
// If an env var is missing, New calls t.Skip — so unit-test runs
// that don't provide the infrastructure don't fail.
package testharness

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

// Harness bundles the connections an integration test usually needs.
// Zero-value harness is NOT usable — always construct via New.
type Harness struct {
	Ctx       context.Context
	Pool      *pgxpool.Pool
	Redis     *redis.Client
	NATS      *nats.Conn
	JetStream nats.JetStreamContext

	// OpenSearchURL is exposed raw (not a client) because each test
	// either talks to it directly with net/http or wires its own
	// client — the search service's client isn't importable from
	// this harness without an import cycle risk.
	OpenSearchURL string

	cleanup []func()
}

// New constructs a Harness. Skips the test if any required env var
// is unset — CI provides them; `go test ./...` from a dev laptop
// without docker-compose running does not.
func New(t *testing.T) *Harness {
	t.Helper()
	required := []string{"DATABASE_URL", "REDIS_URL", "NATS_URL"}
	for _, k := range required {
		if os.Getenv(k) == "" {
			t.Skipf("integration harness: %s not set; run via docker-compose -f deploy/docker-compose.integration.yml up", k)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &Harness{Ctx: ctx}
	h.cleanup = append(h.cleanup, cancel)

	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("pgx pool: %v", err)
	}
	h.Pool = pool
	h.cleanup = append(h.cleanup, func() { pool.Close() })

	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("REDIS_URL")})
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}
	h.Redis = rdb
	h.cleanup = append(h.cleanup, func() { _ = rdb.Close() })

	nc, err := nats.Connect(os.Getenv("NATS_URL"))
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	h.NATS = nc
	h.cleanup = append(h.cleanup, nc.Close)

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	h.JetStream = js

	if osURL := os.Getenv("OPENSEARCH_URL"); osURL != "" {
		h.OpenSearchURL = osURL
		// Best-effort ping; skip if OpenSearch is intentionally not
		// part of this test matrix.
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, osURL, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}

	t.Cleanup(h.Close)
	return h
}

// Close releases every resource. Invoked automatically via
// t.Cleanup; callers can invoke directly for nested harnesses.
func (h *Harness) Close() {
	for i := len(h.cleanup) - 1; i >= 0; i-- {
		h.cleanup[i]()
	}
}

// RunMigrations executes every `NNNN_*.up.sql` under `migrationsDir`
// in ascending order against the test database. No schema_migrations
// tracking — assumes a fresh DB per CI run.
func (h *Harness) RunMigrations(t *testing.T, migrationsDir string) {
	t.Helper()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var ups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	sort.Strings(ups)

	for _, name := range ups {
		body, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := h.Pool.Exec(h.Ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}

// SeedTenant inserts an organizations row and returns its UUID.
// All subsequent test writes RLS-scope against this id. Tests are
// responsible for cleaning up via SQL DELETE on teardown; this
// harness intentionally doesn't track inserted rows.
func (h *Harness) SeedTenant(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id, _ := uuid.NewV7()
	_, err := h.Pool.Exec(h.Ctx,
		`INSERT INTO organizations (id, name, region_pin, created_at) VALUES ($1, $2, 'us-east-1', now())`,
		id, fmt.Sprintf("%s-%s", name, id.String()[:8]),
	)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id
}

// WaitForNATSMsg blocks up to `timeout` for one message on `subject`.
// Common in integration tests that publish via outbox and want to
// assert the message landed in the expected stream.
func (h *Harness) WaitForNATSMsg(t *testing.T, subject string, timeout time.Duration) *nats.Msg {
	t.Helper()
	sub, err := h.NATS.SubscribeSync(subject)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	msg, err := sub.NextMsg(timeout)
	if err != nil {
		t.Fatalf("no message on %s within %s: %v", subject, timeout, err)
	}
	return msg
}
