package service

// Regression guard for the intermittent auto-logout bug: ValidateSession
// used to flatten EVERY GetByTokenHash failure into ErrUnauthorized, so a
// transient Postgres failure (pool exhaustion, restart, timeout) answered
// 401 — indistinguishable from a genuinely dead session — and the web
// client tore down a valid login. Only "no such session" is an auth
// verdict; infra failures must propagate as themselves.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/model"
	"github.com/aieera/sedoc/services/auth/internal/repository"
)

// fakeSessionRepo returns a canned error from GetByTokenHash; every other
// SessionRepository method panics via the embedded nil interface (they
// must not be reached on this path).
type fakeSessionRepo struct {
	repository.SessionRepository
	err error
}

func (f *fakeSessionRepo) GetByTokenHash(context.Context, *pgxpool.Pool, string) (*model.Session, error) {
	return nil, f.err
}

// deadRedis forces the Redis fast path to error so ValidateSession falls
// through to the repository.
func deadRedis() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond})
}

func TestValidateSession_TransientRepoErrorIsNotUnauthorized(t *testing.T) {
	transient := fmt.Errorf("auth db: %w", context.DeadlineExceeded)
	svc := &Service{
		rdb:      deadRedis(),
		sessions: &fakeSessionRepo{err: transient},
		now:      time.Now,
	}

	_, err := svc.ValidateSession(context.Background(), "some-live-token")
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, vdmserr.ErrUnauthorized) {
		t.Fatalf("transient DB failure must not become ErrUnauthorized (401); got %v", err)
	}
}

func TestValidateSession_UnknownTokenIsUnauthorized(t *testing.T) {
	svc := &Service{
		rdb:      deadRedis(),
		sessions: &fakeSessionRepo{err: vdmserr.Wrap(vdmserr.ErrNotFound, pgx.ErrNoRows)},
		now:      time.Now,
	}

	_, err := svc.ValidateSession(context.Background(), "no-such-token")
	if !errors.Is(err, vdmserr.ErrUnauthorized) {
		t.Fatalf("unknown token must stay ErrUnauthorized; got %v", err)
	}
}
