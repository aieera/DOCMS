package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// TestReadyz_NoDriverErrorInBody pins R-9: Kong routes GET /api/v1/health
// anonymously to auth's /readyz, so the body must never carry the raw
// Postgres/Redis/S3 driver error (host, port, user, database). Each
// failing dependency reports just "fail"; the detail goes to the log.
func TestReadyz_NoDriverErrorInBody(t *testing.T) {
	pool, err := pgxpool.New(context.Background(),
		"postgres://leakyuser:leakypass@127.0.0.1:1/leakydb?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })

	s := NewServerWithMeta("auth", "test", pool, rdb, nil, nil)
	rr := httptest.NewRecorder()
	s.handleReady(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", rr.Code)
	}
	body := rr.Body.String()
	for _, leak := range []string{"leakyuser", "leakydb", "127.0.0.1", ":1", "dial", "connect"} {
		if strings.Contains(body, leak) {
			t.Errorf("readyz body leaks driver detail %q: %s", leak, body)
		}
	}
	var out map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if out["postgres"] != "fail" || out["redis"] != "fail" {
		t.Errorf("want postgres=fail redis=fail, got %v", out)
	}
}
