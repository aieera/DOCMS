//go:build integration

package handler

import (
	"context"
	"crypto/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/auth/internal/repository"
	"github.com/aieera/sedoc/services/auth/internal/service"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

// One Postgres (real document + auth chains) and one Redis for the whole
// package; each test seeds its own tenant so tests never share rows.
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

// newIntegrationHandler wires a real Service + Handler over the shared env.
func newIntegrationHandler(t *testing.T, env *testenv.Env) (*Handler, *service.Service) {
	t.Helper()
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	svc := service.New(service.Config{
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
	h := New(Config{Service: svc, Logger: zerolog.Nop()})
	return h, svc
}
