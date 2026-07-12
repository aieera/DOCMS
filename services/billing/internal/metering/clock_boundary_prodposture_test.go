//go:build integration && prodposture
// +build integration,prodposture

// Metering clock-boundary correctness (money-path suite completion).
//
// TestProdPosture_BillingMeteringCronCycles proves the cron runs clean and
// is idempotent WITHIN one wall-clock hour. This test drives the REAL
// collect loop across simulated day boundaries (via an injected clock) to
// pin the period bucketing that the existing test can't reach:
//
//   - usage_records.period_start is a DATE, so although collect() computes
//     now.Truncate(time.Hour), the (tenant_id, period_start) upsert key
//     collapses to a DAILY bucket. Re-metering ANY hour of the same day
//     UPDATES that day's row (last read wins) — it never duplicates.
//   - Crossing midnight opens a NEW row; each day gets exactly one row per
//     tenant regardless of how many hourly cycles ran that day.
//
// This is the "metering cron proven correct over simulated cycles" DoD
// item, run under the enforced NOBYPASSRLS posture. meteringFixtureDDL is
// shared with prod_posture_metering_test.go (same package).
package metering

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/billing/internal/repository"
)

func TestProdPosture_BillingMeteringClockBoundary(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, meteringFixtureDDL)
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(db.SuperDSN, "../../migrations"))
	_, err = db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES
		($1,'A','cb-a'), ($2,'B','cb-b')`, tenantA, tenantB)
	require.NoError(t, err)

	const gib = int64(1 << 30)
	addBlob := func(tenant uuid.UUID, n int64) {
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `INSERT INTO content_blobs (tenant_id, size_bytes) VALUES ($1, $2)`, tenant, n)
			return e
		}))
	}
	addBlob(tenantA, gib) // A starts at 1 GiB
	addBlob(tenantB, gib) // B stays at 1 GiB

	var logBuf bytes.Buffer
	meter := New(repository.New(db.App), zerolog.New(&logBuf))

	// Injected clock: collect() buckets on nowFn().Truncate(hour) → DATE.
	var clock time.Time
	meter.nowFn = func() time.Time { return clock }

	// Day 1, morning cycle.
	clock = time.Date(2026, 7, 1, 8, 30, 0, 0, time.UTC)
	meter.collect(ctx)

	// Day 1, afternoon cycle — a DIFFERENT hour, same day. A grew by 1 GiB
	// between cycles; this must UPDATE day 1's row (→ 2 GiB), not add one.
	addBlob(tenantA, gib) // A now 2 GiB
	clock = time.Date(2026, 7, 1, 15, 0, 0, 0, time.UTC)
	meter.collect(ctx)

	// Day 2.
	clock = time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	meter.collect(ctx)

	// Day 3, last-minute-of-day boundary (23:59) — still bucketed to 07-03.
	clock = time.Date(2026, 7, 3, 23, 59, 0, 0, time.UTC)
	meter.collect(ctx)

	require.NotContains(t, logBuf.String(), `"level":"error"`,
		"metering cron must run clean across the boundaries; log:\n%s", logBuf.String())

	type urow struct {
		day     string
		storage float64
	}
	rowsFor := func(tenant uuid.UUID) []urow {
		var out []urow
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			rr, e := tx.Query(ctx, `SELECT period_start, storage_gb FROM usage_records ORDER BY period_start`)
			if e != nil {
				return e
			}
			defer rr.Close()
			for rr.Next() {
				var ps time.Time
				var u urow
				if e := rr.Scan(&ps, &u.storage); e != nil {
					return e
				}
				u.day = ps.Format("2006-01-02")
				out = append(out, u)
			}
			return rr.Err()
		}))
		return out
	}

	// 4 collects across 3 calendar days → exactly 3 rows per tenant (DATE
	// bucket), NOT 4. The two same-day cycles coalesced.
	a := rowsFor(tenantA)
	require.Len(t, a, 3, "one usage row per calendar day, not per collect (period_start is DATE): %+v", a)
	require.Equal(t, []string{"2026-07-01", "2026-07-02", "2026-07-03"},
		[]string{a[0].day, a[1].day, a[2].day}, "period_start must bucket to the collect day")
	// The same-day re-meter refreshed day 1's row to A's current 2 GiB.
	require.InDelta(t, 2.0, a[0].storage, 0.01,
		"re-metering the same day updates the row (last read wins), never duplicates")
	for _, r := range a {
		require.InDelta(t, 2.0, r.storage, 0.01, "every A day reflects its current 2 GiB")
	}

	// B never changed and never leaks A's growth.
	b := rowsFor(tenantB)
	require.Len(t, b, 3, "tenant B also gets one row per day")
	for _, r := range b {
		require.InDelta(t, 1.0, r.storage, 0.01, "B stays at 1 GiB — no cross-tenant leak")
	}
}
