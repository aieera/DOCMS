// Package service contains the audit service business logic: NATS
// consumption, hash chaining, integrity verification, GDPR operations.
package service

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/repository"
)

// ErrSigningDisabled is returned by checkpoint operations when no
// Ed25519 signing key is configured (SEDOC_AUDIT_SIGNING_KEY unset).
var ErrSigningDisabled = errors.New("audit checkpoint signing disabled: set SEDOC_AUDIT_SIGNING_KEY")

// Service is the audit service facade.
type Service struct {
	repo  *repository.Repository
	rdb   *redis.Client
	log   zerolog.Logger
	locks sync.Map // per-tenant in-process mutex (Redis SETNX backs cross-instance)

	// Ed25519 checkpoint signing. signer is nil when no key is
	// configured, in which case checkpoint creation/verification is a
	// no-op that reports "disabled".
	signer    ed25519.PrivateKey
	signerPub ed25519.PublicKey
	keyID     string
}

// Config is the DI struct.
type Config struct {
	Repo   *repository.Repository
	Redis  *redis.Client
	Logger zerolog.Logger
	// SigningKeySeedB64 is the base64-encoded 32-byte Ed25519 seed used
	// to sign audit checkpoints. Empty disables checkpoint signing.
	// Generate with:  head -c32 /dev/urandom | base64
	SigningKeySeedB64 string
}

// New creates a Service.
func New(cfg Config) *Service {
	s := &Service{repo: cfg.Repo, rdb: cfg.Redis, log: cfg.Logger}
	if seed := strings.TrimSpace(cfg.SigningKeySeedB64); seed != "" {
		raw, err := base64.StdEncoding.DecodeString(seed)
		if err != nil || len(raw) != ed25519.SeedSize {
			cfg.Logger.Error().Int("len", len(raw)).Msg("invalid SEDOC_AUDIT_SIGNING_KEY (need base64 of 32 bytes); checkpoint signing disabled")
		} else {
			s.signer = ed25519.NewKeyFromSeed(raw)
			s.signerPub = s.signer.Public().(ed25519.PublicKey)
			sum := sha256.Sum256(s.signerPub)
			s.keyID = hex.EncodeToString(sum[:8])
			cfg.Logger.Info().Str("key_id", s.keyID).Msg("audit checkpoint signing enabled (ed25519)")
		}
	}
	return s
}

// CanonicalCheckpointMessage is the exact byte string signed for a
// checkpoint. Published so an external verifier can reconstruct it from
// (tenant_id, head_hash, event_count) and verify the signature with the
// public key from GET /api/v1/audit/signing-key.
func CanonicalCheckpointMessage(tenantID, headHash string, eventCount int64) []byte {
	return []byte(fmt.Sprintf("sedoc-audit-checkpoint:v1|%s|%s|%d", tenantID, headHash, eventCount))
}

// SigningKeyInfo describes the checkpoint signing key for external
// verifiers.
type SigningKeyInfo struct {
	Enabled   bool   `json:"enabled"`
	Algo      string `json:"algo,omitempty"`
	KeyID     string `json:"key_id,omitempty"`
	PublicKey string `json:"public_key,omitempty"` // base64
}

// SigningKey returns the public signing-key material (never the private
// key) so third parties can verify checkpoints independently.
func (s *Service) SigningKey() SigningKeyInfo {
	if s.signer == nil {
		return SigningKeyInfo{Enabled: false}
	}
	return SigningKeyInfo{
		Enabled:   true,
		Algo:      "ed25519",
		KeyID:     s.keyID,
		PublicKey: base64.StdEncoding.EncodeToString(s.signerPub),
	}
}

// CreateCheckpoint signs the tenant's current chain head and persists a
// checkpoint. Errors with ErrSigningDisabled when no key is configured,
// or when the tenant has no events to checkpoint.
func (s *Service) CreateCheckpoint(ctx context.Context, tenantID string) (*model.AuditCheckpoint, error) {
	if s.signer == nil {
		return nil, ErrSigningDisabled
	}
	count, head, err := s.repo.HeadAndCount(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("head+count: %w", err)
	}
	if count == 0 || head == "" {
		return nil, fmt.Errorf("no audit events to checkpoint")
	}
	sig := ed25519.Sign(s.signer, CanonicalCheckpointMessage(tenantID, head, count))
	cp := &model.AuditCheckpoint{
		TenantID:   tenantID,
		HeadHash:   head,
		EventCount: count,
		Algo:       "ed25519",
		KeyID:      s.keyID,
		Signature:  base64.StdEncoding.EncodeToString(sig),
	}
	if err := s.repo.InsertCheckpoint(ctx, cp); err != nil {
		return nil, fmt.Errorf("insert checkpoint: %w", err)
	}
	return cp, nil
}

// ListCheckpoints returns the tenant's checkpoints (newest first).
func (s *Service) ListCheckpoints(ctx context.Context, tenantID string, limit int) ([]*model.AuditCheckpoint, error) {
	return s.repo.ListCheckpoints(ctx, tenantID, limit)
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
	// Tenant ID lives at the envelope root (CloudEvents tenantid
	// extension) for outbox-published events; fall back to data.tenant_id
	// for legacy direct-publish callers.
	tenantID := strField(envelope, "tenantid")
	if tenantID == "" {
		tenantID = strField(data, "tenant_id")
	}
	if tenantID == "" {
		return fmt.Errorf("event missing tenant_id")
	}
	action := strField(envelope, "type")
	if action == "" {
		action = subject
	}

	// Actor + IP: prefer the inner payload (an emitter that explicitly
	// included them is the most authoritative source), then fall back
	// to the CloudEvents envelope where the outbox publisher stamps
	// them from the request ctx (pkg/database/outbox.go ActorID/Name +
	// outbox_publisher cloudEvent VDMS* extensions). Pre-plumbing
	// emitters that don't include these in their payload now still
	// get correct actor/IP rows in audit_events.
	actor := strField(data, "actor_id")
	if actor == "" {
		actor = strField(envelope, "vdmsactorid")
	}
	actorName := strField(data, "actor_name")
	if actorName == "" {
		actorName = strField(envelope, "vdmsactorname")
	}
	ipAddr := strField(data, "ip_address")
	if ipAddr == "" {
		ipAddr = strField(envelope, "vdmsclientip")
	}
	userAgent := strField(data, "user_agent")
	if userAgent == "" {
		userAgent = strField(envelope, "vdmsuseragent")
	}

	// Resource type/id: prefer explicit payload fields, else recover
	// from the CloudEvents envelope's `subject` attribute which the
	// outbox publisher formats as "<aggregateType>/<aggregateID>".
	// That format covers every domain event the outbox produces, so
	// audit rows for document / version / workflow / etc. events get
	// resource fields populated without changes to those emitters.
	resourceType := strField(data, "resource_type")
	resourceID := strField(data, "resource_id")
	if resourceType == "" || resourceID == "" {
		if rt, rid, ok := splitEnvelopeSubject(strField(envelope, "subject")); ok {
			if resourceType == "" {
				resourceType = rt
			}
			if resourceID == "" {
				resourceID = rid
			}
		}
	}

	event := &model.AuditEvent{
		ID:            newID(),
		TenantID:      tenantID,
		Actor:         actor,
		ActorName:     actorName,
		Action:        action,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		ResourceTitle: strField(data, "resource_title"),
		Details:       raw,
		IPAddress:     ipAddr,
		UserAgent:     userAgent,
		SourceEvent:   subject,
		CreatedAt:     time.Now().UTC(),
	}

	return s.appendChained(ctx, event)
}

// appendChained links `event` into its tenant's hash chain and persists
// it: acquire the per-tenant lock, read the previous hash, compute this
// event's hash, insert. The shared primitive behind IngestEvent and
// RecordAccessDenied so every appended row — including denials — is
// hash-chained identically. event.TenantID must be set.
func (s *Service) appendChained(ctx context.Context, event *model.AuditEvent) error {
	// Truncate to microseconds BEFORE hashing: Postgres timestamptz has
	// microsecond resolution, so a nanosecond-precision now() would hash
	// one value at ingest and store/return a truncated one — making
	// VerifyIntegrity's re-derived hash mismatch on every event. This was
	// masked while the raw-pool reads failed closed (verify looped over
	// zero rows and passed vacuously); the RLS read fix unmasks it, so
	// the two must land together (issue #74).
	event.CreatedAt = event.CreatedAt.Truncate(time.Microsecond)

	unlock := s.lockTenant(ctx, event.TenantID)
	defer unlock()

	prevHash, err := s.repo.GetLastHash(ctx, event.TenantID)
	if err != nil {
		return fmt.Errorf("get last hash: %w", err)
	}
	event.PreviousHash = prevHash
	event.EventHash = computeEventHash(prevHash, event)

	return s.repo.Insert(ctx, event)
}

// RecordAccessDenied appends a self-audit event when an RBAC gate denies
// access to an audit endpoint (issue #74 — "audit the denial itself").
// The audit trail records every attempt to read or redact it. Recorded
// through the same hash-chain primitive as any other event, so a denial
// is tamper-evident too. Best-effort at the call site: a failure to log
// the denial must not change the 403 the caller already returns.
func (s *Service) RecordAccessDenied(ctx context.Context, tenantID, actor, actorName, action, endpoint, role string) error {
	if tenantID == "" {
		return fmt.Errorf("record access denied: empty tenant")
	}
	details, _ := json.Marshal(map[string]string{
		"endpoint":      endpoint,
		"actual_role":   role,
		"required_role": "admin|owner",
	})
	return s.appendChained(ctx, &model.AuditEvent{
		ID:        newID(),
		TenantID:  tenantID,
		Actor:     actor,
		ActorName: actorName,
		Action:    "audit." + action + ".denied",
		Details:   details,
		CreatedAt: time.Now().UTC(),
	})
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
		expected := expectedHashFor(prevHash, e)
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
	// Chain is internally consistent — now cross-check against the latest
	// signed checkpoint (defends against a full chain recompute by an
	// attacker with DB write access).
	s.verifyLatestCheckpoint(ctx, tenantID, events, result)
	auditVerifyTotal.WithLabelValues(tenantID, "ok").Inc()
	auditVerifyEventsScanned.WithLabelValues(tenantID).Add(float64(len(events)))
	return result, nil
}

// MerkleProofForResource returns a range-scoped Merkle/hash-chain proof over
// every audit event for one resource (e.g. a document). Each event's hash is
// recomputed and compared to the stored value (tamper detection) and a Merkle
// root anchors the set so an external verifier can confirm it independently.
func (s *Service) MerkleProofForResource(ctx context.Context, tenantID, resourceType, resourceID string) (*model.MerkleProof, error) {
	// Single page large enough for any one resource's audit trail.
	events, _, err := s.repo.List(ctx, model.ListFilter{
		TenantID:     tenantID,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		PageSize:     10000,
	})
	if err != nil {
		auditVerifyTotal.WithLabelValues(tenantID, "error").Inc()
		return nil, err
	}
	proof := buildMerkleProof(tenantID, resourceType, resourceID, events)
	if proof.Valid {
		auditVerifyTotal.WithLabelValues(tenantID, "ok").Inc()
	} else {
		auditChainBreakTotal.WithLabelValues(tenantID).Inc()
		auditVerifyTotal.WithLabelValues(tenantID, "break").Inc()
	}
	return proof, nil
}

// verifyLatestCheckpoint loads the tenant's most recent signed checkpoint
// and confirms (a) its Ed25519 signature verifies under our public key
// and (b) the recomputed chain head at its event_count matches the
// signed head_hash. Populates result.Checkpoint* fields. A missing
// checkpoint or disabled signing is reported in CheckpointMessage rather
// than failing the whole verification.
func (s *Service) verifyLatestCheckpoint(ctx context.Context, tenantID string, events []*model.AuditEvent, result *model.IntegrityResult) {
	cp, err := s.repo.LatestCheckpoint(ctx, tenantID)
	if err != nil {
		result.CheckpointMessage = "checkpoint lookup failed: " + err.Error()
		return
	}
	if cp == nil {
		result.CheckpointMessage = "no signed checkpoint"
		return
	}
	result.CheckpointAt = cp.CreatedAt.Format(time.RFC3339)
	result.CheckpointCount = cp.EventCount
	if s.signer == nil || cp.KeyID != s.keyID {
		result.CheckpointMessage = "signing key unavailable to verify checkpoint (key_id " + cp.KeyID + ")"
		return
	}
	sig, derr := base64.StdEncoding.DecodeString(cp.Signature)
	if derr != nil {
		result.CheckpointMessage = "checkpoint signature not decodable"
		return
	}
	if !ed25519.Verify(s.signerPub, CanonicalCheckpointMessage(cp.TenantID, cp.HeadHash, cp.EventCount), sig) {
		result.CheckpointMessage = "checkpoint signature INVALID"
		return
	}
	// Signature authentic — confirm the signed head matches the recomputed
	// chain at event_count (detects deletion/alteration of the first N).
	if cp.EventCount > int64(len(events)) {
		result.CheckpointMessage = fmt.Sprintf("checkpoint covers %d events but only %d present (truncation?)", cp.EventCount, len(events))
		return
	}
	if events[cp.EventCount-1].EventHash != cp.HeadHash {
		result.CheckpointMessage = "chain head at checkpoint does not match signed head (tamper)"
		return
	}
	result.CheckpointValid = true
	result.CheckpointMessage = "signature valid; chain head matches signed checkpoint"
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
	// GDPR erasure mutates HASHED fields (actor/actor_name/ip/user_agent), so it
	// necessarily invalidates the hash chain from the first affected event
	// forward. The old code redacted and left the chain permanently BROKEN —
	// every erasure tripped the p0 audit_chain_break alert and made
	// VerifyIntegrity report tampering. Instead perform an AUTHORIZED re-chain:
	// redact, recompute every event's hash in order, persist, and re-anchor with
	// a fresh signed checkpoint. Held under the per-tenant lock so no concurrent
	// append races the rewrite.
	unlock := s.lockTenant(ctx, tenantID)
	defer unlock()

	n, err := s.repo.AnonymizeSubject(ctx, tenantID, subjectID)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil // nothing redacted → chain untouched
	}

	events, err := s.repo.ListAll(ctx, tenantID) // chain order (created_at, id)
	if err != nil {
		return n, fmt.Errorf("anonymize re-chain load: %w", err)
	}
	prev := ""
	for _, e := range events {
		e.PreviousHash = prev
		e.EventHash = computeEventHash(prev, e)
		prev = e.EventHash
	}
	if err := s.repo.UpdateEventHashes(ctx, tenantID, events); err != nil {
		return n, fmt.Errorf("anonymize re-chain persist: %w", err)
	}

	// The head hash moved, so the prior checkpoint no longer matches. Re-anchor
	// with a fresh checkpoint when signing is enabled (CreateCheckpoint does not
	// re-acquire the per-tenant lock, so this is safe here); ignore the
	// signing-disabled case.
	if _, cErr := s.CreateCheckpoint(ctx, tenantID); cErr != nil && !errors.Is(cErr, ErrSigningDisabled) {
		s.log.Warn().Err(cErr).Str("tenant", tenantID).Msg("audit: re-checkpoint after anonymize failed")
	}
	return n, nil
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
		"dms.tenant.>",    // per-tenant KEK/KMS: dms.tenant.key_rotated.v1
		"dms.irm.>",       // IRM protected-export license lifecycle (§5/§8)
		"dms.signature.>", // signing-ceremony lifecycle (applied/completed/...) — must be in the hash chain
		"dms.policy.>", "dms.permission.>",
		"dms.billing.>", "dms.subscription.>", "dms.usage.>",
		"dms.audit.>",
		"dms.search.>",
		"dms.workflow.>", "dms.task.>",
		"dms.ocr.>", "dms.classify.>", "dms.embed.>", "dms.ner.>",
		"dms.notify.>",
		"dms.sharelink.>", "dms.folder.>", "dms.intelligence.>", "dms.rotation.>",
		// Records management: dms.record.disposed.v1 is the tamper-evident
		// disposition certificate (hash-chained here, verifiable via
		// VerifyIntegrity). dms.record.declared.v1 records the declaration.
		"dms.record.>",
		// E-discovery: dms.export.completed.v1 is the export chain-of-custody.
		"dms.export.>",
		// Identity providers: dms.idp.configured.v1 records IdP registration.
		"dms.idp.>",
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

// hashV2Prefix marks an event hash computed by computeEventHash (which covers
// EVERY content field). Legacy events (no prefix) were hashed by computeHash
// over only prev|tenant|actor|action|resource|created_at, leaving details,
// ip_address, user_agent, actor_name, resource_type, resource_title and
// source_event UNAUTHENTICATED — rewritable undetected. Verification selects
// the algorithm from this prefix, so historical chains stay valid while new
// events get full coverage; the chain linkage (each event commits to the prior
// event's prefixed hash) prevents downgrading a v2 event to the legacy form.
const hashV2Prefix = "v2:"

// computeEventHash hashes prev + every content field of e with length-prefixed
// framing, so no field's bytes can be shifted across a boundary (Details is
// arbitrary JSON that may contain delimiters). This closes the gap where the
// most security-relevant columns were outside the hash, and folds in
// resource_type (which also scopes the Merkle proof bucket).
func computeEventHash(prev string, e *model.AuditEvent) string {
	h := sha256.New()
	wf := func(b []byte) { fmt.Fprintf(h, "%d:", len(b)); h.Write(b) }
	ws := func(s string) { wf([]byte(s)) }
	ws(prev)
	ws(e.ID)
	ws(e.TenantID)
	ws(e.Actor)
	ws(e.ActorName)
	ws(e.Action)
	ws(e.ResourceType)
	ws(e.ResourceID)
	ws(e.ResourceTitle)
	wf(e.Details)
	ws(e.IPAddress)
	ws(e.UserAgent)
	ws(e.SourceEvent)
	ws(e.CreatedAt.Format(time.RFC3339Nano))
	return hashV2Prefix + hex.EncodeToString(h.Sum(nil))
}

// computeHash is the LEGACY chain hash, retained ONLY to verify events written
// before the v2 upgrade. Never use it for new events.
func computeHash(prev, tenantID, actor, action, resourceID string, t time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s", prev, tenantID, actor, action, resourceID, t.Format(time.RFC3339Nano))
	return hex.EncodeToString(h.Sum(nil))
}

// expectedHashFor recomputes the expected hash for e, choosing the algorithm
// from the stored hash's version prefix so legacy and v2 events both verify.
func expectedHashFor(prev string, e *model.AuditEvent) string {
	if strings.HasPrefix(e.EventHash, hashV2Prefix) {
		return computeEventHash(prev, e)
	}
	return computeHash(prev, e.TenantID, e.Actor, e.Action, e.ResourceID, e.CreatedAt)
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

// splitEnvelopeSubject parses a CloudEvents subject formatted as
// "<aggregateType>/<aggregateID>" (the shape the outbox publisher
// emits at pkg/database/outbox_publisher.go) into the parts the
// audit row needs. Returns ok=false for empty / malformed subjects
// so the caller can leave resource fields empty rather than insert
// garbage.
func splitEnvelopeSubject(s string) (resourceType, resourceID string, ok bool) {
	if s == "" {
		return "", "", false
	}
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}
