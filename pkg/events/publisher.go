// Package events wraps NATS JetStream with the CloudEvents v1.0 envelope and
// the VaultDMS stream topology. Services publish via Publisher and consume
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

// CloudEvent is the v1.0 envelope with the VaultDMS-specific extensions.
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

// DefaultStreams is the canonical VaultDMS stream topology, per
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
	{Name: "DOC_EVENTS", Subjects: []string{"dms.document.>", "dms.version.>", "dms.workspace.>"}},
	{Name: "USER_EVENTS", Subjects: []string{"dms.user.>", "dms.session.>", "dms.apikey.>", "dms.auth.>"}},
	{Name: "POLICY_EVENTS", Subjects: []string{"dms.policy.>", "dms.permission.>"}},
	{Name: "BILLING_EVENTS", Subjects: []string{"dms.billing.>", "dms.subscription.>", "dms.usage.>"}},
	{Name: "AUDIT_EVENTS", Subjects: []string{"dms.audit.>"}},
	{Name: "SEARCH_EVENTS", Subjects: []string{"dms.search.>"}},
	{Name: "WORKFLOW_EVENTS", Subjects: []string{"dms.workflow.>", "dms.task.>"}},
	// dms.model.> covers the active-learning lifecycle (ADR 0060):
	// retrain_requested / trained / evaluated / promoted. Without
	// binding, every "Trigger retrain" click left the outbox publisher
	// in an infinite retry loop ("nats: no response from stream").
	{Name: "INTEL_EVENTS", Subjects: []string{"dms.ocr.>", "dms.classify.>", "dms.embed.>", "dms.ner.>", "dms.model.>", "dms.redaction.>"}},
	{Name: "NOTIFY_EVENTS", Subjects: []string{"dms.notify.>"}},
	// Compliance + lifecycle events emitted by services/document/internal/compliance
	// (legal holds) and services/workflow/internal/activities/residency.
	// dms.compliance.> covers the PII/PHI scan emit + admin rescan trigger
	// (ADR 0054); without binding, those events were silently dropped by
	// the broker — discovered when the "Run scan" admin button enqueued
	// dms.compliance.rescan_requested.v1 with no stream to land on.
	{Name: "COMPLIANCE_EVENTS", Subjects: []string{"dms.hold.>", "dms.residency.>", "dms.compliance.>"}},
	// Signature lifecycle events emitted by services/signature and the
	// workflow signature_stub activity (completed, declined).
	{Name: "SIGNATURE_EVENTS", Subjects: []string{"dms.signature.>"}},
	// §17.3 / D10 — annotation CRUD fan-out to collaboration WS.
	{Name: "ANNOTATION_EVENTS", Subjects: []string{"dms.annotation.>"}},
	// Retained alongside the new topology for back-compat with events
	// that still use these prefixes (e.g. dms.sharelink.*, dms.folder.*,
	// dms.intelligence.*). Remove once every emitter is migrated.
	{Name: "LEGACY_EVENTS", Subjects: []string{"dms.sharelink.>", "dms.folder.>", "dms.intelligence.>", "dms.rotation.>"}},
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

// streamReplicas reads VAULTDMS_NATS_REPLICAS (default 1 for dev).
// Prod clusters should set 3; setting > cluster peer count will cause
// AddStream to fail — intentional, forces correct capacity planning.
func streamReplicas() int {
	if v := os.Getenv("VAULTDMS_NATS_REPLICAS"); v != "" {
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
