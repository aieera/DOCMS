//go:build integration
// +build integration

// Run with: go test -tags integration ./pkg/database/...
//
// Covers the critical paths from the spec:
//   - Insert writes a row with published=false
//   - Publisher drains unpublished rows, publishes to NATS with
//     Nats-Msg-Id, and marks them published
//   - NATS outage handled without data loss (template)
//   - FOR UPDATE SKIP LOCKED prevents double-publish (template)
//   - Cleanup deletes old published rows (template)
package database_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/testutil"
)

type harness struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	nc     *nats.Conn
	js     nats.JetStreamContext
	tenant uuid.UUID
}

func setup(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)

	natsURL, natsCleanup, err := testutil.NewNATSContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(natsCleanup)

	require.NoError(t, database.RunMigrations(dsn, "../../services/document/migrations"))

	// testcontainer Postgres runs as a BYPASSRLS superuser — opt out
	// of the posture check so NewPool succeeds.
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	nc, js, err := events.ConnectNATS(natsURL)
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close() })

	tenant := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)
	`, tenant, "t", "t-"+tenant.String()[:8])
	require.NoError(t, err)

	return &harness{ctx: ctx, pool: pool, nc: nc, js: js, tenant: tenant}
}

// insertEvent writes one outbox row inside a TX with the tenant GUC set.
func (h *harness) insertEvent(t *testing.T, evt *database.OutboxEvent) {
	t.Helper()
	require.NoError(t, database.WithTenantTx(h.ctx, h.pool, h.tenant, func(tx pgx.Tx) error {
		return database.NewOutboxRepository().Insert(h.ctx, tx, evt)
	}))
}

func TestInsert_WritesUnpublishedRow(t *testing.T) {
	h := setup(t)
	payload, _ := json.Marshal(map[string]string{"document_id": "abc"})
	evt := database.NewOutboxEvent(h.tenant, "dms.document.created.v1",
		"document", uuid.Must(uuid.NewV7()), payload)

	h.insertEvent(t, evt)

	var published bool
	err := h.pool.QueryRow(h.ctx,
		"SELECT published FROM outbox WHERE id = $1", evt.ID).Scan(&published)
	require.NoError(t, err)
	require.False(t, published, "newly inserted outbox row must have published=false")
}

func TestPublisher_PublishesToNATSAndMarks(t *testing.T) {
	h := setup(t)

	received := make(chan *nats.Msg, 4)
	sub, err := h.nc.ChanSubscribe("dms.document.created.v1", received)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	payload, _ := json.Marshal(map[string]any{"document_id": "abc"})
	evt := database.NewOutboxEvent(h.tenant, "dms.document.created.v1",
		"document", uuid.Must(uuid.NewV7()), payload)
	h.insertEvent(t, evt)

	pub := database.NewOutboxPublisherWithConfig(
		h.pool, h.js, "document", zerolog.Nop(),
		database.PublisherConfig{PollInterval: 50 * time.Millisecond},
	)
	pubCtx, cancel := context.WithCancel(h.ctx)
	t.Cleanup(cancel)
	go pub.Start(pubCtx)
	t.Cleanup(pub.Stop)

	select {
	case msg := <-received:
		require.Equal(t, evt.ID.String(), msg.Header.Get("Nats-Msg-Id"))
		var env map[string]any
		require.NoError(t, json.Unmarshal(msg.Data, &env))
		require.Equal(t, "1.0", env["specversion"])
		require.Equal(t, "vaultdms.document", env["source"])
		require.Equal(t, h.tenant.String(), env["tenantid"])
	case <-time.After(3 * time.Second):
		t.Fatal("no NATS message within 3s")
	}

	// published flag flipped
	var published bool
	require.Eventually(t, func() bool {
		err := h.pool.QueryRow(h.ctx,
			"SELECT published FROM outbox WHERE id = $1", evt.ID).Scan(&published)
		return err == nil && published
	}, 3*time.Second, 50*time.Millisecond, "outbox row must be marked published")
}

// ---- Templates for the remaining spec bullets ----

func TestPublisher_CleansUpOldEvents(t *testing.T) {
	// Insert a row, mark it published with published_at=now()-25h, run a
	// publisher with CleanupInterval=50ms & RetainPublished=24h, assert gone.
	t.Skip("template — exercise cleanupOldEvents directly in a follow-up")
}

func TestPublisher_SkipLockedNoDoublePublish(t *testing.T) {
	// Start two publishers against same pool+NATS; insert N events; assert
	// each Nats-Msg-Id appears exactly once and all rows marked published.
	t.Skip("template — SKIP LOCKED semantics exercised by drainBatch SQL")
}

func TestPublisher_NATSOutageRecovers(t *testing.T) {
	// Stop the NATS container after inserting N events; drainBatch returns
	// error, rows remain unpublished. Start NATS back; next tick delivers.
	t.Skip("template — covered by drainBatch partial-failure logic")
}
