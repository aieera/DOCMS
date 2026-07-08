//go:build integration
// +build integration

// Wave 0.2 — end-to-end proof that a compliance-scan notification is
// actually DELIVERED, not just bound. The intelligence worker writes a
// role-targeted dms.notification.send.v1 row into the shared outbox
// (services/intelligence/app/tasks/compliance_scan.py); PR #69 bound the
// subject to a stream so it stopped wedging the drain, but nothing
// consumed it. This test inserts the EXACT outbox row compliance_scan
// writes, runs the real OutboxPublisher drain, and asserts the
// notification service's dms.notification.send.v1 consumer resolved the
// target roles to users and created in-app notification rows for them
// (and only them). This is the DoD's "a compliance-scan run produces a
// delivered dms.notification.send.v1", proven at the Go level so we don't
// have to spin the Python worker.
package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/notification/internal/repository"
	"github.com/aieera/sedoc/services/notification/internal/service"
)

// sendDeliveryFixtureDDL is the delivery path's tables (as in
// prod_posture_delivery_test.go) plus the role columns UsersForRoles
// filters on, and the outbox + outbox_dlq the drain reads. Created
// verbatim so the test skips the full document migration chain (broken at
// 000021 on a clean DB, a separate blocker). No RLS/grants: this runs on
// the container superuser, so WithTenantTx's SET LOCAL is a no-op guard
// rather than an enforced policy — sufficient to exercise the wiring.
const sendDeliveryFixtureDDL = `
CREATE TABLE organizations (
	id         UUID PRIMARY KEY,
	deleted_at TIMESTAMPTZ
);
CREATE TABLE users (
	tenant_id  UUID NOT NULL REFERENCES organizations(id),
	id         UUID NOT NULL,
	role       TEXT NOT NULL DEFAULT 'viewer',
	status     TEXT NOT NULL DEFAULT 'active',
	deleted_at TIMESTAMPTZ,
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE notifications (
	id            UUID NOT NULL,
	tenant_id     UUID NOT NULL REFERENCES organizations(id),
	user_id       UUID NOT NULL,
	type          TEXT NOT NULL,
	title         TEXT NOT NULL,
	body          TEXT NOT NULL DEFAULT '',
	resource_type TEXT,
	resource_id   TEXT,
	channel       TEXT NOT NULL DEFAULT 'in_app',
	read          BOOLEAN NOT NULL DEFAULT false,
	delivered_at  TIMESTAMPTZ,
	read_at       TIMESTAMPTZ,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE notification_preferences (
	tenant_id      UUID NOT NULL REFERENCES organizations(id),
	user_id        UUID NOT NULL,
	channel        TEXT NOT NULL,
	event_type     TEXT NOT NULL,
	is_enabled     BOOLEAN NOT NULL DEFAULT true,
	digest_enabled BOOLEAN NOT NULL DEFAULT false,
	PRIMARY KEY (tenant_id, user_id, channel, event_type)
);
CREATE TABLE notification_snoozes (
	tenant_id  UUID NOT NULL REFERENCES organizations(id),
	id         UUID NOT NULL DEFAULT gen_random_uuid(),
	user_id    UUID NOT NULL,
	event_type TEXT NOT NULL,
	until_at   TIMESTAMPTZ NOT NULL,
	reason     TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE notification_dnd (
	tenant_id  UUID NOT NULL REFERENCES organizations(id),
	user_id    UUID NOT NULL,
	dnd_start  TIME NOT NULL,
	dnd_end    TIME NOT NULL,
	timezone   TEXT NOT NULL DEFAULT 'UTC',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, user_id)
);
CREATE TABLE notification_digests (
	tenant_id      UUID NOT NULL REFERENCES organizations(id),
	id             UUID NOT NULL DEFAULT gen_random_uuid(),
	user_id        UUID NOT NULL,
	event_type     TEXT NOT NULL,
	channel        TEXT NOT NULL,
	events         JSONB NOT NULL DEFAULT '[]'::jsonb,
	count          INT NOT NULL DEFAULT 0,
	flush_after_at TIMESTAMPTZ NOT NULL,
	flushed_at     TIMESTAMPTZ,
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE notification_user_prefs (
	tenant_id        UUID NOT NULL,
	user_id          UUID NOT NULL,
	email_enabled    BOOLEAN NOT NULL DEFAULT true,
	push_enabled     BOOLEAN NOT NULL DEFAULT true,
	slack_enabled    BOOLEAN NOT NULL DEFAULT false,
	sms_enabled      BOOLEAN NOT NULL DEFAULT false,
	quiet_hours_from TIME,
	quiet_hours_to   TIME,
	PRIMARY KEY (tenant_id, user_id)
);
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

func TestNotificationSendDelivery_FromComplianceScanOutboxRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)
	natsURL, natsCleanup, err := testutil.NewNATSContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(natsCleanup)
	redisURL, redisCleanup, err := testutil.NewRedisContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(redisCleanup)

	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, sendDeliveryFixtureDDL)
	require.NoError(t, err)

	ropts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	rdb := redis.NewClient(ropts)
	t.Cleanup(func() { _ = rdb.Close() })

	// Raw JetStream (not events.ConnectNATS → DefaultStreams' ~240GB
	// reservation, issue #84). One small stream covering dms.notification.>
	// so the publisher can deliver and the consumer can bind.
	nc, err := nats.Connect(natsURL)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "NOTIFY",
		Subjects: []string{"dms.notification.>"},
		Storage:  nats.FileStorage,
		MaxBytes: 32 * 1024 * 1024,
	})
	require.NoError(t, err)

	// Seed one tenant with three users: two hold a target role
	// (compliance_officer / admin — the compliance_scan default), one is a
	// plain viewer who must NOT receive.
	tenant := uuid.Must(uuid.NewV7())
	officer := uuid.Must(uuid.NewV7())
	admin := uuid.Must(uuid.NewV7())
	viewer := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)
	for _, u := range []struct {
		id   uuid.UUID
		role string
	}{
		{officer, "compliance_officer"},
		{admin, "admin"},
		{viewer, "viewer"},
	} {
		_, err = pool.Exec(ctx,
			`INSERT INTO users (tenant_id, id, role, status) VALUES ($1,$2,$3,'active')`,
			tenant, u.id, u.role)
		require.NoError(t, err)
	}

	// Wire the notification service exactly as cmd/server does, then start
	// only the dms.notification.send.v1 consumer under test.
	repo := repository.New(pool)
	svc := service.New(service.Config{Repo: repo, Redis: rdb, Logger: zerolog.Nop()})
	require.NoError(t, svc.StartNotificationSendConsumer(ctx, js))

	// Insert the EXACT outbox row compliance_scan.py writes on a
	// high/critical scan: event_type=dms.notification.send.v1,
	// aggregate_type='document', role-targeted JSON payload.
	documentID := uuid.Must(uuid.NewV7())
	notifyPayload := map[string]any{
		"tenant_id":      tenant.String(),
		"document_id":    documentID.String(),
		"subject":        "Compliance scan flagged a document",
		"body":           "Document flagged with high compliance risk: ssn, credit_card",
		"target_roles":   []string{"compliance_officer", "admin"},
		"category":       "compliance",
		"correlation_id": uuid.Must(uuid.NewV7()).String(),
	}
	payloadJSON, err := json.Marshal(notifyPayload)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO outbox (tenant_id, event_type, aggregate_type, aggregate_id, payload)
		VALUES ($1, 'dms.notification.send.v1', 'document', $2, $3::jsonb)`,
		tenant, documentID, string(payloadJSON))
	require.NoError(t, err)

	// Run the real drain. It wraps the row in the CloudEvents envelope and
	// publishes to dms.notification.send.v1 → NOTIFY stream → the consumer
	// resolves roles → users and fans out through Deliver.
	pub := database.NewOutboxPublisher(pool, js, "notification", zerolog.Nop())
	pctx, pcancel := context.WithCancel(ctx)
	go pub.Start(pctx)
	t.Cleanup(func() { pcancel(); pub.Stop() })

	countFor := func(u uuid.UUID) int {
		var n int
		_ = pool.QueryRow(ctx,
			`SELECT count(*) FROM notifications WHERE tenant_id=$1 AND user_id=$2`, tenant, u).Scan(&n)
		return n
	}

	// The two role-holders each get exactly one in-app notification; the
	// viewer gets none. Poll until converged.
	require.Eventually(t, func() bool {
		return countFor(officer) == 1 && countFor(admin) == 1
	}, 30*time.Second, 200*time.Millisecond,
		"compliance_scan notification.send row must deliver to both role-holders")

	require.Equal(t, 1, countFor(officer))
	require.Equal(t, 1, countFor(admin))
	require.Zero(t, countFor(viewer),
		"a user without a target role must not receive the role-targeted notification")

	// The outbox row published cleanly (no wedge, no dead-letter).
	var published bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT published FROM outbox WHERE aggregate_id=$1`, documentID).Scan(&published))
	require.True(t, published, "the notification.send outbox row must be marked published")
	var dlq int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_dlq`).Scan(&dlq))
	require.Zero(t, dlq, "a bound, deliverable subject must never dead-letter")

	// Sanity: the delivered rows carry the compliance content and document
	// resource, and the notification type is the payload's category.
	var typ, title, resourceID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT type, title, resource_id FROM notifications WHERE tenant_id=$1 AND user_id=$2`,
		tenant, officer).Scan(&typ, &title, &resourceID))
	require.Equal(t, "compliance", typ)
	require.Equal(t, "Compliance scan flagged a document", title)
	require.Equal(t, documentID.String(), resourceID)
}
