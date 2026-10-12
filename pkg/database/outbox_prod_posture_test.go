//go:build integration && prodposture
// +build integration,prodposture

// C-4 (2026-10-12 review) / DEC-6 — the shared OutboxPublisher drained the
// FORCE-RLS outbox with a plain pool transaction and no tenant GUC, so
// under the prod NOBYPASSRLS dms_app role it saw zero rows forever: no
// search indexing, audit, notifications, webhooks or AI post-processing,
// while inserts kept succeeding. These tests run the real publisher as
// dms_app against the real document chain and require it to publish,
// dead-letter, drain rows queued before migration 000104, and clean up
// published rows.
package database_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

const (
	drainSubject   = "dms.draintest.created.v1"
	unboundSubject = "dms.draintest-unbound.created.v1" // no stream binds it
)

// newDrainPostureDB returns a prod-posture DB whose document chain is
// migrated up to `to` (0 = all). The dms_app pool is created on the
// empty DB first, then the schema is applied as the owner.
func newDrainPostureDB(ctx context.Context, t *testing.T, to uint) *testutil.ProdPostureDB {
	t.Helper()
	db := testutil.NewProdPostureDB(ctx, t, "")
	if to == 0 {
		require.NoError(t, applyService(t, db.SuperDSN, "document"))
	} else {
		require.NoError(t, applyServiceTo(t, db.SuperDSN, "document", to))
	}
	grantAppRole(ctx, t, db.Super)
	return db
}

func insertAsApp(ctx context.Context, t *testing.T, db *testutil.ProdPostureDB, tenant uuid.UUID, subject string) uuid.UUID {
	t.Helper()
	evt := database.NewOutboxEvent(tenant, subject, "document", uuid.Must(uuid.NewV7()),
		json.RawMessage(`{"k":"v"}`))
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		return database.NewOutboxRepository().Insert(ctx, tx, evt)
	}))
	return evt.ID
}

// startPublisher runs the real publisher on the dms_app pool.
func startPublisher(ctx context.Context, t *testing.T, db *testutil.ProdPostureDB, js nats.JetStreamContext, service string, cfg database.PublisherConfig) {
	t.Helper()
	pub := database.NewOutboxPublisherWithConfig(db.App, js, service, zerolog.Nop(), cfg)
	pctx, cancel := context.WithCancel(ctx)
	go pub.Start(pctx)
	t.Cleanup(func() { cancel(); pub.Stop() })
}

func publishedFlag(ctx context.Context, db *testutil.ProdPostureDB, id uuid.UUID) bool {
	var published bool
	if err := db.Super.QueryRow(ctx, `SELECT published FROM outbox WHERE id = $1`, id).Scan(&published); err != nil {
		return false
	}
	return published
}

func gaugeValue(t *testing.T, name, service string) (float64, bool) {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "service" && l.GetValue() == service {
					return m.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

func TestProdPosture_OutboxDrainPublishes(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)

	db := newDrainPostureDB(ctx, t, 0)
	_, js := newTestJetStream(ctx, t, "DRAINTEST", "dms.draintest.>")

	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	registerTenants(ctx, t, db.Super, tenantA, tenantB)

	goodA := insertAsApp(ctx, t, db, tenantA, drainSubject)
	goodB := insertAsApp(ctx, t, db, tenantB, drainSubject)
	bad := insertAsApp(ctx, t, db, tenantA, unboundSubject)

	startPublisher(ctx, t, db, js, "drainposture", database.PublisherConfig{
		PollInterval: 20 * time.Millisecond,
		MaxAttempts:  2,
		BaseBackoff:  10 * time.Millisecond,
	})

	require.Eventually(t, func() bool {
		var dlq int
		_ = db.Super.QueryRow(ctx, `SELECT count(*) FROM outbox_dlq WHERE id = $1`, bad).Scan(&dlq)
		return publishedFlag(ctx, db, goodA) && publishedFlag(ctx, db, goodB) && dlq == 1
	}, 30*time.Second, 50*time.Millisecond,
		"as dms_app the drain must publish both tenants' rows and dead-letter the unbound one")

	var stillActive int
	require.NoError(t, db.Super.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE id = $1`, bad).Scan(&stillActive))
	require.Zero(t, stillActive, "the dead-lettered row must leave the active outbox")

	si, err := js.StreamInfo("DRAINTEST")
	require.NoError(t, err)
	require.Equal(t, uint64(2), si.State.Msgs, "both tenants' events must reach JetStream")
	seen := map[string]bool{}
	for seq := uint64(1); seq <= 2; seq++ {
		m, err := js.GetMsg("DRAINTEST", seq)
		require.NoError(t, err)
		var env map[string]any
		require.NoError(t, json.Unmarshal(m.Data, &env))
		seen[env["tenantid"].(string)] = true
	}
	require.Equal(t, map[string]bool{tenantA.String(): true, tenantB.String(): true}, seen)

	// The drain's metrics read through the definer, not the RLS-filtered
	// raw pool: the dead-lettered row must show up in the depth gauge.
	require.Eventually(t, func() bool {
		v, ok := gaugeValue(t, "outbox_dlq_depth", "drainposture")
		return ok && v == 1
	}, 10*time.Second, 50*time.Millisecond, "outbox_dlq_depth must count the dead-lettered row")
}

func TestProdPosture_OutboxDrainPublishesPreexistingRows(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)

	// Rows queued by the pre-000104 code: schema at 000103, rows written
	// the way the old producers wrote them (one fresh, one already retried
	// and due again).
	db := newDrainPostureDB(ctx, t, 103)
	tenant := uuid.Must(uuid.NewV7())
	registerTenants(ctx, t, db.Super, tenant)
	fresh, retried := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err := db.Super.Exec(ctx, `
		INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload, created_at)
		VALUES ($1, $2, $3, 'document', gen_random_uuid(), '{"k":"v"}', now() - interval '10 minutes')`,
		fresh, tenant, drainSubject)
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, `
		INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload, created_at,
		                    attempts, last_error, next_attempt_at)
		VALUES ($1, $2, $3, 'document', gen_random_uuid(), '{"k":"v"}', now() - interval '9 minutes',
		        1, 'nats: timeout', now() - interval '1 minute')`,
		retried, tenant, drainSubject)
	require.NoError(t, err)

	// Upgrade to the current chain (000104+), then drain as dms_app.
	require.NoError(t, applyService(t, db.SuperDSN, "document"))
	grantAppRole(ctx, t, db.Super)
	_, js := newTestJetStream(ctx, t, "DRAINTEST", "dms.draintest.>")

	startPublisher(ctx, t, db, js, "drainposture-legacy", database.PublisherConfig{
		PollInterval: 20 * time.Millisecond,
	})

	require.Eventually(t, func() bool {
		return publishedFlag(ctx, db, fresh) && publishedFlag(ctx, db, retried)
	}, 30*time.Second, 50*time.Millisecond, "rows queued before 000104 must still drain")

	si, err := js.StreamInfo("DRAINTEST")
	require.NoError(t, err)
	require.Equal(t, uint64(2), si.State.Msgs)
}

func TestProdPosture_OutboxCleanupDeletesPublished(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)

	db := newDrainPostureDB(ctx, t, 0)
	_, js := newTestJetStream(ctx, t, "DRAINTEST", "dms.draintest.>")
	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	registerTenants(ctx, t, db.Super, tenantA, tenantB)

	insert := func(tenant uuid.UUID, published bool, publishedAgo time.Duration) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		var publishedAt any
		if published {
			publishedAt = time.Now().Add(-publishedAgo)
		}
		_, err := db.Super.Exec(ctx, `
			INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload,
			                    published, published_at)
			VALUES ($1, $2, $3, 'document', gen_random_uuid(), '{"k":"v"}', $4, $5)`,
			id, tenant, unboundSubject, published, publishedAt)
		require.NoError(t, err)
		return id
	}
	oldA := insert(tenantA, true, 48*time.Hour)
	oldB := insert(tenantB, true, 48*time.Hour)
	recent := insert(tenantA, true, time.Hour)
	pending := insert(tenantB, false, 0)

	// Drain effectively off (hour-long poll); cleanup every 50ms.
	startPublisher(ctx, t, db, js, "drainposture-cleanup", database.PublisherConfig{
		PollInterval:    time.Hour,
		CleanupInterval: 50 * time.Millisecond,
		RetainPublished: 24 * time.Hour,
	})

	exists := func(id uuid.UUID) bool {
		var n int
		_ = db.Super.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE id = $1`, id).Scan(&n)
		return n == 1
	}
	require.Eventually(t, func() bool { return !exists(oldA) && !exists(oldB) },
		30*time.Second, 50*time.Millisecond,
		"as dms_app the cleanup must delete every tenant's expired published rows")
	require.True(t, exists(recent), "published rows inside the retention window must stay")
	require.True(t, exists(pending), "unpublished rows must never be cleaned up")
}
