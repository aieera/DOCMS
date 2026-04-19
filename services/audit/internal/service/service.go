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
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/services/audit/internal/model"
	"github.com/vaultdms/vaultdms/services/audit/internal/repository"
)

// Service is the audit service facade.
type Service struct {
	repo  *repository.Repository
	rdb   *redis.Client
	log   zerolog.Logger
	locks sync.Map // per-tenant in-process mutex (Redis SETNX backs cross-instance)
}

// Config is the DI struct.
type Config struct {
	Repo   *repository.Repository
	Redis  *redis.Client
	Logger zerolog.Logger
}

// New creates a Service.
func New(cfg Config) *Service {
	return &Service{repo: cfg.Repo, rdb: cfg.Redis, log: cfg.Logger}
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
			return result, nil
		}
		result.Verified++
		prevHash = e.EventHash
	}
	auditVerifyTotal.WithLabelValues(tenantID, "ok").Inc()
	auditVerifyEventsScanned.WithLabelValues(tenantID).Add(float64(len(events)))
	return result, nil
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
