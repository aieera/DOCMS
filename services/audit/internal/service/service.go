// Package service contains the audit service business logic: NATS
// consumption, hash chaining, integrity verification, GDPR operations.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/services/audit/internal/model"
	"github.com/vaultdms/vaultdms/services/audit/internal/repository"
)

// Service is the audit service facade.
type Service struct {
	repo   *repository.Repository
	rdb    *redis.Client
	pool   *pgxpool.Pool               // Wave 17: tamper-detect outbox emission
	outbox *database.OutboxRepository  // nil → emission path is a no-op
	log    zerolog.Logger
	locks  sync.Map // per-tenant in-process mutex (Redis SETNX backs cross-instance)
}

// Config is the DI struct.
type Config struct {
	Repo   *repository.Repository
	Redis  *redis.Client
	// Pool + Outbox are both required for `dms.audit.tamper_detected.v1`
	// emission on chain break. Leave nil in tests that don't care about
	// the emit path; VerifyIntegrity falls back to metrics-only alerting.
	Pool   *pgxpool.Pool
	Outbox *database.OutboxRepository
	Logger zerolog.Logger
}

// New creates a Service.
func New(cfg Config) *Service {
	return &Service{
		repo:   cfg.Repo,
		rdb:    cfg.Redis,
		pool:   cfg.Pool,
		outbox: cfg.Outbox,
		log:    cfg.Logger,
	}
}

// IngestEvent derives an audit entry from a raw NATS CloudEvents envelope.
func (s *Service) IngestEvent(ctx context.Context, subject string, raw []byte) error {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("unmarshal event: %w", err)
	}
	data, _ := envelope["data"].(map[string]any)
	if data == nil {
		data = envelope
	}
	tenantID := strField(data, "tenant_id")
	if tenantID == "" {
		return fmt.Errorf("event missing tenant_id")
	}
	action := strField(envelope, "type")
	if action == "" {
		action = subject
	}
	event := &model.AuditEvent{
		ID:           newID(),
		TenantID:     tenantID,
		Actor:        strField(data, "actor_id"),
		ActorName:    strField(data, "actor_name"),
		Action:       action,
		ResourceType: strField(data, "resource_type"),
		ResourceID:   strField(data, "resource_id"),
		ResourceTitle: strField(data, "resource_title"),
		Details:      raw,
		IPAddress:    strField(data, "ip_address"),
		UserAgent:    strField(data, "user_agent"),
		SourceEvent:  subject,
		CreatedAt:    time.Now().UTC(),
	}

	// Hash chain: acquire per-tenant lock, fetch previous hash, compute new.
	unlock := s.lockTenant(ctx, tenantID)
	defer unlock()

	prevHash, err := s.repo.GetLastHash(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("get last hash: %w", err)
	}
	event.PreviousHash = prevHash
	event.EventHash = computeHash(prevHash, tenantID, event.Actor, event.Action, event.ResourceID, event.CreatedAt)

	return s.repo.Insert(ctx, event)
}

// List returns paginated audit events.
func (s *Service) List(ctx context.Context, f model.ListFilter) ([]*model.AuditEvent, string, error) {
	return s.repo.List(ctx, f)
}

// VerifyIntegrity walks the hash chain for a tenant and checks every link.
// Emits audit_verify_total{result} + audit_chain_break_total metrics so
// the daily CronJob's output is scrapeable by Prometheus — alertmanager's
// p0-platform-emergency rule (deploy/monitoring/alerts/tiered-alerts.yml)
// pages on-call the moment audit_chain_break_total moves off zero.
func (s *Service) VerifyIntegrity(ctx context.Context, tenantID string) (*model.IntegrityResult, error) {
	events, err := s.repo.ListAll(ctx, tenantID)
	if err != nil {
		auditVerifyTotal.WithLabelValues(tenantID, "error").Inc()
		return nil, err
	}
	result := &model.IntegrityResult{TenantID: tenantID, TotalEvents: int64(len(events)), Valid: true}
	prevHash := ""
	for _, e := range events {
		expected := computeHash(prevHash, e.TenantID, e.Actor, e.Action, e.ResourceID, e.CreatedAt)
		if e.EventHash != expected {
			result.Valid = false
			result.BrokenAt = e.ID
			result.BrokenHash = e.EventHash
			result.ExpectedHash = expected
			auditChainBreakTotal.WithLabelValues(tenantID).Inc()
			auditVerifyTotal.WithLabelValues(tenantID, "break").Inc()
			// Emit `dms.audit.tamper_detected.v1` via outbox so downstream
			// consumers (ops Slack bot, SOC feed) see the event in
			// addition to the alertmanager counter-based page. Outbox
			// insert failure does NOT swallow the verify result — the
			// forensics report is the primary output; emission is a
			// secondary signal.
			if err := s.emitTamperDetected(ctx, tenantID, e.ID, expected, e.EventHash, result.Verified, result.TotalEvents); err != nil {
				s.log.Error().Err(err).Str("tenant_id", tenantID).Str("broken_at", e.ID).Msg("emit tamper_detected failed")
			}
			return result, nil
		}
		result.Verified++
		prevHash = e.EventHash
	}
	auditVerifyTotal.WithLabelValues(tenantID, "ok").Inc()
	auditVerifyEventsScanned.WithLabelValues(tenantID).Add(float64(len(events)))
	return result, nil
}

// emitTamperDetected writes a `dms.audit.tamper_detected.v1` outbox
// row. Soft-noop when pool/outbox aren't configured (unit tests);
// returns error so callers can log, never fails the caller's flow.
func (s *Service) emitTamperDetected(
	ctx context.Context,
	tenantID, brokenAtEventID, expectedHash, actualHash string,
	verifiedBeforeBreak int64, totalEvents int64,
) error {
	if s.pool == nil || s.outbox == nil {
		return nil
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("parse tenant_id: %w", err)
	}
	brokenUUID, _ := uuid.Parse(brokenAtEventID)
	payload, err := json.Marshal(map[string]any{
		"tenant_id":              tenantID,
		"broken_at_event_id":     brokenAtEventID,
		"expected_hash":          expectedHash,
		"actual_hash":            actualHash,
		"verified_before_break":  verifiedBeforeBreak,
		"total_events":           totalEvents,
		"detected_at":            time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	evt := database.NewOutboxEvent(tenantUUID, "dms.audit.tamper_detected.v1", "audit_chain", brokenUUID, json.RawMessage(payload))
	return database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		return s.outbox.Insert(ctx, tx, evt)
	})
}

// ExportSubject returns all events for a data subject (GDPR Art.15).
func (s *Service) ExportSubject(ctx context.Context, tenantID, subjectID string) (*model.DataSubjectExport, error) {
	events, err := s.repo.ListBySubject(ctx, tenantID, subjectID)
	if err != nil {
		return nil, err
	}
	return &model.DataSubjectExport{SubjectID: subjectID, Events: events, ExportedAt: time.Now().UTC()}, nil
}

// AnonymizeSubject redacts a data subject's PII (GDPR Art.17).
func (s *Service) AnonymizeSubject(ctx context.Context, tenantID, subjectID string) (int64, error) {
	return s.repo.AnonymizeSubject(ctx, tenantID, subjectID)
}

// ---- NATS subscriber ------------------------------------------------------

// StartConsumer subscribes to all domain events via wildcard. `parent`
// is the service lifecycle ctx; cancelling it cascades into every
// in-flight handler on shutdown (Wave 6 Prompt 6.4).
func (s *Service) StartConsumer(parent context.Context, js nats.JetStreamContext) error {
	if parent == nil {
		parent = context.Background()
	}
	// JetStream requires the subscribe subject to be covered by exactly one
	// stream. `dms.>` spans many streams, so we subscribe to each stream's
	// subject filter individually. Keep this list in sync with
	// pkg/events.DefaultStreams.
	subjects := []string{
		"dms.document.>", "dms.version.>", "dms.workspace.>",
		"dms.user.>", "dms.session.>", "dms.apikey.>", "dms.auth.>",
		"dms.policy.>", "dms.permission.>",
		"dms.billing.>", "dms.subscription.>", "dms.usage.>",
		"dms.audit.>",
		"dms.search.>",
		"dms.workflow.>", "dms.task.>",
		"dms.ocr.>", "dms.classify.>", "dms.embed.>", "dms.ner.>",
		"dms.notify.>",
		"dms.sharelink.>", "dms.folder.>", "dms.intelligence.>", "dms.rotation.>",
	}
	handler := func(msg *nats.Msg) {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		if corrID := msg.Header.Get("correlation-id"); corrID != "" {
			ctx = auth.SetCorrelationID(ctx, corrID)
		}
		if err := s.IngestEvent(ctx, msg.Subject, msg.Data); err != nil {
			s.log.Error().Err(err).Str("subject", msg.Subject).Msg("audit ingest failed")
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	}
	for _, subj := range subjects {
		durable := "audit-" + strings.ReplaceAll(strings.TrimSuffix(subj, ".>"), ".", "_")
		if _, err := js.Subscribe(subj, handler,
			nats.Durable(durable), nats.ManualAck(), nats.MaxDeliver(5), nats.AckWait(30*time.Second),
		); err != nil {
			return fmt.Errorf("subscribe %s: %w", subj, err)
		}
		s.log.Info().Str("subject", subj).Str("durable", durable).Msg("audit subscribed")
	}
	return nil
}

// ---- internals ------------------------------------------------------------

func computeHash(prev, tenantID, actor, action, resourceID string, t time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s", prev, tenantID, actor, action, resourceID, t.Format(time.RFC3339Nano))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Service) lockTenant(ctx context.Context, tenantID string) func() {
	key := "audit_lock:" + tenantID
	// In-process mutex first.
	val, _ := s.locks.LoadOrStore(tenantID, &sync.Mutex{})
	mu := val.(*sync.Mutex)
	mu.Lock()
	// Redis SETNX for cross-instance ordering (best-effort; in-process mutex is primary).
	if s.rdb != nil {
		_ = s.rdb.SetNX(ctx, key, "1", 5*time.Second).Err()
	}
	return func() {
		if s.rdb != nil {
			_ = s.rdb.Del(ctx, key).Err()
		}
		mu.Unlock()
	}
}

func newID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New().String()
	}
	return id.String()
}

func strField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
