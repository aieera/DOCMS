package siem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
)

// dlqSubject lands SIEM-forward failures on the COMPLIANCE_EVENTS DLQ stream
// (dms.dlq.compliance_events.>), which pkg/events auto-creates. No new stream.
const dlqSubject = "dms.dlq.compliance_events.siem"

// siemSubjects mirrors the audit consumer's subject list — SIEM wants the same
// security-relevant domain events. Kept in sync with pkg/events.DefaultStreams.
var siemSubjects = []string{
	"dms.document.>", "dms.version.>", "dms.workspace.>",
	"dms.user.>", "dms.session.>", "dms.apikey.>", "dms.auth.>",
	"dms.policy.>", "dms.permission.>",
	"dms.billing.>", "dms.subscription.>", "dms.usage.>",
	"dms.audit.>", "dms.search.>", "dms.workflow.>", "dms.task.>",
	"dms.ocr.>", "dms.classify.>", "dms.embed.>", "dms.ner.>",
	"dms.notify.>", "dms.sharelink.>", "dms.folder.>",
	"dms.record.>", "dms.export.>", "dms.hold.>", "dms.retention.>",
}

// Service owns the SIEM sink config + the independent forwarding consumer.
type Service struct {
	pool       *pgxpool.Pool
	fwd        *Forwarder
	log        zerolog.Logger
	maxDeliver int
}

func New(pool *pgxpool.Pool, log zerolog.Logger) *Service {
	return &Service{
		pool:       pool,
		fwd:        NewForwarder(&http.Client{Timeout: 10 * time.Second}),
		log:        log,
		maxDeliver: 5,
	}
}

// ---- consumer ------------------------------------------------------------

// StartConsumer subscribes to the domain subjects on siem-* durables —
// independent of the audit hash-chain consumer, so a slow sink can't block
// audit ingest. Retries via redelivery (MaxDeliver); past the cap it routes
// to the DLQ and terminates.
func (s *Service) StartConsumer(parent context.Context, js nats.JetStreamContext) error {
	if parent == nil {
		parent = context.Background()
	}
	handler := func(msg *nats.Msg) {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		if err := s.handle(ctx, msg.Subject, msg.Data); err != nil {
			md, _ := msg.Metadata()
			if md != nil && int(md.NumDelivered) >= s.maxDeliver {
				s.toDLQ(js, msg.Subject, msg.Data, err)
				_ = msg.Ack() // terminate redelivery
				return
			}
			_ = msg.Nak() // retry
			return
		}
		_ = msg.Ack()
	}
	for _, subj := range siemSubjects {
		durable := "siem-" + strings.ReplaceAll(strings.TrimSuffix(subj, ".>"), ".", "_")
		if _, err := js.Subscribe(subj, handler,
			nats.Durable(durable), nats.ManualAck(), nats.MaxDeliver(s.maxDeliver), nats.AckWait(30*time.Second),
		); err != nil {
			return fmt.Errorf("siem subscribe %s: %w", subj, err)
		}
	}
	s.log.Info().Int("subjects", len(siemSubjects)).Msg("siem forwarding consumer started")
	return nil
}

// handle normalises one event and fans it out to the tenant's enabled sinks.
// Returns an error (→ retry/DLQ) if ANY sink delivery failed. At-least-once:
// on retry, healthy sinks may see a duplicate — SIEM ingestion tolerates this.
func (s *Service) handle(ctx context.Context, subject string, raw []byte) error {
	ev := Normalize(subject, raw, time.Now())
	if ev.TenantID == "" {
		return nil // can't route without a tenant; ack + skip
	}
	tenantID, err := uuid.Parse(ev.TenantID)
	if err != nil {
		return nil
	}
	sinks, err := s.listEnabled(ctx, tenantID)
	if err != nil {
		return err // transient DB error → retry
	}
	if len(sinks) == 0 {
		return nil // nothing configured; ack
	}
	var firstErr error
	for _, sink := range sinks {
		if err := s.fwd.Forward(ctx, sink, ev); err != nil {
			s.bumpFailed(ctx, tenantID, sink.ID, err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		s.bumpDelivered(ctx, tenantID, sink.ID)
	}
	return firstErr
}

func (s *Service) toDLQ(js nats.JetStreamContext, subject string, raw []byte, cause error) {
	payload, _ := json.Marshal(map[string]any{
		"original_subject": subject,
		"error":            cause.Error(),
		"failed_at":        time.Now().UTC().Format(time.RFC3339),
		"raw":              json.RawMessage(raw),
	})
	if _, err := js.Publish(dlqSubject, payload); err != nil {
		s.log.Error().Err(err).Str("subject", subject).Msg("siem DLQ publish failed")
	}
}

// SendTestEvent forwards a synthetic event to one sink and returns the delivery
// result (drives the admin "send test event" button). Bumps health counters.
func (s *Service) SendTestEvent(ctx context.Context, tenantID, sinkID uuid.UUID) error {
	sink, err := s.get(ctx, tenantID, sinkID)
	if err != nil {
		return err
	}
	ev := NormalizedEvent{
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		TenantID:     tenantID.String(),
		Subject:      "dms.siem.test.v1",
		Action:       "siem.test",
		Actor:        "system",
		ResourceType: "siem_sink",
		ResourceID:   sinkID.String(),
	}
	if err := s.fwd.Forward(ctx, *sink, ev); err != nil {
		s.bumpFailed(ctx, tenantID, sinkID.String(), err.Error())
		return err
	}
	s.bumpDelivered(ctx, tenantID, sinkID.String())
	return nil
}

// ---- store (per-tenant CRUD + health) ------------------------------------

const sinkCols = `id::text, tenant_id::text, name, type, endpoint, COALESCE(token,''),
	enabled, delivered_count, failed_count, last_success_at, COALESCE(last_error,''),
	last_error_at, created_at, updated_at`

func scanSink(row pgx.Row) (*Sink, error) {
	var k Sink
	if err := row.Scan(&k.ID, &k.TenantID, &k.Name, &k.Type, &k.Endpoint, &k.Token,
		&k.Enabled, &k.DeliveredCount, &k.FailedCount, &k.LastSuccessAt, &k.LastError,
		&k.LastErrorAt, &k.CreatedAt, &k.UpdatedAt); err != nil {
		return nil, err
	}
	return &k, nil
}

func (s *Service) Create(ctx context.Context, tenantID uuid.UUID, in SinkInput) (*Sink, error) {
	if in.Name == "" || in.Endpoint == "" {
		return nil, errors.New("name and endpoint are required")
	}
	if in.Type != SinkSyslog && in.Type != SinkSplunk && in.Type != SinkSentinel {
		return nil, errors.New("type must be syslog|splunk_hec|sentinel_hec")
	}
	var out *Sink
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanSink(tx.QueryRow(ctx, `
			INSERT INTO siem_sinks (tenant_id, name, type, endpoint, token, enabled)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),COALESCE($6,true))
			RETURNING `+sinkCols,
			tenantID, in.Name, in.Type, in.Endpoint, in.Token, in.Enabled))
		return e
	})
	return out, err
}

func (s *Service) List(ctx context.Context, tenantID uuid.UUID) ([]*Sink, error) {
	out := []*Sink{}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+sinkCols+` FROM siem_sinks WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			k, err := scanSink(rows)
			if err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) get(ctx context.Context, tenantID, id uuid.UUID) (*Sink, error) {
	var out *Sink
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanSink(tx.QueryRow(ctx, `SELECT `+sinkCols+` FROM siem_sinks WHERE tenant_id=$1 AND id=$2`, tenantID, id))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("sink not found")
	}
	return out, err
}

// Get is the exported single-sink read (token masked by the handler).
func (s *Service) Get(ctx context.Context, tenantID, id uuid.UUID) (*Sink, error) {
	return s.get(ctx, tenantID, id)
}

func (s *Service) Update(ctx context.Context, tenantID, id uuid.UUID, in SinkInput) (*Sink, error) {
	var out *Sink
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanSink(tx.QueryRow(ctx, `
			UPDATE siem_sinks SET
			    name = COALESCE(NULLIF($3,''), name),
			    type = COALESCE(NULLIF($4,''), type),
			    endpoint = COALESCE(NULLIF($5,''), endpoint),
			    token = COALESCE(NULLIF($6,''), token),
			    enabled = COALESCE($7, enabled),
			    updated_at = now()
			 WHERE tenant_id=$1 AND id=$2
			RETURNING `+sinkCols,
			tenantID, id, in.Name, string(in.Type), in.Endpoint, in.Token, in.Enabled))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("sink not found")
	}
	return out, err
}

func (s *Service) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `DELETE FROM siem_sinks WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return errors.New("sink not found")
		}
		return nil
	})
}

func (s *Service) listEnabled(ctx context.Context, tenantID uuid.UUID) ([]Sink, error) {
	out := []Sink{}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+sinkCols+` FROM siem_sinks WHERE tenant_id=$1 AND enabled`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			k, err := scanSink(rows)
			if err != nil {
				return err
			}
			out = append(out, *k)
		}
		return rows.Err()
	})
	return out, err
}

// bump* update delivery-health counters. Best-effort (a counter write must
// never fail the forward path); id is the sink's uuid string.
func (s *Service) bumpDelivered(ctx context.Context, tenantID uuid.UUID, id string) {
	_ = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`UPDATE siem_sinks SET delivered_count=delivered_count+1, last_success_at=now(), updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			tenantID, id)
		return e
	})
}

func (s *Service) bumpFailed(ctx context.Context, tenantID uuid.UUID, id, errMsg string) {
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	_ = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`UPDATE siem_sinks SET failed_count=failed_count+1, last_error=$3, last_error_at=now(), updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			tenantID, id, errMsg)
		return e
	})
}
