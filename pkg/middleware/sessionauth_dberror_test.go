package middleware

// Regression guard for the intermittent auto-logout bug: SessionAuth used
// to answer 401 "authentication required" for ANY database error, so a
// transient infra failure (pool exhaustion, Postgres restart, network
// blip) was indistinguishable from an expired session and the frontend
// tore down a perfectly valid login. Only "no such session" (ErrNoRows)
// is an authentication verdict; everything else must surface as 503.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// deadPool returns a pool whose every query fails with a connection
// error — the same class of failure a DB restart or exhausted pool
// produces. Port 1 on localhost refuses instantly; pgxpool connects
// lazily so construction succeeds.
func deadPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://u:p@127.0.0.1:1/db?connect_timeout=1&pool_max_conns=1")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestSessionAuth_TransientDBErrorIs503NotUnauthorized(t *testing.T) {
	mw := SessionAuth(SessionAuthConfig{Pool: deadPool(t)})

	nextCalled := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: "dms_session", Value: "some-live-token"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if nextCalled {
		t.Fatal("next handler must not run when session validation is unavailable")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("DB failure must be 503 (not an auth verdict); got %d", rec.Code)
	}
}
