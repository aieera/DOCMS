package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// PublisherConfig tunes the outbox → NATS bridge. Zero values pick safe
// defaults matching the spec (100ms poll, 100-event batch, 24h retention).
type PublisherConfig struct {
	PollInterval     time.Duration
	BatchSize        int
	RetainPublished  time.Duration
	CleanupInterval  time.Duration
	PublishTimeout   time.Duration
}

// OutboxPublisher is a background goroutine that polls the `outbox` table,
// wraps each row in a CloudEvents v1.0 envelope, and publishes to NATS
// JetStream. Multiple replicas can run concurrently: FOR UPDATE SKIP LOCKED
// partitions the work, and NATS deduplicates by Nats-Msg-Id (event.id).
type OutboxPublisher struct {
	pool        *pgxpool.Pool
	js          nats.JetStreamContext
	serviceName string
	log         zerolog.Logger
	cfg         PublisherConfig
	stop        chan struct{}
	done        chan struct{}
}

// NewOutboxPublisher wires a publisher. serviceName populates the
// CloudEvents `source` attribute ("vaultdms.<service>"). It is not started
// until Start(ctx) is called.
func NewOutboxPublisher(
	pool *pgxpool.Pool,
	js nats.JetStreamContext,
	serviceName string,
	log zerolog.Logger,
) *OutboxPublisher {
	return NewOutboxPublisherWithConfig(pool, js, serviceName, log, PublisherConfig{})
}

// NewOutboxPublisherWithConfig is the same as NewOutboxPublisher but accepts
// explicit tuning. Zero-valued fields fall back to defaults.
func NewOutboxPublisherWithConfig(
	pool *pgxpool.Pool,
	js nats.JetStreamContext,
	serviceName string,
	log zerolog.Logger,
	cfg PublisherConfig,
) *OutboxPublisher {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 100 * time.Millisecond
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.RetainPublished <= 0 {
		cfg.RetainPublished = 24 * time.Hour
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}
	if cfg.PublishTimeout <= 0 {
		cfg.PublishTimeout = 5 * time.Second
	}
	return &OutboxPublisher{
		pool:        pool,
		js:          js,
		serviceName: serviceName,
		log:         log.With().Str("component", "outbox_publisher").Logger(),
		cfg:         cfg,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// Start runs the publisher loop. It blocks until ctx is cancelled or Stop
// is called. Safe to run as `go publisher.Start(ctx)`.
func (p *OutboxPublisher) Start(ctx context.Context) {
	defer close(p.done)

	pollT := time.NewTicker(p.cfg.PollInterval)
	defer pollT.Stop()
	cleanupT := time.NewTicker(p.cfg.CleanupInterval)
	defer cleanupT.Stop()

	p.log.Info().Msg("outbox publisher started")
	for {
		select {
		case <-ctx.Done():
			p.log.Info().Msg("outbox publisher stopping (context cancelled)")
			return
		case <-p.stop:
			p.log.Info().Msg("outbox publisher stopping")
			return
		case <-pollT.C:
			if err := p.drainBatch(ctx); err != nil {
				p.log.Error().Err(err).Msg("drain batch")
			}
		case <-cleanupT.C:
			if err := p.cleanupOldEvents(ctx); err != nil {
				p.log.Error().Err(err).Msg("cleanup old events")
			}
		}
	}
}

// Stop asks the loop to exit and waits for it.
func (p *OutboxPublisher) Stop() {
	select {
	case <-p.stop:
		// already requested
	default:
		close(p.stop)
	}
	<-p.done
}

// drainBatch does one poll cycle: lock a batch of unpublished rows with
// SKIP LOCKED, publish each in order, and mark the successfully-delivered
// rows published. Partial failure stops the batch early; unpublished rows
// are retried on the next tick, preserving per-aggregate ordering.
func (p *OutboxPublisher) drainBatch(ctx context.Context) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, event_type, aggregate_type, aggregate_id,
		       payload, created_at, actor_id, actor_name, ip_address
		FROM outbox
		WHERE NOT published
		ORDER BY created_at, id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, p.cfg.BatchSize)
	if err != nil {
		return fmt.Errorf("select unpublished: %w", err)
	}

	var batch []OutboxEvent
	for rows.Next() {
		var (
			e         OutboxEvent
			actorID   *uuid.UUID
			actorName *string
			ipAddress *string
		)
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.EventType, &e.AggregateType,
			&e.AggregateID, &e.Payload, &e.CreatedAt,
			&actorID, &actorName, &ipAddress,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan: %w", err)
		}
		e.ActorID = actorID
		if actorName != nil {
			e.ActorName = *actorName
		}
		if ipAddress != nil {
			e.IPAddress = *ipAddress
		}
		batch = append(batch, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows err: %w", err)
	}
	if len(batch) == 0 {
		return nil
	}

	published := make([]uuid.UUID, 0, len(batch))
	for _, e := range batch {
		if err := p.publishOne(ctx, e); err != nil {
			// Stop on first failure so later events don't jump the queue;
			// next tick retries from this point.
			p.log.Error().
				Err(err).
				Str("event_id", e.ID.String()).
				Str("event_type", e.EventType).
				Msg("nats publish failed; batch halted")
			break
		}
		published = append(published, e.ID)
	}

	if len(published) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE outbox SET published = true, published_at = now()
			WHERE id = ANY($1)
		`, published); err != nil {
			return fmt.Errorf("mark published: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// publishOne wraps the row in a CloudEvents v1.0 envelope and publishes to
// JetStream with Nats-Msg-Id = event.id for idempotent delivery.
func (p *OutboxPublisher) publishOne(ctx context.Context, e OutboxEvent) error {
	envelope := cloudEvent{
		SpecVersion:     "1.0",
		ID:              e.ID.String(),
		Source:          "vaultdms." + p.serviceName,
		Type:            e.EventType,
		Subject:         e.AggregateType + "/" + e.AggregateID.String(),
		Time:            e.CreatedAt.UTC().Format(time.RFC3339),
		DataContentType: "application/json",
		TenantID:        e.TenantID.String(),
		Data:            e.Payload,
		VDMSActorName:   e.ActorName,
		VDMSClientIP:    e.IPAddress,
	}
	if e.ActorID != nil {
		envelope.VDMSActorID = e.ActorID.String()
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	pubCtx, cancel := context.WithTimeout(ctx, p.cfg.PublishTimeout)
	defer cancel()
	_, err = p.js.PublishMsg(&nats.Msg{
		Subject: e.EventType,
		Data:    body,
		Header:  nats.Header{"Nats-Msg-Id": []string{e.ID.String()}},
	}, nats.Context(pubCtx))
	if err != nil {
		return fmt.Errorf("js publish: %w", err)
	}
	return nil
}

// cleanupOldEvents deletes rows that have been published for longer than
// RetainPublished. Runs in its own short transaction.
func (p *OutboxPublisher) cleanupOldEvents(ctx context.Context) error {
	cutoff := time.Now().Add(-p.cfg.RetainPublished)
	ct, err := p.pool.Exec(ctx, `
		DELETE FROM outbox WHERE published AND published_at < $1
	`, cutoff)
	if err != nil {
		return err
	}
	if n := ct.RowsAffected(); n > 0 {
		p.log.Debug().Int64("rows", n).Msg("cleaned up old outbox events")
	}
	return nil
}

// cloudEvent is the CloudEvents v1.0 envelope with the VaultDMS tenantid
// extension. Exported as lowercase (unexported) because it is an
// implementation detail of the wire format.
type cloudEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject,omitempty"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	TenantID        string          `json:"tenantid,omitempty"`
	Data            json.RawMessage `json:"data"`
	// VaultDMS-specific CloudEvents extensions: request-context audit
	// fields stamped on the outbox row at write time. The audit
	// consumer reads these as a fallback when the inner data payload
	// doesn't include actor / IP (i.e. for every emitter that
	// pre-dates the outbox-audit-context plumbing). Lowercase
	// per the CloudEvents spec's extension-naming convention
	// (https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md#attribute-naming-convention).
	VDMSActorID   string `json:"vdmsactorid,omitempty"`
	VDMSActorName string `json:"vdmsactorname,omitempty"`
	VDMSClientIP  string `json:"vdmsclientip,omitempty"`
}

