// Package testutil provides test-only helpers: ephemeral containers,
// fixtures, and assertions. Import under a build tag or only from _test.go.
package testutil

import (
	"context"
	"fmt"
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
		Image:        "minio/minio:RELEASE.2024-02-17T01-15-57Z",
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

// NewNATSContainer starts NATS with JetStream enabled.
func NewNATSContainer(ctx context.Context) (url string, cleanup Cleanup, err error) {
	req := testcontainers.ContainerRequest{
		Image:        "nats:2.10",
		ExposedPorts: []string{"4222/tcp", "8222/tcp"},
		Cmd:          []string{"-js", "-m", "8222"},
		WaitingFor:   wait.ForHTTP("/healthz").WithPort("8222/tcp").WithStartupTimeout(30 * time.Second),
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
