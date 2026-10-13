//go:build integration
// +build integration

// Shared harness for the service-layer integration tests: one ephemeral
// Postgres per package run with the workflow tables (schema-of-record DDL
// from document 000001 + 000005 + 000062, FKs and RLS dropped — the full
// document chain does not apply standalone and these tests prove service
// logic, not tenant isolation), a real repository, and a recording fake for
// the Temporal client.
package service

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"go.temporal.io/sdk/client"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/workflow/internal/model"
	"github.com/aieera/sedoc/services/workflow/internal/repository"
)

const workflowTablesDDL = `
CREATE TABLE documents (
	tenant_id UUID NOT NULL,
	id        UUID NOT NULL DEFAULT gen_random_uuid(),
	title     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE workflow_definitions (
	tenant_id    UUID NOT NULL,
	id           UUID NOT NULL DEFAULT gen_random_uuid(),
	name         TEXT NOT NULL,
	description  TEXT,
	definition   JSONB NOT NULL,
	version      INT NOT NULL DEFAULT 1,
	is_active    BOOLEAN NOT NULL DEFAULT true,
	created_by   UUID,
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	visibility   TEXT NOT NULL DEFAULT 'shared' CHECK (visibility IN ('shared', 'private')),
	owner_id     UUID,
	PRIMARY KEY (tenant_id, id),
	UNIQUE (tenant_id, name, version)
);
CREATE TABLE workflow_grants (
	tenant_id     UUID        NOT NULL,
	id            UUID        NOT NULL DEFAULT gen_random_uuid(),
	workflow_id   UUID        NOT NULL,
	grantee_type  TEXT        NOT NULL CHECK (grantee_type IN ('user', 'group')),
	grantee_id    UUID        NOT NULL,
	granted_by    UUID,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, id),
	UNIQUE (tenant_id, workflow_id, grantee_type, grantee_id)
);
CREATE TABLE workflow_instances (
	tenant_id             UUID NOT NULL,
	id                    UUID NOT NULL DEFAULT gen_random_uuid(),
	definition_id         UUID NOT NULL,
	document_id           UUID,
	status                TEXT NOT NULL DEFAULT 'pending'
	                           CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
	current_step_id       TEXT,
	temporal_workflow_id  TEXT,
	started_by            UUID,
	started_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
	completed_at          TIMESTAMPTZ,
	result                JSONB,
	error_message         TEXT,
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE workflow_tasks (
	tenant_id      UUID NOT NULL,
	id             UUID NOT NULL DEFAULT gen_random_uuid(),
	instance_id    UUID NOT NULL,
	step_id        TEXT,
	step_name      TEXT NOT NULL,
	assignee_id    UUID,
	status         TEXT NOT NULL DEFAULT 'pending'
	                    CHECK (status IN ('pending', 'in_progress', 'completed',
	                                      'rejected', 'delegated', 'escalated', 'skipped')),
	due_at         TIMESTAMPTZ,
	completed_at   TIMESTAMPTZ,
	completed_by   UUID,
	outcome        TEXT,
	notes          TEXT,
	delegated_to   UUID,
	document_id    UUID,
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, id)
);
`

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(runWithPostgres(m))
}

func runWithPostgres(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres container: %v\n", err)
		return 1
	}
	defer cleanup()
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pool: %v\n", err)
		return 1
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, workflowTablesDDL); err != nil {
		fmt.Fprintf(os.Stderr, "ddl: %v\n", err)
		return 1
	}
	testPool = pool
	return m.Run()
}

// fakeTemporal records the signals and cancels the service sends. Every
// other client.Client method panics via the nil embedded interface, which
// makes an unexpected Temporal call fail the test loudly.
type fakeTemporal struct {
	client.Client
	mu      sync.Mutex
	signals []model.StepSignal
	cancels int
}

func (f *fakeTemporal) SignalWorkflow(_ context.Context, _, _, _ string, arg interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := arg.(model.StepSignal); ok {
		f.signals = append(f.signals, s)
	}
	return nil
}

func (f *fakeTemporal) CancelWorkflow(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	return nil
}

func newTestService(t *testing.T) (*Service, *fakeTemporal) {
	t.Helper()
	ft := &fakeTemporal{}
	return New(Config{Repo: repository.New(testPool), Temporal: ft, Logger: zerolog.Nop()}), ft
}

// as returns a request context for a session user of tenant with role.
func as(tenant, user uuid.UUID, role string) context.Context {
	return auth.WithUser(context.Background(), auth.UserInfo{ID: user, TenantID: tenant, Role: role})
}

func newIDs() (tenant, a, b uuid.UUID) {
	return uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
}
