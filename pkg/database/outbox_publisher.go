package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// PublisherConfig tunes the outbox → NATS bridge. Zero values pick safe
// defaults matching the spec (100ms poll, 100-event batch, 24h retention).
type PublisherConfig struct {
	PollInterval    time.Duration
	BatchSize       int
	RetainPublished time.Duration
	CleanupInterval time.Duration
	PublishTimeout  time.Duration
	// MaxAttempts is how many times a single row is retried before it is
	// dead-lettered to outbox_dlq. Default 5.
	MaxAttempts int
	// BaseBackoff is the first retry delay; each subsequent attempt waits
	// min(BaseBackoff*2^(attempts-1), MaxBackoff). Default 2s / 5m. Tests
	// set BaseBackoff=0 to retry immediately.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
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
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	// BaseBackoff==0 is a legitimate test setting (retry immediately), so
	// only default MaxBackoff.
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 5 * time.Minute
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

// drainBatch does one poll cycle: lock a batch of DUE unpublished rows
// with SKIP LOCKED and publish each. Crucially it does NOT stop on the
// first failure — a single unbound-subject row must not wedge the whole
// shared outbox (Wave 0.2). Per row:
//   - success            → mark published.
//   - failure, attempts+1 < MaxAttempts → bump attempts + last_error and
//     defer via next_attempt_at = now + backoff(attempts); a later row
//     still publishes this same cycle.
//   - failure, attempts+1 >= MaxAttempts → move the row to outbox_dlq
//     with the error and remove it from the active outbox so the drain
//     advances forever.
func (p *OutboxPublisher) drainBatch(ctx context.Context) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// ip_address::text — pgx binary mode can't decode inet into *string.
	// WHERE also excludes backoff-deferred rows (next_attempt_at in the
	// future) so a failing row yields the lock to the rows behind it.
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, event_type, aggregate_type, aggregate_id,
		       payload, created_at, actor_id, actor_name, ip_address::text, user_agent,
		       attempts
		FROM outbox
		WHERE NOT published
		  AND (next_attempt_at IS NULL OR next_attempt_at <= now())
		ORDER BY created_at, id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, p.cfg.BatchSize)
	if err != nil {
		return fmt.Errorf("select unpublished: %w", err)
	}

	type row struct {
		e        OutboxEvent
		attempts int
	}
	var batch []row
	for rows.Next() {
		var (
			r         row
			actorID   *uuid.UUID
			actorName *string
			ipAddress *string
			userAgent *string
		)
		if err := rows.Scan(
			&r.e.ID, &r.e.TenantID, &r.e.EventType, &r.e.AggregateType,
			&r.e.AggregateID, &r.e.Payload, &r.e.CreatedAt,
			&actorID, &actorName, &ipAddress, &userAgent, &r.attempts,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan: %w", err)
		}
		r.e.ActorID = actorID
		if actorName != nil {
			r.e.ActorName = *actorName
		}
		if ipAddress != nil {
			r.e.IPAddress = *ipAddress
		}
		if userAgent != nil {
			r.e.UserAgent = *userAgent
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows err: %w", err)
	}

	published := make([]uuid.UUID, 0, len(batch))
	for _, r := range batch {
		perr := p.publishOne(ctx, r.e)
		if perr == nil {
			published = append(published, r.e.ID)
			continue
		}
		// A failure never halts the batch — the whole point of Wave 0.2.
		outboxPublishFailures.WithLabelValues(p.serviceName, r.e.EventType).Inc()
		attempts := r.attempts + 1
		if attempts >= p.cfg.MaxAttempts {
			if err := p.deadLetter(ctx, tx, r.e, attempts, perr.Error()); err != nil {
				return fmt.Errorf("dead-letter %s: %w", r.e.ID, err)
			}
			outboxDLQTotal.WithLabelValues(p.serviceName, r.e.EventType).Inc()
			p.log.Error().Err(perr).
				Str("event_id", r.e.ID.String()).Str("event_type", r.e.EventType).
				Int("attempts", attempts).Msg("outbox row dead-lettered after max attempts")
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE outbox
			   SET attempts = $2, last_error = $3, next_attempt_at = now() + $4
			 WHERE id = $1
		`, r.e.ID, attempts, perr.Error(), p.backoff(attempts)); err != nil {
			return fmt.Errorf("record retry %s: %w", r.e.ID, err)
		}
		p.log.Warn().Err(perr).
			Str("event_id", r.e.ID.String()).Str("event_type", r.e.EventType).
			Int("attempts", attempts).Msg("outbox publish failed; will retry")
	}

	if len(published) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE outbox SET published = true, published_at = now()
			WHERE id = ANY($1)
		`, published); err != nil {
			return fmt.Errorf("mark published: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	p.observe(ctx)
	return nil
}

// deadLetter moves an exhausted row out of outbox into outbox_dlq (with
// the last error) so the active drain never sees it again. Runs in the
// drain tx so the move + removal are atomic.
func (p *OutboxPublisher) deadLetter(ctx context.Context, tx pgx.Tx, e OutboxEvent, attempts int, errMsg string) error {
	var actorID any
	if e.ActorID != nil {
		actorID = e.ActorID
	}
	nullIf := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox_dlq (
			id, tenant_id, event_type, aggregate_type, aggregate_id, payload,
			created_at, actor_id, actor_name, ip_address, user_agent,
			attempts, last_error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::inet,$11,$12,$13)
	`, e.ID, e.TenantID, e.EventType, e.AggregateType, e.AggregateID, e.Payload,
		e.CreatedAt, actorID, nullIf(e.ActorName), e.IPAddress, nullIf(e.UserAgent),
		attempts, errMsg); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM outbox WHERE id = $1`, e.ID)
	return err
}

// backoff returns the delay before the next attempt: BaseBackoff *
// 2^(attempts-1), capped at MaxBackoff. BaseBackoff==0 → 0 (tests).
func (p *OutboxPublisher) backoff(attempts int) time.Duration {
	if p.cfg.BaseBackoff <= 0 {
		return 0
	}
	d := p.cfg.BaseBackoff
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= p.cfg.MaxBackoff {
			return p.cfg.MaxBackoff
		}
	}
	if d > p.cfg.MaxBackoff {
		d = p.cfg.MaxBackoff
	}
	return d
}

// observe refreshes the DLQ-depth and drain-lag gauges. Best-effort:
// a metrics-read failure must not fail the drain.
func (p *OutboxPublisher) observe(ctx context.Context) {
	var depth int64
	if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_dlq`).Scan(&depth); err == nil {
		outboxDLQDepth.WithLabelValues(p.serviceName).Set(float64(depth))
	}
	var lag *float64
	if err := p.pool.QueryRow(ctx, `
		SELECT EXTRACT(EPOCH FROM (now() - min(created_at)))
		FROM outbox
		WHERE NOT published AND (next_attempt_at IS NULL OR next_attempt_at <= now())
	`).Scan(&lag); err == nil {
		if lag == nil {
			outboxDrainLagSeconds.WithLabelValues(p.serviceName).Set(0)
		} else {
			outboxDrainLagSeconds.WithLabelValues(p.serviceName).Set(*lag)
		}
	}
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
		VDMSUserAgent:   e.UserAgent,
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

// cloudEvent is the CloudEvents v1.0 envelope with the SeDoc tenantid
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
	// SeDoc-specific CloudEvents extensions: request-context audit
	// fields stamped on the outbox row at write time. The audit
	// consumer reads these as a fallback when the inner data payload
	// doesn't include actor / IP (i.e. for every emitter that
	// pre-dates the outbox-audit-context plumbing). Lowercase
	// per the CloudEvents spec's extension-naming convention
	// (https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md#attribute-naming-convention).
	VDMSActorID   string `json:"vdmsactorid,omitempty"`
	VDMSActorName string `json:"vdmsactorname,omitempty"`
	VDMSClientIP  string `json:"vdmsclientip,omitempty"`
	VDMSUserAgent string `json:"vdmsuseragent,omitempty"`
}
