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
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/audit/internal/jsconsumer"
	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/repository"
)

// ErrSigningDisabled is returned by checkpoint operations when no
// Ed25519 signing key is configured (SEDOC_AUDIT_SIGNING_KEY unset).
var ErrSigningDisabled = errors.New("audit checkpoint signing disabled: set SEDOC_AUDIT_SIGNING_KEY")

// ErrActorRequired rejects an erasure without an identifiable caller: the
// audit.subject_erased marker must name who performed it.
var ErrActorRequired = errors.New("anonymize: actor must be the caller's user id")

// Service is the audit service facade.
type Service struct {
	repo  *repository.Repository
	log   zerolog.Logger
	locks sync.Map // per-tenant in-process mutex; the chain lock is in Postgres (M-14)

	// Consumer delivery policy: MaxDeliver and the NAK backoff schedule.
	// Fixed in production (defaultMaxDeliver, nakRetryDelays); tests shrink
	// them.
	maxDeliver int
	nakDelays  []time.Duration
	// DLQ publish retry on the final delivery (see parkOnDLQ).
	dlqRetryDelays   []time.Duration
	dlqRetryAttempts int

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
	Logger zerolog.Logger
	// SigningKeySeedB64 is the base64-encoded 32-byte Ed25519 seed used
	// to sign audit checkpoints. Empty disables checkpoint signing.
	// Generate with:  head -c32 /dev/urandom | base64
	SigningKeySeedB64 string
}

// New creates a Service.
func New(cfg Config) *Service {
	s := &Service{repo: cfg.Repo, log: cfg.Logger, maxDeliver: defaultMaxDeliver, nakDelays: nakRetryDelays,
		dlqRetryDelays: nakRetryDelays[:3], dlqRetryAttempts: 5}
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

	// The in-process mutex only keeps this replica's goroutines from
	// queueing on pool connections; the cross-replica guarantee is the
	// Postgres advisory lock inside AppendChained (M-14).
	unlock := s.lockTenant(event.TenantID)
	defer unlock()

	if err := s.repo.AppendChained(ctx, event, computeEventHash); err != nil {
		return fmt.Errorf("append chained: %w", err)
	}
	return nil
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

// AnonymizeSubject redacts a data subject's PII (GDPR Art.17). actorID /
// actorName identify the caller performing the erasure; they are recorded
// on the audit.subject_erased marker appended to the chain.
//
// Erasure mutates hashed fields (actor, actor_name, ip, user agent, the
// stored envelope), so the chain must be re-computed from the first
// redacted event onward — an authorised re-chain, not tampering. The
// repository does the redaction and the re-chain in one transaction under
// the chain's advisory lock, through the SECURITY DEFINER functions of
// audit migration 000006 (dms_app itself cannot UPDATE the WORM table,
// H-12). The head moves, so a fresh signed checkpoint re-anchors it.
func (s *Service) AnonymizeSubject(ctx context.Context, tenantID, subjectID, actorID, actorName string) (int64, error) {
	if id, err := uuid.Parse(actorID); err != nil || id == uuid.Nil {
		return 0, ErrActorRequired
	}
	unlock := s.lockTenant(tenantID)
	defer unlock()

	marker := func(redacted int64) *model.AuditEvent {
		sum := sha256.Sum256([]byte(subjectID))
		details, _ := json.Marshal(map[string]any{
			"subject_hash":   hex.EncodeToString(sum[:]),
			"redacted_count": redacted,
		})
		return &model.AuditEvent{
			ID:        newID(),
			TenantID:  tenantID,
			Actor:     actorID,
			ActorName: actorName,
			Action:    "audit.subject_erased",
			Details:   details,
			CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		}
	}
	n, err := s.repo.AnonymizeAndRechain(ctx, tenantID, subjectID,
		repository.ChainHashes{Hash: computeEventHash, Expected: expectedHashFor}, marker)
	if err != nil {
		return 0, fmt.Errorf("anonymize subject: %w", err)
	}
	if n == 0 {
		return 0, nil // nothing redacted → chain untouched
	}
	// The redaction is committed. A checkpoint failure must not report the
	// erasure as failed, but it leaves the latest checkpoint signing a head
	// that no longer exists, so it is logged loudly.
	if _, cErr := s.CreateCheckpoint(ctx, tenantID); cErr != nil && !errors.Is(cErr, ErrSigningDisabled) {
		s.log.Error().Err(cErr).Str("tenant", tenantID).Msg("audit: re-checkpoint after anonymize failed; latest checkpoint no longer matches the head")
	}
	return n, nil
}

// ---- NATS subscriber ------------------------------------------------------

// JetStream requires the subscribe subject to be covered by exactly one
// stream. `dms.>` spans many streams, so we subscribe to each stream's
// subject filter individually. Keep this list in sync with
// pkg/events.DefaultStreams.
var auditSubjects = []string{
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

// StartConsumer subscribes to all domain events via wildcard. `parent`
// is the service lifecycle ctx; cancelling it cascades into every
// in-flight handler on shutdown (Wave 6 Prompt 6.4).
func (s *Service) StartConsumer(parent context.Context, js nats.JetStreamContext) error {
	if parent == nil {
		parent = context.Background()
	}
	handler := func(msg *nats.Msg) {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		if corrID := msg.Header.Get("correlation-id"); corrID != "" {
			ctx = auth.SetCorrelationID(ctx, corrID)
		}
		if err := s.IngestEvent(ctx, msg.Subject, msg.Data); err != nil {
			// O-5: JetStream drops a message silently once MaxDeliver is
			// exhausted. On the last delivery, park it on the audit DLQ
			// stream first and ack only once that publish is stored, so
			// a poison event stays replayable instead of vanishing.
			if meta, mErr := msg.Metadata(); mErr == nil && int(meta.NumDelivered) >= s.maxDeliver {
				auditIngestTotal.WithLabelValues("error").Inc()
				auditIngestFailures.WithLabelValues(msg.Subject).Inc()
				if dErr := s.parkOnDLQ(parent, js, msg, meta, err); dErr != nil {
					// Not acked and not NAK'd: JetStream will not redeliver
					// past MaxDeliver, so a NAK here lost the event silently.
					s.log.Error().Err(dErr).AnErr("cause", err).Str("subject", msg.Subject).
						Msg("audit ingest FAILED and the DLQ publish kept failing — event left unacked; it is dropped when AckWait expires")
					return
				}
				s.log.Error().Err(err).Str("subject", msg.Subject).Uint64("delivered", meta.NumDelivered).
					Msg("audit ingest FAILED on the last delivery — event parked on the audit DLQ")
				_ = msg.Ack()
				return
			}
			// BUG-01 — an ingest failure is evidence loss, so it must be
			// counted, not just logged. The partition outage that emptied
			// the audit log produced one of these lines per event for
			// weeks and nothing on any dashboard moved, because the only
			// signal was an error log nobody was tailing.
			//
			// The message is NAK'd (never acked-and-lost), but a bare Nak
			// is redelivered immediately, so the consumer's five delivery
			// attempts burn in milliseconds against a fault — a DB blip,
			// a missing partition — that needs seconds to clear. Spacing
			// the redeliveries turns those five attempts into a retry
			// window of minutes. Delay only; the consumer's MaxDeliver /
			// AckWait config is untouched.
			delivered := s.nakDelayFor(msg)
			auditIngestTotal.WithLabelValues("error").Inc()
			auditIngestFailures.WithLabelValues(msg.Subject).Inc()
			s.log.Error().Err(err).
				Str("subject", msg.Subject).
				Dur("nak_delay", delivered).
				Msg("audit ingest FAILED — event NAK'd for redelivery; the last delivery parks it on the audit DLQ")
			_ = msg.NakWithDelay(delivered)
			return
		}
		auditIngestTotal.WithLabelValues("ok").Inc()
		_ = msg.Ack()
	}
	for _, subj := range auditSubjects {
		durable := "audit-" + strings.ReplaceAll(strings.TrimSuffix(subj, ".>"), ".", "_")
		// M-10: deliver group = durable, so every replica binds and shares.
		if _, err := jsconsumer.QueueSubscribe(js, subj, durable,
			jsconsumer.Options{MaxDeliver: s.maxDeliver, AckWait: 30 * time.Second}, handler,
		); err != nil {
			return fmt.Errorf("subscribe %s: %w", subj, err)
		}
		s.log.Info().Str("subject", subj).Str("durable", durable).Msg("audit subscribed")
	}
	return nil
}

// auditDLQPrefix is where events the consumer gave up on are parked. It is
// bound to the AUDIT_EVENTS_DLQ stream (pkg/events creates it beside
// AUDIT_EVENTS with 30-day retention); the original subject is appended.
const auditDLQPrefix = "dms.dlq.audit_events.ingest."

// toDLQ publishes a permanently failed event to the audit DLQ stream and
// waits for the stream to store it. The original bytes travel base64 so a
// non-JSON poison message survives intact for replay.
func (s *Service) toDLQ(js nats.JetStreamContext, msg *nats.Msg, meta *nats.MsgMetadata, cause error) error {
	payload, err := json.Marshal(map[string]any{
		"original_subject": msg.Subject,
		"error":            cause.Error(),
		"delivered":        meta.NumDelivered,
		"failed_at":        time.Now().UTC().Format(time.RFC3339Nano),
		"raw_b64":          msg.Data,
	})
	if err != nil {
		return err
	}
	out := nats.NewMsg(auditDLQPrefix + msg.Subject)
	out.Data = payload
	// Nats-Msg-Id from the source stream position: a publish that timed
	// out after the DLQ stored it is retried by parkOnDLQ, and the DLQ
	// stream's duplicate window drops the second copy.
	out.Header.Set(nats.MsgIdHdr, fmt.Sprintf("audit-dlq:%s:%d:%s", meta.Stream, meta.Sequence.Stream, msg.Subject))
	_, err = js.PublishMsg(out)
	return err
}

// parkOnDLQ publishes a final-delivery failure to the DLQ, retrying a
// failed publish (DLQ stream briefly unavailable, NATS blip) while
// msg.InProgress() keeps the message pending: this is the last delivery,
// so once it is NAK'd or its AckWait lapses JetStream drops it for good.
func (s *Service) parkOnDLQ(parent context.Context, js nats.JetStreamContext, msg *nats.Msg, meta *nats.MsgMetadata, cause error) error {
	var err error
	for attempt := 0; attempt < s.dlqRetryAttempts; attempt++ {
		if err = s.toDLQ(js, msg, meta, cause); err == nil {
			return nil
		}
		s.log.Error().Err(err).Str("subject", msg.Subject).Int("attempt", attempt+1).
			Msg("audit DLQ publish failed; keeping the event pending and retrying")
		_ = msg.InProgress()
		delay := s.dlqRetryDelays[min(attempt, len(s.dlqRetryDelays)-1)]
		select {
		case <-parent.Done():
			return fmt.Errorf("shutting down: %w", err)
		case <-time.After(delay):
		}
		_ = msg.InProgress()
	}
	return err
}

// defaultMaxDeliver is how many times JetStream delivers one event to the
// audit consumer before it is parked on the DLQ.
const defaultMaxDeliver = 5

// nakRetryDelays is the redelivery backoff applied to a failed audit
// ingest, indexed by (delivery attempt - 1). The tail value is reused for
// any attempt beyond the slice, so the schedule stays defined whatever
// MaxDeliver is configured to.
var nakRetryDelays = []time.Duration{
	2 * time.Second,
	10 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	5 * time.Minute,
}

// nakDelayFor picks the redelivery delay for a failed message from its
// JetStream delivery count. Messages without JetStream metadata (a plain
// core-NATS message, as in unit tests) get the first delay.
func nakDelayFor(msg *nats.Msg) time.Duration { return nakDelayFrom(nakRetryDelays, msg) }

func (s *Service) nakDelayFor(msg *nats.Msg) time.Duration { return nakDelayFrom(s.nakDelays, msg) }

func nakDelayFrom(delays []time.Duration, msg *nats.Msg) time.Duration {
	attempt := uint64(1)
	if meta, err := msg.Metadata(); err == nil && meta.NumDelivered > 0 {
		attempt = meta.NumDelivered
	}
	if attempt > uint64(len(delays)) {
		return delays[len(delays)-1]
	}
	return delays[attempt-1]
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
	// UTC: pgx returns timestamptz in the process's local zone, so a
	// replica with TZ set would otherwise re-derive a different string.
	// Events are stamped in UTC, so existing hashes are unchanged.
	ws(e.CreatedAt.UTC().Format(time.RFC3339Nano))
	return hashV2Prefix + hex.EncodeToString(h.Sum(nil))
}

// computeHash is the LEGACY chain hash, retained ONLY to verify events written
// before the v2 upgrade. Never use it for new events.
func computeHash(prev, tenantID, actor, action, resourceID string, t time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s", prev, tenantID, actor, action, resourceID, t.UTC().Format(time.RFC3339Nano))
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

// lockTenant serialises this replica's appenders for one tenant. It is NOT
// the chain's correctness guarantee — the Postgres advisory lock taken in
// the append transaction is (M-14: the old Redis SETNX here discarded its
// result and deleted the key unconditionally, so it never excluded anyone).
func (s *Service) lockTenant(tenantID string) func() {
	val, _ := s.locks.LoadOrStore(tenantID, &sync.Mutex{})
	mu := val.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
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
