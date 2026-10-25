// Package testutil provides test-only helpers: ephemeral containers,
// fixtures, and assertions. Import under a build tag or only from _test.go.
package testutil

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Cleanup returns a function the test should defer.
type Cleanup func()

// NewPostgresContainer starts an ephemeral Postgres with the SeDoc
// default extensions applied and returns its connection URL.
func NewPostgresContainer(ctx context.Context) (string, Cleanup, error) {
	pg, err := postgres.RunContainer(ctx,
		testcontainers.WithImage("postgres:16-alpine"),
		postgres.WithDatabase("vaultdms"),
		postgres.WithUsername("vaultdms"),
		postgres.WithPassword("devpassword"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		return "", nil, fmt.Errorf("start postgres: %w", err)
	}
	url, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = pg.Terminate(ctx)
		return "", nil, err
	}
	cleanup := func() { _ = pg.Terminate(context.Background()) }
	return url, cleanup, nil
}

// NewRedisContainer starts an ephemeral Redis.
func NewRedisContainer(ctx context.Context) (string, Cleanup, error) {
	r, err := tcredis.RunContainer(ctx, testcontainers.WithImage("redis:7-alpine"))
	if err != nil {
		return "", nil, fmt.Errorf("start redis: %w", err)
	}
	uri, err := r.ConnectionString(ctx)
	if err != nil {
		_ = r.Terminate(ctx)
		return "", nil, err
	}
	return uri, func() { _ = r.Terminate(context.Background()) }, nil
}

// NewMinIOContainer starts MinIO and returns its endpoint + credentials.
func NewMinIOContainer(ctx context.Context) (endpoint, access, secret string, cleanup Cleanup, err error) {
	req := testcontainers.ContainerRequest{
		// MinIO stopped publishing community images to Docker Hub and quay.io in
		// 2025 (minio/minio now 404s). Chainguard's build is the public source;
		// pinned by the multi-arch index digest (RELEASE.2026-10-23, amd64 +
		// arm64). Same image as docker-compose.yml and the CI service container.
		Image:        "cgr.dev/chainguard/minio@sha256:87896cb33f93ecfcca4e883c3fb2f86a0a1a600c003a104011ed30631c1869c3",
		ExposedPorts: []string{"9000/tcp"},
		Env: map[string]string{
			"MINIO_ROOT_USER":     "minioadmin",
			"MINIO_ROOT_PASSWORD": "minioadmin",
		},
		Cmd:        []string{"server", "/data"},
		WaitingFor: wait.ForHTTP("/minio/health/live").WithPort("9000/tcp").WithStartupTimeout(30 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	if err != nil {
		return "", "", "", nil, fmt.Errorf("start minio: %w", err)
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "9000")
	return fmt.Sprintf("%s:%s", host, port.Port()), "minioadmin", "minioadmin",
		func() { _ = c.Terminate(context.Background()) }, nil
}

// natsTestConfig is the nats-server config NewNATSContainer runs with — see
// the Files entry below for why the limits are explicit.
const natsTestConfig = `jetstream {
  store_dir: "/data"
  max_file_store: 1T
  max_memory_store: 1G
}
`

// NewNATSContainer starts NATS with JetStream enabled.
func NewNATSContainer(ctx context.Context) (url string, cleanup Cleanup, err error) {
	req := testcontainers.ContainerRequest{
		Image:        "nats:2.10",
		ExposedPorts: []string{"4222/tcp", "8222/tcp"},
		// Explicit JetStream limits. Without a config nats-server derives
		// max_file_store from the host's free disk (18 GB on a dev box, less
		// on a CI runner) and events.DefaultStreams reserves 5–10 GB per
		// stream (~240 GB in total), so EnsureStreams failed in CI with
		// "insufficient storage resources available". The limit is a logical
		// reservation cap, not an allocation; the tests write kilobytes.
		Files: []testcontainers.ContainerFile{{
			Reader:            strings.NewReader(natsTestConfig),
			ContainerFilePath: "/etc/nats/sedoc-test.conf",
			FileMode:          0o444,
		}},
		Cmd:        []string{"-c", "/etc/nats/sedoc-test.conf", "-m", "8222"},
		WaitingFor: wait.ForHTTP("/healthz").WithPort("8222/tcp").WithStartupTimeout(30 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start nats: %w", err)
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "4222")
	return fmt.Sprintf("nats://%s:%s", host, port.Port()),
		func() { _ = c.Terminate(context.Background()) }, nil
}
