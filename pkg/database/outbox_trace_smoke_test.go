//go:build integration
// +build integration

// Golden-trace smoke test.
//
// Run with: go test -tags integration -run TestOutboxTrace ./pkg/database/...
//
// Proves the ONE non-obvious tracing invariant: a distributed trace stays
// connected across the async NATS boundary because the trace context rides
// the transactional outbox. Concretely:
//
//	span (upload) → OutboxRepository.Insert (stamps trace_context on the row)
//	              → OutboxPublisher (copies it onto the NATS traceparent header)
//	              → consumer extracts it → its span shares the SAME trace ID.
//
// If this test fails, traces are silently splitting into disconnected roots
// at every service boundary — the exact failure the outbox propagation was
// built to prevent. This is the golden-trace assertion referenced by the
// tracing docs and wired into CI's integration lane.
//
// Self-contained on purpose: it provisions its own minimal outbox schema and
// binds a single small JetStream stream via raw nats.Connect, NOT
// events.ConnectNATS (whose DefaultStreams reserves ~240GB, issue #84) and
// NOT the full document migration chain (coupled/broken on a clean DB).
package database_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/pkg/tracing"
)

// traceOutboxDDL is a superset outbox schema (document 000001 + the actor
// columns + migration 000096's trace_context) created verbatim so the test
// skips the full — and clean-DB-broken — document migration chain. The
// extra columns are harmless for the base publisher and satisfy the
// hardened one, so this runs regardless of which publisher version is built.
const traceOutboxDDL = `
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
	next_attempt_at TIMESTAMPTZ,
	trace_context   JSONB
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

func TestOutboxTrace_GoldenTraceSurvivesNATSHop(t *testing.T) {
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
	_, err = pool.Exec(ctx, traceOutboxDDL)
	require.NoError(t, err)

	// Raw JetStream + one small stream for the test subject only — not
	// events.ConnectNATS/DefaultStreams (~240GB reservations, issue #84).
	nc, err := nats.Connect(natsURL)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	const subject = "dms.tracetest.version.uploaded.v1"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "TRACETEST",
		Subjects: []string{"dms.tracetest.>"},
		Storage:  nats.FileStorage,
		MaxBytes: 16 * 1024 * 1024,
	})
	require.NoError(t, err)

	// A real, always-sampling TracerProvider so the span we start has a
	// valid, sampled SpanContext that InjectToMap will serialize. Also
	// install the W3C propagator (Init would do this in a live process).
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracing.SetGlobalPropagator()

	// Subscribe BEFORE producing so we can't miss the message. Core NATS
	// subscriber receives the JetStream-published message + its headers.
	received := make(chan *nats.Msg, 4)
	sub, err := nc.ChanSubscribe(subject, received)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Start the "upload request" span and produce the event inside it.
	spanCtx, span := tracing.StartSpan(context.Background(), "upload")
	wantTrace := span.SpanContext().TraceID()
	require.True(t, wantTrace.IsValid(), "producer span must have a valid trace id")

	tenant := uuid.Must(uuid.NewV7())
	payload, _ := json.Marshal(map[string]any{"document_id": "abc", "mime_type": "application/pdf"})
	evt := database.NewOutboxEvent(tenant, subject, "document", uuid.Must(uuid.NewV7()), payload)
	require.NoError(t, database.WithTenantTx(spanCtx, pool, tenant, func(tx pgx.Tx) error {
		return database.NewOutboxRepository().Insert(spanCtx, tx, evt)
	}))
	span.End()

	// The row must have captured the trace context (migration 000096 column).
	var traceJSON []byte
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT trace_context FROM outbox WHERE id = $1", evt.ID).Scan(&traceJSON))
	require.NotEmpty(t, traceJSON, "Insert must stamp trace_context onto the outbox row")
	var stamped map[string]string
	require.NoError(t, json.Unmarshal(traceJSON, &stamped))
	require.Contains(t, stamped, "traceparent")
	require.Contains(t, stamped["traceparent"], wantTrace.String(),
		"stamped traceparent must carry the producing span's trace id")

	// Run the real publisher — it must copy trace_context onto the NATS msg.
	pub := database.NewOutboxPublisherWithConfig(
		pool, js, "document", zerolog.Nop(),
		database.PublisherConfig{PollInterval: 50 * time.Millisecond},
	)
	pubCtx, cancelPub := context.WithCancel(ctx)
	t.Cleanup(cancelPub)
	go pub.Start(pubCtx)
	t.Cleanup(pub.Stop)

	select {
	case msg := <-received:
		// 1. The traceparent header rode across the NATS hop.
		tpHeader := msg.Header.Get("traceparent")
		require.NotEmpty(t, tpHeader, "published NATS message must carry a traceparent header")
		require.Contains(t, tpHeader, wantTrace.String(),
			"traceparent on the NATS message must carry the producing trace id")

		// 2. The consumer, extracting from the headers exactly as the search
		//    indexer's handlerCtx does, starts a span in the SAME trace —
		//    i.e. producer and consumer are one connected trace.
		carrier := make(map[string]string, len(msg.Header))
		for k := range msg.Header {
			carrier[k] = msg.Header.Get(k)
		}
		consumerCtx, cspan := tracing.ConsumerContext(context.Background(), carrier,
			"search.index "+subject)
		defer cspan.End()
		gotTrace := trace.SpanFromContext(consumerCtx).SpanContext().TraceID()
		require.Equal(t, wantTrace.String(), gotTrace.String(),
			"consumer span must be in the same trace as the producer — the trace broke at the NATS boundary")
	case <-time.After(5 * time.Second):
		t.Fatal("no NATS message within 5s")
	}
}
