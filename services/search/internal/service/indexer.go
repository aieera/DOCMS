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
	svc    *Service
	js     nats.JetStreamContext
	log    zerolog.Logger
	subs   []*nats.Subscription
	parent context.Context
}

// NewIndexer creates the consumer. Call Start to begin receiving.
func NewIndexer(svc *Service, js nats.JetStreamContext, log zerolog.Logger) *Indexer {
	return &Indexer{svc: svc, js: js, log: log, parent: context.Background()}
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
	if rb, ok := data["readable_by"].([]any); ok {
		for _, r := range rb {
			if s, ok := r.(string); ok {
				doc.ReadableBy = append(doc.ReadableBy, s)
			}
		}
	}
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
	if tenantID == "" || resourceID == "" {
		_ = msg.Term()
		return
	}

	ctx, cancel := handlerCtx(ix.parent, msg)
	defer cancel()
	var err error
	switch resourceType {
	case "document":
		err = ix.svc.PartialUpdate(ctx, tenantID, resourceID, map[string]any{
			"readable_by": readableBy,
		})
	case "folder":
		err = ix.svc.UpdateReadableByFolder(ctx, tenantID, resourceID, readableBy)
	case "workspace":
		err = ix.svc.UpdateReadableByWorkspace(ctx, tenantID, resourceID, readableBy)
	default:
		ix.log.Warn().Str("type", resourceType).Msg("unknown resource type in permission.changed")
		_ = msg.Term()
		return
	}
	if err != nil {
		ix.log.Error().Err(err).Str("resource", resourceType+"/"+resourceID).Msg("readable_by update failed")
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
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
