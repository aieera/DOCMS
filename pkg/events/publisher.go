// Package events wraps NATS JetStream with the CloudEvents v1.0 envelope and
// the SeDoc stream topology. Services publish via Publisher and consume
// via Subscriber.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// CloudEvent is the v1.0 envelope with the SeDoc-specific extensions.
type CloudEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject,omitempty"`
	Time            time.Time       `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	TenantID        string          `json:"tenantid,omitempty"`
	RegionPin       string          `json:"regionpin,omitempty"`
	CorrelationID   string          `json:"correlationid,omitempty"`
	Data            json.RawMessage `json:"data"`
}

// NewCloudEvent builds an envelope with a fresh UUIDv7 ID and current time.
func NewCloudEvent(source, eventType, subject string, data []byte) (CloudEvent, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return CloudEvent{}, fmt.Errorf("gen event id: %w", err)
	}
	return CloudEvent{
		SpecVersion:     "1.0",
		ID:              id.String(),
		Source:          source,
		Type:            eventType,
		Subject:         subject,
		Time:            time.Now().UTC(),
		DataContentType: "application/json",
		Data:            data,
	}, nil
}

// StreamSpec defines a JetStream stream created on startup.
// DLQ is auto-created alongside each stream with 720h retention so
// poisoned-message consumers have somewhere to park failed events.
type StreamSpec struct {
	Name     string
	Subjects []string
}

// DefaultStreams is the canonical SeDoc stream topology, per
// `DMS Architecture/final.md` § 4.4 (Wave 5 Prompt 5.2). Subject list
// and stream names here are the SOURCE OF TRUTH — any new NATS
// subject introduced anywhere in the codebase must land under one of
// these prefixes or a new StreamSpec must be added.
//
// The previous topology (DOCUMENTS/AUTH/WORKFLOWS/...) missed
// `dms.user.*`, `dms.policy.*`, `dms.billing.*`, and `dms.session.*`
// — every publish to those subjects was silently dropped by the
// broker because no stream bound them. See `docs/runbooks/05-event-pipeline.md`
// for recovery if an older broker still has the old topology.
var DefaultStreams = []StreamSpec{
	// dms.email.> covers email-ingestion events (ADR 0087) — incoming
	// messages routed into documents land here. Bound to DOC_EVENTS
	// because the lifecycle of an ingested email IS a document
	// lifecycle event from the system's perspective.
	{Name: "DOC_EVENTS", Subjects: []string{"dms.document.>", "dms.version.>", "dms.workspace.>", "dms.email.>"}},
	{Name: "USER_EVENTS", Subjects: []string{"dms.user.>", "dms.session.>", "dms.apikey.>", "dms.auth.>"}},
	{Name: "POLICY_EVENTS", Subjects: []string{"dms.policy.>", "dms.permission.>"}},
	{Name: "BILLING_EVENTS", Subjects: []string{"dms.billing.>", "dms.subscription.>", "dms.usage.>"}},
	{Name: "AUDIT_EVENTS", Subjects: []string{"dms.audit.>"}},
	{Name: "SEARCH_EVENTS", Subjects: []string{"dms.search.>"}},
	// dms.review.> — workflow review approve/reject (ADR 0073). Bound
	// here because reviews ARE workflow steps; without this the
	// approve/reject buttons left the outbox publisher in an infinite
	// retry loop ("nats: no response from stream").
	{Name: "WORKFLOW_EVENTS", Subjects: []string{"dms.workflow.>", "dms.task.>", "dms.review.>"}},
	// INTEL_EVENTS umbrella for every intelligence-pipeline domain:
	//   - dms.model.> covers the active-learning lifecycle (ADR 0060):
	//     retrain_requested / trained / evaluated / promoted.
	//   - dms.routing.> covers smart-routing accept/dismiss (ADR 0064).
	//   - dms.anomaly.> covers workspace-outlier reviews (ADR 0058).
	//   - dms.autotag.> covers tag-suggestion reviews (ADR 0050).
	//   - dms.entity.> covers NER correction events (ADR 0056).
	//   - dms.ner_config.> covers per-tenant LLM-tier key changes
	//     (ADR 0081); single token, sibling of `ner`, so needs its
	//     own binding.
	// Without these bindings each was an infinite outbox-retry bomb
	// halting the entire batch (same shape as dms.ocr_quality before
	// the 2026-05-25 rename).
	// dms.language.> covers lang_detect's dms.language.detected.v1 (ADR 0056).
	// Without this binding it was an infinite outbox-retry bomb halting the
	// batch — same shape as the others above; surfaced once the lang_detect
	// task was repaired (2026-06-05) and actually started emitting.
	{Name: "INTEL_EVENTS", Subjects: []string{
		"dms.ocr.>", "dms.classify.>", "dms.embed.>", "dms.ner.>",
		"dms.model.>", "dms.redaction.>",
		"dms.routing.>", "dms.anomaly.>", "dms.autotag.>",
		"dms.entity.>", "dms.ner_config.>", "dms.language.>",
	}},
	{Name: "NOTIFY_EVENTS", Subjects: []string{"dms.notify.>"}},
	// Compliance + lifecycle events emitted by services/document/internal/compliance
	// (legal holds) and services/workflow/internal/activities/residency.
	// dms.compliance.> covers the PII/PHI scan emit + admin rescan trigger
	// (ADR 0054); without binding, those events were silently dropped by
	// the broker — discovered when the "Run scan" admin button enqueued
	// dms.compliance.rescan_requested.v1 with no stream to land on.
	// dms.retention.> covers archive/dispose-candidate sweep events.
	// dms.dsr.> covers GDPR subject-rights requested/completed/blocked/failed.
	// dms.ediscovery.> covers cross-tenant eDiscovery export emissions.
	{Name: "COMPLIANCE_EVENTS", Subjects: []string{
		"dms.hold.>", "dms.residency.>", "dms.compliance.>",
		"dms.retention.>", "dms.dsr.>", "dms.ediscovery.>",
	}},
	// Signature lifecycle events emitted by services/signature and the
	// workflow signature_stub activity (completed, declined).
	{Name: "SIGNATURE_EVENTS", Subjects: []string{"dms.signature.>"}},
	// Storage lifecycle events emitted by services/storage on the upload
	// path (initiated / completed / deduplicated / quarantined). No consumer
	// today, but the storage service emits them, so they MUST have a stream
	// to land on — otherwise every upload's storage events ("no response from
	// stream") permanently jam the shared outbox batch behind them, blocking
	// the document/version events the OCR + index pipeline depends on.
	{Name: "STORAGE_EVENTS", Subjects: []string{"dms.storage.>"}},
	// WS3 — pre-commit ingestion pipeline. dms.ingestion.> carries the
	// staging-then-route lifecycle (received → processed → routed /
	// needs_review). received is consumed by the intelligence worker
	// (OCR + extract against the blob ref) and processed is consumed by
	// the document service to start the IngestAndRoute Temporal workflow.
	// Without this binding every ingest would jam the shared outbox
	// publisher in a "no response from stream" retry loop, same failure
	// mode the other comments in this list warn about.
	{Name: "INGESTION_EVENTS", Subjects: []string{"dms.ingestion.>"}},
	// §17.3 / D10 — annotation CRUD fan-out to collaboration WS.
	{Name: "ANNOTATION_EVENTS", Subjects: []string{"dms.annotation.>"}},
	// ADR 0066 — threaded comments + reactions. Without binding, every
	// commented-on document blocked its tenant's outbox publisher in an
	// infinite "no response from stream" retry loop — and because the
	// publisher halts the batch on first failure, downstream events
	// (login, audit, notification) silently failed to land. Surfaced as
	// "BUG-20 audit log empty" in QA.
	// dms.coauth.> covers Yjs realtime collab session start/end (ADR 0096).
	{Name: "COLLAB_EVENTS", Subjects: []string{"dms.comment.>", "dms.thread.>", "dms.coauth.>"}},
	// dms.webhook.> covers admin "Send test event" deliveries from the
	// connector service. Lives in LEGACY_EVENTS because the webhook
	// dispatcher is itself a legacy outbound surface (will move to its
	// own stream when the v2 dispatcher lands).
	// Retained alongside the new topology for back-compat with events
	// that still use these prefixes (e.g. dms.sharelink.*, dms.folder.*,
	// dms.intelligence.*). Remove once every emitter is migrated.
	{Name: "LEGACY_EVENTS", Subjects: []string{
		"dms.sharelink.>", "dms.folder.>", "dms.intelligence.>",
		"dms.rotation.>", "dms.webhook.>",
	}},
}

// ConnectNATS dials NATS, initializes JetStream, and declares all default
// streams idempotently (retention 7d, max 10GB per stream, file storage).
func ConnectNATS(url string) (*nats.Conn, nats.JetStreamContext, error) {
	nc, err := nats.Connect(url,
		nats.Name("vaultdms"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("jetstream ctx: %w", err)
	}
	if _, err := EnsureStreams(js, DefaultStreams); err != nil {
		nc.Close()
		return nil, nil, err
	}
	return nc, js, nil
}

// BootstrapReport is the diff returned by EnsureStreams so callers can
// log / print what actually changed on this boot.
type BootstrapReport struct {
	Added   []string
	Updated []string
	Skipped []string // unchanged
	DLQ     []string // DLQ streams touched
}

// streamReplicas reads SEDOC_NATS_REPLICAS (default 1 for dev).
// Prod clusters should set 3; setting > cluster peer count will cause
// AddStream to fail — intentional, forces correct capacity planning.
func streamReplicas() int {
	if v := os.Getenv("SEDOC_NATS_REPLICAS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5 {
			return n
		}
	}
	return 1
}

// buildPrimaryConfig constructs the StreamConfig for a primary stream
// per `final.md` § 4.4: Retention=Limits, MaxAge=168h, Duplicates=2m,
// Discard=Old, Storage=File, Replicas configurable.
func buildPrimaryConfig(s StreamSpec, replicas int) *nats.StreamConfig {
	const tenGB = 10 * 1024 * 1024 * 1024
	return &nats.StreamConfig{
		Name:       s.Name,
		Subjects:   s.Subjects,
		Retention:  nats.LimitsPolicy,
		MaxAge:     168 * time.Hour, // 7 days
		MaxBytes:   tenGB,
		Storage:    nats.FileStorage,
		Replicas:   replicas,
		Discard:    nats.DiscardOld,
		Duplicates: 2 * time.Minute,
	}
}

// buildDLQConfig returns the DLQ sibling of a primary stream:
// no subject binding (manual republish only) and 30-day retention so
// operators have enough time to triage without losing evidence.
func buildDLQConfig(primaryName string, replicas int) *nats.StreamConfig {
	const fiveGB = 5 * 1024 * 1024 * 1024
	return &nats.StreamConfig{
		Name:      primaryName + "_DLQ",
		Subjects:  []string{"dms.dlq." + strings.ToLower(primaryName) + ".>"},
		Retention: nats.LimitsPolicy,
		MaxAge:    720 * time.Hour, // 30 days
		MaxBytes:  fiveGB,
		Storage:   nats.FileStorage,
		Replicas:  replicas,
		Discard:   nats.DiscardOld,
	}
}

// EnsureStreams adds or updates each stream in specs plus the matching
// DLQ stream for each. Idempotent: re-running produces exactly the
// same topology. Returns a diff report callers can log. An error from
// any single stream aborts the whole pass to avoid partial topology.
func EnsureStreams(js nats.JetStreamContext, specs []StreamSpec) (*BootstrapReport, error) {
	replicas := streamReplicas()
	report := &BootstrapReport{}

	for _, s := range specs {
		primary := buildPrimaryConfig(s, replicas)
		if err := ensureOne(js, primary, report, false); err != nil {
			return report, err
		}
		dlq := buildDLQConfig(s.Name, replicas)
		if err := ensureOne(js, dlq, report, true); err != nil {
			return report, err
		}
	}
	return report, nil
}

// ensureOne Add/Update/Skip logic with drift detection. Existing
// streams are compared subject-by-subject; identical = skip, differing
// = update. New = add.
func ensureOne(js nats.JetStreamContext, cfg *nats.StreamConfig, report *BootstrapReport, isDLQ bool) error {
	existing, err := js.StreamInfo(cfg.Name)
	if err != nil {
		// Not found → add.
		if _, err := js.AddStream(cfg); err != nil {
			return fmt.Errorf("add stream %s: %w", cfg.Name, err)
		}
		if isDLQ {
			report.DLQ = append(report.DLQ, cfg.Name)
		} else {
			report.Added = append(report.Added, cfg.Name)
		}
		return nil
	}
	// Exists → diff. Compare subjects only (other fields are already
	// set in prod and we don't want to churn them on every boot).
	if subjectsEqual(existing.Config.Subjects, cfg.Subjects) {
		report.Skipped = append(report.Skipped, cfg.Name)
		return nil
	}
	if _, err := js.UpdateStream(cfg); err != nil {
		return fmt.Errorf("update stream %s: %w", cfg.Name, err)
	}
	if isDLQ {
		report.DLQ = append(report.DLQ, cfg.Name)
	} else {
		report.Updated = append(report.Updated, cfg.Name)
	}
	return nil
}

func subjectsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			return false
		}
	}
	return true
}

// Publisher publishes CloudEvents to JetStream.
type Publisher struct {
	js     nats.JetStreamContext
	source string
}

// NewPublisher constructs a Publisher. source is the CloudEvents source URI
// (usually the service name, e.g. "vaultdms.document").
func NewPublisher(js nats.JetStreamContext, source string) *Publisher {
	return &Publisher{js: js, source: source}
}

// Publish marshals and publishes an event on subject.
func (p *Publisher) Publish(ctx context.Context, subject string, evt CloudEvent) error {
	if evt.Source == "" {
		evt.Source = p.source
	}
	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	_, err = p.js.PublishMsg(&nats.Msg{
		Subject: subject,
		Data:    body,
		Header: nats.Header{
			"Nats-Msg-Id": []string{evt.ID},
			"Ce-Id":       []string{evt.ID},
			"Ce-Type":     []string{evt.Type},
			"Ce-Source":   []string{evt.Source},
		},
	}, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("js publish: %w", err)
	}
	return nil
}
