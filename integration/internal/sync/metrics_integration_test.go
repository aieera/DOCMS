//go:build integration
// +build integration

// Verifies the worker metrics endpoint assembles DB-derived backlog/DLQ +
// runtime counters + shared-limiter state (Prompt 6).
//
// Run with: go test -tags integration ./integration/internal/sync/...
package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

func TestMetricsHandler_AssemblesCountsAndLimiter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := store.New(pool)

	// Seed one pending (backlog) and one failed (DLQ) sync_log row.
	_, _, err = st.InsertEvent(ctx, "e1", "customer.created", "C1", []byte(`{}`))
	require.NoError(t, err)
	failID, _, err := st.InsertEvent(ctx, "e2", "customer.created", "C2", []byte(`{}`))
	require.NoError(t, err)
	require.NoError(t, st.MarkFailed(ctx, failID, "boom", "corr-1"))

	m := NewMetrics()
	m.OnProcessed()
	m.OnProcessed()
	lim := sedoc.NewLimiter(10, 5)

	rec := httptest.NewRecorder()
	MetricsHandler(m, lim, st)(rec, httptest.NewRequest(http.MethodGet, "/metrics/sync", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var out struct {
		EventsPerMin   int64 `json:"events_per_min"`
		ProcessedTotal int64 `json:"processed_total"`
		Backlog        int64 `json:"backlog"`
		DLQ            int64 `json:"dlq"`
		Limiter        struct {
			LimitPerSec float64 `json:"limit_per_sec"`
			Burst       int     `json:"burst"`
		} `json:"limiter"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, int64(1), out.Backlog, "one pending row")
	require.Equal(t, int64(1), out.DLQ, "one failed row")
	require.Equal(t, int64(2), out.ProcessedTotal)
	require.Equal(t, int64(2), out.EventsPerMin)
	require.Equal(t, float64(10), out.Limiter.LimitPerSec)
	require.Equal(t, 5, out.Limiter.Burst)
}
