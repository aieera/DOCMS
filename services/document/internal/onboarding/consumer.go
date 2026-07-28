// Package onboarding keeps the default-workspace membership current:
// every user belongs to their tenant's is_default workspace. Existing
// users are backfilled by migration 000100; this consumer covers users
// created afterwards (admin-created, SCIM/LDAP-synced — anything that
// emits dms.user.created.v1).
package onboarding

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
)

// Consumer is a durable push consumer on dms.user.created.v1.
type Consumer struct {
	js   nats.JetStreamContext
	pool *pgxpool.Pool
	log  zerolog.Logger
}

// NewConsumer constructs the consumer.
func NewConsumer(js nats.JetStreamContext, pool *pgxpool.Pool, log zerolog.Logger) *Consumer {
	return &Consumer{js: js, pool: pool, log: log}
}

// Start binds the durable subscription. Same non-fatal contract as the
// autolink consumer: callers log and continue when the stream isn't
// provisioned.
func (c *Consumer) Start() error {
	_, err := c.js.Subscribe("dms.user.created.v1", c.handle,
		nats.Durable("document-default-membership"),
		nats.ManualAck(),
		nats.AckWait(time.Minute),
	)
	return err
}

type userCreatedPayload struct {
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
}

func (c *Consumer) handle(msg *nats.Msg) {
	var env events.CloudEvent
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		c.log.Warn().Err(err).Msg("default-membership: bad envelope; dropping")
		_ = msg.Ack()
		return
	}
	var data userCreatedPayload
	_ = json.Unmarshal(env.Data, &data)
	tenantRaw := data.TenantID
	if tenantRaw == "" {
		tenantRaw = env.TenantID
	}
	tenantID, terr := uuid.Parse(tenantRaw)
	userID, uerr := uuid.Parse(data.UserID)
	if terr != nil || uerr != nil {
		_ = msg.Ack() // malformed/legacy event — nothing to enrol
		return
	}

	// Tenant context re-established from the envelope before DB work —
	// RLS applies (NATS-consumer discipline, see CLAUDE.md).
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := database.WithTenantTx(ctx, c.pool, tenantID, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			INSERT INTO workspace_members (tenant_id, workspace_id, user_id, role, added_by)
			SELECT w.tenant_id, w.id, $2, 'member', NULL
			  FROM workspaces w
			 WHERE w.tenant_id = $1 AND w.is_default AND w.deleted_at IS NULL
			ON CONFLICT (tenant_id, workspace_id, user_id) DO NOTHING
		`, tenantID, userID)
		return execErr
	})
	if err != nil {
		c.log.Error().Err(err).Str("user", userID.String()).
			Msg("default-membership: enrol failed; will redeliver")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}
