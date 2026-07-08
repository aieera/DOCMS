//go:build integration
// +build integration

// Wave 0.2 — the outbox drain must NOT wedge on an unbound-subject row.
// Before the fix, drainBatch broke on the first publish failure, so a
// single row whose event_type had no bound JetStream stream ("no
// responders") blocked every event behind it forever. This pins the new
// behavior: the bad row is retried, then dead-lettered to outbox_dlq,
// while the rows behind it publish normally and the drain keeps advancing.
package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// outboxSchemaDDL is the outbox + outbox_dlq schema (document 000001 +
// migration 000094), created verbatim so the test doesn't run the full
// document chain (broken on a clean DB at 000021, a separate blocker).
const outboxSchemaDDL = `
CREATE TABLE outbox (
	id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id       UUID NOT NULL,
	event_type      TEXT NOT NULL,
	aggregate_type  TEXT NOT NULL,
	aggregate_id    UUID NOT NULL,
	payload         JSONB NOT NULL,
	published       BOOLEAN NOT NULL DEFAULT false,
	created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
	published_at    TIMESTAMPTZ,
	actor_id        UUID,
	actor_name      TEXT,
	ip_address      INET,
	user_agent      TEXT,
	attempts        INT NOT NULL DEFAULT 0,
	last_error      TEXT,
	next_attempt_at TIMESTAMPTZ
);
CREATE TABLE outbox_dlq (
	id               UUID PRIMARY KEY,
	tenant_id        UUID NOT NULL,
	event_type       TEXT NOT NULL,
	aggregate_type   TEXT NOT NULL,
	aggregate_id     UUID NOT NULL,
	payload          JSONB NOT NULL,
	created_at       TIMESTAMPTZ NOT NULL,
	actor_id         UUID,
	actor_name       TEXT,
	ip_address       INET,
	user_agent       TEXT,
	attempts         INT NOT NULL,
	last_error       TEXT,
	dead_lettered_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

func TestOutboxDrain_UnboundSubjectDoesNotWedge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)
	natsURL, natsCleanup, err := testutil.NewNATSContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(natsCleanup)

	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, outboxSchemaDDL)
	require.NoError(t, err)

	// Connect to JetStream directly — events.ConnectNATS provisions the
	// full DefaultStreams topology (~240GB reservations, issue #84), which
	// this test neither needs nor can afford; it binds only WEDGETEST.
	nc, err := nats.Connect(natsURL)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	// Bind a stream for the GOOD subject only — a small manual stream
	// (not DefaultStreams, which reserves ~240GB, issue #84). The BAD
	// subject stays unbound so its publish returns "no responders".
	const goodSubject = "dms.wedgetest.good.v1"
	const badSubject = "dms.wedgetest.unbound.v1"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "WEDGETEST",
		Subjects: []string{"dms.wedgetest.good.>"},
		Storage:  nats.FileStorage,
		MaxBytes: 16 * 1024 * 1024,
	})
	require.NoError(t, err)

	tenant := uuid.Must(uuid.NewV7())
	insert := func(subject string, at time.Time) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		_, e := pool.Exec(ctx, `
			INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload, created_at)
			VALUES ($1, $2, $3, 'document', $4, $5::jsonb, $6)`,
			id, tenant, subject, uuid.Must(uuid.NewV7()), `{"k":"v"}`, at)
		require.NoError(t, e)
		return id
	}
	t0 := time.Now().Add(-time.Minute)
	good1 := insert(goodSubject, t0)
	bad := insert(badSubject, t0.Add(time.Second))      // the wedge candidate
	good2 := insert(goodSubject, t0.Add(2*time.Second)) // must publish despite `bad` being ahead of it

	// MaxAttempts=2 + BaseBackoff=0 → a failed row is due again next tick
	// and dead-letters on its 2nd failure. drainBatch is unexported;
	// drive it via the public Start loop with a fast poll.
	pub := database.NewOutboxPublisherWithConfig(pool, js, "wedgetest", zerolog.Nop(),
		database.PublisherConfig{
			PollInterval: 20 * time.Millisecond,
			MaxAttempts:  2,
			BaseBackoff:  0,
		})
	pctx, pcancel := context.WithCancel(ctx)
	go pub.Start(pctx)

	// Both good rows must publish (good2 proves rows BEHIND the bad one
	// still drain — the anti-wedge guarantee), and the bad row must land
	// in outbox_dlq. Poll until converged.
	require.Eventually(t, func() bool {
		var g1Pub, g2Pub bool
		_ = pool.QueryRow(ctx, `SELECT published FROM outbox WHERE id=$1`, good1).Scan(&g1Pub)
		_ = pool.QueryRow(ctx, `SELECT published FROM outbox WHERE id=$1`, good2).Scan(&g2Pub)
		var dlq int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_dlq WHERE id=$1`, bad).Scan(&dlq)
		return g1Pub && g2Pub && dlq == 1
	}, 20*time.Second, 100*time.Millisecond,
		"good rows must publish and the unbound-subject row must dead-letter")

	pcancel()
	pub.Stop()

	// The dead-lettered row carries the failure reason and is gone from
	// the active outbox (so the drain never re-sees it).
	var errMsg string
	var attempts int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT last_error, attempts FROM outbox_dlq WHERE id=$1`, bad).Scan(&errMsg, &attempts))
	require.NotEmpty(t, errMsg, "DLQ row must record the publish error")
	require.Equal(t, 2, attempts)
	var stillActive int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE id=$1`, bad).Scan(&stillActive))
	require.Zero(t, stillActive, "dead-lettered row must be removed from the active outbox")

	// Sanity: the good subject really was delivered to its stream.
	si, err := js.StreamInfo("WEDGETEST")
	require.NoError(t, err)
	require.Equal(t, uint64(2), si.State.Msgs, "both good events must be in the stream")
}
