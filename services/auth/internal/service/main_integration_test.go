//go:build integration

package service

import (
	"context"
	"crypto/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/auth/internal/repository"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

// One Postgres (real document + auth chains) and one Redis for the whole
// package; each test seeds its own tenant so tests never share rows. The
// older revocation suite keeps its own fixture container.
var (
	envOnce    sync.Once
	sharedEnv  *testenv.Env
	envErr     error
	envCleanup func()
)

func TestMain(m *testing.M) {
	code := m.Run()
	if envCleanup != nil {
		envCleanup()
	}
	os.Exit(code)
}

func integrationEnv(t *testing.T) *testenv.Env {
	t.Helper()
	envOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		sharedEnv, envCleanup, envErr = testenv.Start(ctx)
	})
	if envErr != nil {
		t.Fatalf("integration env: %v", envErr)
	}
	return sharedEnv
}

// newIntegrationService wires a real Service over the shared env with a
// random local KEK.
func newIntegrationService(t *testing.T, env *testenv.Env) *Service {
	t.Helper()
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	return New(Config{
		Pool:     env.Pool,
		Redis:    env.Redis,
		Users:    repository.NewUserRepo(),
		Sessions: repository.NewSessionRepo(),
		APIKeys:  repository.NewAPIKeyRepo(),
		WebAuthn: repository.NewWebAuthnRepo(),
		Outbox:   database.NewOutboxRepository(),
		LocalKEK: kek,
		Logger:   zerolog.Nop(),
	})
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// setMFAPolicy writes the tenant MFA policy row directly (superuser).
func setMFAPolicy(t *testing.T, env *testenv.Env, tenant uuid.UUID, mode string) {
	t.Helper()
	if _, err := env.Pool.Exec(context.Background(), `
		INSERT INTO tenant_mfa_policy (tenant_id, mode) VALUES ($1, $2)
		ON CONFLICT (tenant_id) DO UPDATE SET mode = EXCLUDED.mode`, tenant, mode); err != nil {
		t.Fatalf("set mfa policy: %v", err)
	}
}
