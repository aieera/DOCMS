package testharness

// Pre-wired containerBoot adapters that wrap pkg/testutil so tests
// get auto-bootstrapped services without repeating adapter glue.
//
// Usage:
//
//	h := testharness.NewWithContainers(t,
//	    testharness.ContainerOptions{Postgres: true, Redis: true},
//	    testharness.BootPostgres, testharness.BootRedis, nil,
//	)
//
// Each boot function matches the containerBoot signature and
// delegates to the matching pkg/testutil helper. testutil is
// imported here rather than under a build tag because testharness
// already has its own "skip if env vars are absent" skip path —
// callers that don't want testcontainers in their binary can use
// testharness.New instead of NewWithContainers.

import (
	"context"

	"github.com/vaultdms/vaultdms/pkg/testutil"
)

// BootPostgres starts an ephemeral Postgres via testcontainers.
func BootPostgres(ctx context.Context) (string, func(), error) {
	url, cleanup, err := testutil.NewPostgresContainer(ctx)
	return url, func() { cleanup() }, err
}

// BootRedis starts an ephemeral Redis via testcontainers.
func BootRedis(ctx context.Context) (string, func(), error) {
	url, cleanup, err := testutil.NewRedisContainer(ctx)
	return url, func() { cleanup() }, err
}

// BootNATS starts an ephemeral NATS (JetStream enabled).
func BootNATS(ctx context.Context) (string, func(), error) {
	url, cleanup, err := testutil.NewNATSContainer(ctx)
	return url, func() { cleanup() }, err
}
