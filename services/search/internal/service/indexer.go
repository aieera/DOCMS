package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

// indexerHandlerTimeout bounds how long a single index mutation can hold a
// NATS consumer slot. Matches AckWait (60s) with headroom for commit.
const indexerHandlerTimeout = 30 * time.Second

// handlerCtx builds the per-message context: 30s timeout + correlation-id
// propagation from the NATS header, so downstream logs stay threaded.
//
// Wave 6 Prompt 6.4: the parent context is the service lifecycle ctx —
// cancelling it on SIGTERM cascades into every in-flight handler so
// shutdown drains cleanly without leaked goroutines.
func handlerCtx(parent context.Context, msg *nats.Msg) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, indexerHandlerTimeout)
	if corrID := msg.Header.Get("correlation-id"); corrID != "" {
		ctx = auth.SetCorrelationID(ctx, corrID)
	}
	return ctx, cancel
}

// Indexer subscribes to NATS subjects and drives index mutations through
// the Service layer. Each handler acks on success, naks (redelivery) on
// transient errors, and terms malformed messages.
type Indexer struct {
	svc        *Service
	debouncer  *PermissionDebouncer // ADR 0083 — coalesces permission events
	js         nats.JetStreamContext
	log        zerolog.Logger
	subs       []*nats.Subscription
	parent     context.Context
}

// NewIndexer creates the consumer. Call Start to begin receiving.
// debouncer may be nil — when nil, permission events propagate
// synchronously (the legacy path), which is convenient for tests
// that want deterministic timing.
func NewIndexer(svc *Service, debouncer *PermissionDebouncer, js nats.JetStreamContext, log zerolog.Logger) *Indexer {
	return &Indexer{svc: svc, debouncer: debouncer, js: js, log: log, parent: context.Background()}
}

// Start subscribes to all search-relevant subjects. Idempotent if called
// more than once. `parent` is the service lifecycle context — when it
// cancels, every in-flight handler's derived ctx cancels with it.
func (ix *Indexer) Start(parent context.Context) error {
	if parent != nil {
		ix.parent = parent
	}
	subjects := []struct {
		subject string
		handler nats.MsgHandler
	}{
		{"dms.document.created.v1", ix.onDocCreatedOrUpdated},
		{"dms.document.updated.v1", ix.onDocCreatedOrUpdated},
		{"dms.document.deleted.v1", ix.onDocDeleted},
		{"dms.version.ocr_completed.v1", ix.onOCRCompleted},
		{"dms.permission.changed.v1", ix.onPermissionChanged},
		{"dms.version.classified.v1", ix.onClassified},
		{"dms.version.entities_detected.v1", ix.onEntitiesDetected},
		// FIX-5 follow-up: cascade folder deletes propagate to the
		// index via the document_ids list in the event payload so
		// the recipient doesn't need a tree-walk view.
		{"dms.folder.deleted.v1", ix.onFolderDeleted},
		// FIX-4 follow-up: new version → refresh mime_type / size /
		// version_count on the indexed doc. OCR content arrives
		// separately via dms.version.ocr_completed.v1.
		{"dms.version.uploaded.v1", ix.onVersionUploaded},
	}

	for _, s := range subjects {
		durable := "search-" + strings.ReplaceAll(s.subject, ".", "_")
		sub, err := ix.js.Subscribe(s.subject, s.handler,
			nats.Durable(durable),
			nats.ManualAck(),
			nats.MaxDeliver(5),
			nats.AckWait(60_000_000_000), // 60s
		)
		if err != nil {
			return err
		}
		ix.subs = append(ix.subs, sub)
		ix.log.Info().Str("subject", s.subject).Str("durable", durable).Msg("subscribed")
	}
	return nil
}

// Stop drains all subscriptions.
func (ix *Indexer) Stop() {
	for _, s := range ix.subs {
		_ = s.Drain()
	}
}

// ---- handlers -------------------------------------------------------------

func (ix *Indexer) onDocCreatedOrUpdated(msg *nats.Msg) {
	data, ok := ix.parseData(msg)
	if !ok {
		return
	}
	doc := &model.IndexDocument{
		TenantID:       strField(data, "tenant_id"),
		DocumentID:     strField(data, "document_id"),
		WorkspaceID:    strField(data, "workspace_id"),
		FolderID:       strField(data, "folder_id"),
		FolderPath:     strField(data, "folder_path"),
		Title:          strField(data, "title"),
		Description:    strField(data, "description"),
		Content:        strField(data, "content"),
		ContentSnippet: strField(data, "content_snippet"),
		DocumentClass:  strField(data, "document_class"),
		LifecycleState: strField(data, "lifecycle_state"),
		RegionPin:      strField(data, "region_pin"),
		MimeType:       strField(data, "mime_type"),
		CreatedBy:      strField(data, "created_by"),
		CreatedByName:  strField(data, "created_by_name"),
		HasThumbnail:   boolField(data, "has_thumbnail"),
		VersionCount:   intField(data, "version_count"),
	}
	if v, ok := data["size_bytes"].(float64); ok {
		doc.SizeBytes = int64(v)
	}
	if tags, ok := data["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				doc.Tags = append(doc.Tags, s)
			}
		}
	}
	doc.ReadableBy = strSliceField(data, "readable_by")
	// ADR 0083 — when the publisher provides pre-split fields, take
	// them verbatim. When only the legacy mixed field is present
	// (older publishers, e.g. document service before its ADR 0083
	// follow-up), leave the new fields empty — the query bridge
	// clause still matches via `readable_by`.
	doc.ReadableByUsers = strSliceField(data, "readable_by_users")
	doc.ReadableByGroups = strSliceField(data, "readable_by_groups")
	doc.ShareTokens = strSliceField(data, "share_tokens")
	if doc.TenantID == "" || doc.DocumentID == "" {
		ix.log.Warn().Msg("doc event missing tenant_id or document_id")
		_ = msg.Term()
		return
	}
	ctx, cancel := handlerCtx(ix.parent, msg)
	defer cancel()
	if err := ix.svc.IndexDocument(ctx, doc); err != nil {
		ix.log.Error().Err(err).Str("document_id", doc.DocumentID).Msg("index failed")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func (ix *Indexer) onDocDeleted(msg *nats.Msg) {
	data, ok := ix.parseData(msg)
	if !ok {
		return
	}
	tenantID := strField(data, "tenant_id")
	docID := strField(data, "document_id")
	if tenantID == "" || docID == "" {
		_ = msg.Term()
		return
	}
	ctx, cancel := handlerCtx(ix.parent, msg)
	defer cancel()
	if err := ix.svc.DeleteDocument(ctx, tenantID, docID); err != nil {
		ix.log.Error().Err(err).Str("document_id", docID).Msg("delete from index failed")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// onVersionUploaded refreshes per-version fields on the indexed
// document when a new version lands. OCR-derived content arrives
// separately via dms.version.ocr_completed.v1; this handler covers
// the metadata that changes on every new version (mime_type,
// size_bytes, the latest version_id). It does NOT touch readable_by
// — that's owned by the permission/folder pipeline (FIX-4) and a
// new version doesn't change who can see the document.
//
// Idempotent: PartialUpdate with the same payload is a no-op.
func (ix *Indexer) onVersionUploaded(msg *nats.Msg) {
	data, ok := ix.parseData(msg)
	if !ok {
		return
	}
	tenantID := strField(data, "tenant_id")
	docID := strField(data, "document_id")
	if tenantID == "" || docID == "" {
		_ = msg.Term()
		return
	}
	fields := map[string]any{}
	if mime := strField(data, "mime_type"); mime != "" {
		fields["mime_type"] = mime
	}
	if v, ok := data["size_bytes"].(float64); ok {
		fields["size_bytes"] = int64(v)
	}
	if uploaded := strField(data, "uploaded_at"); uploaded != "" {
		fields["updated_at"] = uploaded
	}
	if len(fields) == 0 {
		// Nothing actionable on this event — payload was either
		// minimal or malformed. Ack so it doesn't loop.
		_ = msg.Ack()
		return
	}
	ctx, cancel := handlerCtx(ix.parent, msg)
	defer cancel()
	if err := ix.svc.PartialUpdate(ctx, tenantID, docID, fields); err != nil {
		// 404 (document_missing_exception) is a legitimate state
		// for backfill scenarios: the index doc hasn't been
		// created yet (the dms.document.created.v1 publisher
		// hasn't shipped readable_by for this doc, or the doc
		// pre-dates the search ACL projection fix). NAK-retrying
		// would loop until JetStream gives up; ack so the metric
		// counts a skip rather than a queue stall, and rely on the
		// next document.updated.v1 event to fill the fields.
		if strings.Contains(err.Error(), "document_missing_exception") || strings.Contains(err.Error(), "404") {
			ix.log.Debug().Str("document_id", docID).Msg("version.uploaded: index doc missing; skipping")
			_ = msg.Ack()
			return
		}
		ix.log.Warn().Err(err).Str("document_id", docID).Msg("version.uploaded partial update failed; nak for retry")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// onFolderDeleted fans the document-side cascade out to OpenSearch.
// The document service's SoftDeleteSubtree pre-computed the affected
// document_ids and shipped them on the event payload (FIX-5 follow-up)
// so this handler doesn't need a tree-walk view of folders. Each id
// goes through svc.DeleteDocument independently — same code path as
// onDocDeleted — and we ack only when every delete succeeds, so a
// transient OpenSearch hiccup gets redelivered.
//
// Restore symmetry is harder: re-indexing a restored doc needs the
// full source-of-truth payload (title, content, ACL) which lives in
// Postgres. That's left for a follow-up that adds either a per-doc
// document.restored.v1 event with full payload, or a search→document
// re-fetch path.
func (ix *Indexer) onFolderDeleted(msg *nats.Msg) {
	data, ok := ix.parseData(msg)
	if !ok {
		return
	}
	tenantID := strField(data, "tenant_id")
	if tenantID == "" {
		// Outbox publisher stamps tenant_id on every CloudEvents
		// envelope; missing it means a malformed message we'll
		// never recover.
		_ = msg.Term()
		return
	}
	docIDs := strSliceField(data, "document_ids")
	if len(docIDs) == 0 {
		// Empty subtrees are valid (folder with no docs cascaded)
		// — nothing to do in the index, ack and move on.
		_ = msg.Ack()
		return
	}
	ctx, cancel := handlerCtx(ix.parent, msg)
	defer cancel()
	for _, id := range docIDs {
		if id == "" {
			continue
		}
		if err := ix.svc.DeleteDocument(ctx, tenantID, id); err != nil {
			ix.log.Error().Err(err).Str("document_id", id).Msg("folder cascade: delete from index failed")
			_ = msg.Nak()
			return
		}
	}
	_ = msg.Ack()
}

func (ix *Indexer) onOCRCompleted(msg *nats.Msg) {
	data, ok := ix.parseData(msg)
	if !ok {
		return
	}
	tenantID := strField(data, "tenant_id")
	docID := strField(data, "document_id")
	content := strField(data, "content")
	if tenantID == "" || docID == "" {
		_ = msg.Term()
		return
	}
	fields := map[string]any{"content": content}
	if len(content) > 500 {
		fields["content_snippet"] = content[:500]
	} else {
		fields["content_snippet"] = content
	}
	ctx, cancel := handlerCtx(ix.parent, msg)
	defer cancel()
	if err := ix.svc.PartialUpdate(ctx, tenantID, docID, fields); err != nil {
		ix.log.Error().Err(err).Str("document_id", docID).Msg("OCR content update failed")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func (ix *Indexer) onPermissionChanged(msg *nats.Msg) {
	data, ok := ix.parseData(msg)
	if !ok {
		return
	}
	tenantID := strField(data, "tenant_id")
	resourceType := strField(data, "resource_type")
	resourceID := strField(data, "resource_id")
	readableBy := strSliceField(data, "readable_by")
	// ADR 0083 — when the publisher provides pre-split user/group
	// sets, propagate them too. Legacy publishers only send the
	// mixed `readable_by` array; in that case the new fields stay
	// untouched on the index doc.
	readableByUsers := strSliceField(data, "readable_by_users")
	readableByGroups := strSliceField(data, "readable_by_groups")
	if tenantID == "" || resourceID == "" {
		_ = msg.Term()
		return
	}

	fields := map[string]any{"readable_by": readableBy}
	if readableByUsers != nil {
		fields["readable_by_users"] = readableByUsers
	}
	if readableByGroups != nil {
		fields["readable_by_groups"] = readableByGroups
	}

	if resourceType != "document" && resourceType != "folder" && resourceType != "workspace" {
		ix.log.Warn().Str("type", resourceType).Msg("unknown resource type in permission.changed")
		_ = msg.Term()
		return
	}

	// ADR 0083 — debounced batch path. The flusher loop owns the
	// actual OpenSearch update; we ack the NATS message immediately
	// because at-least-once delivery on the same key would just be
	// coalesced anyway.
	//
	// Acking before the index write lands is safe here: the
	// debouncer's flushAll() drains on shutdown, so a SIGTERM mid-
	// flight doesn't lose work.
	if ix.debouncer != nil {
		// Parse the CloudEvents `time` field if present so propagation
		// lag is measured against when the publisher emitted the
		// change, not when the subscriber happened to poll.
		var emittedAt time.Time
		if t, _ := parseCloudEventTime(msg); !t.IsZero() {
			emittedAt = t
		}
		ix.debouncer.Submit(tenantID, resourceType, resourceID, fields, emittedAt)
		_ = msg.Ack()
		return
	}

	// Synchronous path — kept for tests + deploys that haven't wired
	// the debouncer yet.
	ctx, cancel := handlerCtx(ix.parent, msg)
	defer cancel()
	var err error
	switch resourceType {
	case "document":
		err = ix.svc.PartialUpdate(ctx, tenantID, resourceID, fields)
	case "folder":
		err = ix.svc.UpdateReadableByFolder(ctx, tenantID, resourceID, readableBy)
	case "workspace":
		err = ix.svc.UpdateReadableByWorkspace(ctx, tenantID, resourceID, readableBy)
	}
	if err != nil {
		ix.log.Error().Err(err).Str("resource", resourceType+"/"+resourceID).Msg("readable_by update failed")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// parseCloudEventTime pulls the `time` field out of the CloudEvents
// envelope. Returns zero time on missing/malformed — caller treats
// that as "use now()" rather than skipping the propagation.
func parseCloudEventTime(msg *nats.Msg) (time.Time, error) {
	var env map[string]any
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return time.Time{}, err
	}
	s, _ := env["time"].(string)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

func (ix *Indexer) onClassified(msg *nats.Msg) {
	data, ok := ix.parseData(msg)
	if !ok {
		return
	}
	tenantID := strField(data, "tenant_id")
	docID := strField(data, "document_id")
	class := strField(data, "document_class")
	if tenantID == "" || docID == "" {
		_ = msg.Term()
		return
	}
	ctx, cancel := handlerCtx(ix.parent, msg)
	defer cancel()
	if err := ix.svc.PartialUpdate(ctx, tenantID, docID, map[string]any{"document_class": class}); err != nil {
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func (ix *Indexer) onEntitiesDetected(msg *nats.Msg) {
	data, ok := ix.parseData(msg)
	if !ok {
		return
	}
	tenantID := strField(data, "tenant_id")
	docID := strField(data, "document_id")
	if tenantID == "" || docID == "" {
		_ = msg.Term()
		return
	}
	entities := &model.ExtractedEntities{
		People:        strSliceField(data, "people"),
		Organizations: strSliceField(data, "organizations"),
		Locations:     strSliceField(data, "locations"),
		Dates:         strSliceField(data, "dates"),
		Amounts:       strSliceField(data, "amounts"),
	}
	ctx, cancel := handlerCtx(ix.parent, msg)
	defer cancel()
	if err := ix.svc.PartialUpdate(ctx, tenantID, docID, map[string]any{
		"extracted_entities": entities,
	}); err != nil {
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// ---- parse helpers --------------------------------------------------------

func (ix *Indexer) parseData(msg *nats.Msg) (map[string]any, bool) {
	var envelope map[string]any
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		ix.log.Error().Err(err).Msg("invalid event json")
		_ = msg.Term()
		return nil, false
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		data = envelope
	}
	// Outbox-published events carry tenant_id at the envelope root as
	// the CloudEvents `tenantid` extension, not inside data. Hoist it
	// so handlers can read it the same way regardless of publisher.
	if _, present := data["tenant_id"]; !present {
		if tid, ok := envelope["tenantid"].(string); ok && tid != "" {
			data["tenant_id"] = tid
		}
	}
	return data, true
}

func strField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func intField(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func strSliceField(m map[string]any, key string) []string {
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
