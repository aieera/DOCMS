// Package sync is the integration worker's domain logic: turning a canonical ERP
// event into SeDoc folder provisioning + document create/version, idempotently.
package sync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aieera/sedoc/integration/internal/bucket"
	"github.com/aieera/sedoc/integration/internal/erp"
	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
)

// Config tunes the syncer.
type Config struct {
	WorkspaceID string
	RegionPin   string
	Buckets     int
}

// Syncer applies events to SeDoc.
type Syncer struct {
	st  *store.Store
	doc *sedoc.Client
	src erp.DocumentSource // resolves FileRef → bytes; may be nil if events always inline
	cfg Config
}

// New constructs a Syncer.
func New(st *store.Store, doc *sedoc.Client, src erp.DocumentSource, cfg Config) *Syncer {
	if cfg.RegionPin == "" {
		cfg.RegionPin = "us-east-1"
	}
	if cfg.Buckets <= 0 {
		cfg.Buckets = bucket.DefaultBuckets
	}
	return &Syncer{st: st, doc: doc, src: src, cfg: cfg}
}

// subfolders is the fixed per-customer layout. Keys match document.committed
// doc_type plus "attachments" for /ingest.
var subfolders = []struct{ key, name string }{
	{erp.DocQuote, "Quotes"},
	{erp.DocPO, "POs"},
	{erp.DocSO, "SOs"},
	{erp.DocDO, "DOs"},
	{erp.DocInvoice, "Invoices"},
	{"attachments", "Attachments"},
}

// Handle dispatches a job to its handler and returns a result summary (stored on
// the sync_log row) + an error. The worker classifies the error (retry vs DLQ).
func (s *Syncer) Handle(ctx context.Context, jobID string, e *erp.Event) (any, error) {
	switch e.Kind {
	case erp.KindCustomerCreated, erp.KindCustomerUpdated:
		m, err := s.ensureCustomer(ctx, e.CustomerRef, e.Name)
		if err != nil {
			return nil, err
		}
		return map[string]any{"main_folder_id": m.MainFolderID, "subfolders": m.SubfolderIDs}, nil
	case erp.KindDocumentCommitted:
		return s.handleDocumentCommitted(ctx, jobID, e)
	case erp.KindAttachmentUploaded:
		return s.handleAttachment(ctx, jobID, e)
	default:
		return nil, &erp.ValidationError{Field: "kind", Msg: "unhandled kind: " + e.Kind}
	}
}

// ensureCustomer provisions the bucket + main + 6 subfolders for a customer (or
// renames the main folder if it already exists). Idempotent: an existing mapping
// short-circuits; partial failures converge because the SeDoc folder creates use
// stable logical Idempotency-Keys (a retry replays the same folder id).
func (s *Syncer) ensureCustomer(ctx context.Context, ref, name string) (*store.CustomerMapping, error) {
	existing, err := s.st.GetCustomer(ctx, ref)
	if err == nil {
		// Already provisioned. Only rename when the caller carries an explicit,
		// changed name (customer.created/updated) — the document/attachment
		// paths pass name="" and must NOT clobber the real name with the ref.
		if name != "" && existing.Name != name {
			if rerr := s.doc.RenameFolder(ctx, "rename:"+s.cfg.WorkspaceID+":"+ref, existing.MainFolderID, name); rerr != nil {
				return nil, rerr
			}
			existing.Name = name
			if uerr := s.st.UpdateCustomerName(ctx, ref, name); uerr != nil {
				return nil, uerr
			}
		}
		return existing, nil
	}
	if err != store.ErrNotFound {
		return nil, err
	}
	if name == "" {
		name = ref // first sighting via a doc/attachment event — use the ref until a customer.* event supplies the real name
	}

	ws := s.cfg.WorkspaceID
	label := bucket.Label(ref, s.cfg.Buckets)
	bucketID, err := s.st.EnsureBucket(ctx, ws, label, func() (string, error) {
		return s.doc.EnsureFolder(ctx, "bucket:"+ws+":"+label, ws, nil, label)
	})
	if err != nil {
		return nil, fmt.Errorf("ensure bucket: %w", err)
	}
	mainID, err := s.doc.EnsureFolder(ctx, "main:"+ws+":"+ref, ws, &bucketID, name)
	if err != nil {
		return nil, fmt.Errorf("ensure main folder: %w", err)
	}
	subs := make(map[string]string, len(subfolders))
	for _, sf := range subfolders {
		id, ferr := s.doc.EnsureFolder(ctx, "sub:"+ws+":"+ref+":"+sf.key, ws, &mainID, sf.name)
		if ferr != nil {
			return nil, fmt.Errorf("ensure subfolder %s: %w", sf.key, ferr)
		}
		subs[sf.key] = id
	}
	m := &store.CustomerMapping{
		CustomerRef: ref, Name: name, BucketFolderID: bucketID,
		MainFolderID: mainID, SubfolderIDs: subs,
	}
	if serr := s.st.SaveCustomer(ctx, m); serr != nil {
		return nil, serr
	}
	return m, nil
}

func (s *Syncer) handleDocumentCommitted(ctx context.Context, jobID string, e *erp.Event) (any, error) {
	m, err := s.ensureCustomer(ctx, e.CustomerRef, "")
	if err != nil {
		return nil, err
	}
	folderID, ok := m.SubfolderIDs[e.DocType]
	if !ok {
		return nil, &erp.ValidationError{Field: "doc_type", Msg: "unknown doc_type: " + e.DocType}
	}
	data, mime, err := s.bytes(ctx, e)
	if err != nil {
		return nil, err
	}
	filename := e.Filename
	if filename == "" {
		filename = e.DocType + "-" + e.DocNumber + ".pdf"
	}
	up, err := s.doc.UploadBlob(ctx, jobID, s.cfg.RegionPin, filename, mime, data)
	if err != nil {
		return nil, err
	}
	// external_id is type-prefixed so quote/PO/SO/DO/invoice numbers can't
	// collide across types.
	externalID := e.DocType + "-" + e.DocNumber
	res, err := s.doc.UpsertByExternalKey(ctx, jobID+":upsert", sedoc.UpsertInput{
		WorkspaceID:   s.cfg.WorkspaceID,
		ExternalID:    externalID,
		FolderID:      folderID,
		Title:         externalID,
		DocumentClass: e.DocType,
		BlobChecksum:  up.SHA256,
		BlobRef:       up.ContentBlobID,
		Mime:          mime,
		ChangeSummary: "erp status: " + e.Status,
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Syncer) handleAttachment(ctx context.Context, jobID string, e *erp.Event) (any, error) {
	m, err := s.ensureCustomer(ctx, e.CustomerRef, "")
	if err != nil {
		return nil, err
	}
	folderID := m.SubfolderIDs["attachments"]
	data, mime, err := s.bytes(ctx, e)
	if err != nil {
		return nil, err
	}
	filename := e.Filename
	if filename == "" {
		filename = "attachment"
	}
	up, err := s.doc.UploadBlob(ctx, jobID, s.cfg.RegionPin, filename, mime, data)
	if err != nil {
		return nil, err
	}
	res, err := s.doc.Ingest(ctx, jobID+":ingest", sedoc.IngestInput{
		WorkspaceID:       s.cfg.WorkspaceID,
		FolderID:          folderID,
		TargetCustomerRef: e.CustomerRef,
		BlobChecksum:      up.SHA256,
		ContentBlobID:     up.ContentBlobID,
		Mime:              mime,
		DocumentClass:     e.DocType, // best-guess hint; may be empty
	})
	if err != nil {
		return nil, err
	}
	if res.IngestionItemID != "" {
		if terr := s.st.TrackIngestion(ctx, res.IngestionItemID, e.CustomerRef, res.Status); terr != nil {
			return res, nil // tracking is best-effort
		}
	}
	return res, nil
}

// bytes resolves the document payload: inline event bytes, else the ERP render
// endpoint via the DocumentSource.
func (s *Syncer) bytes(ctx context.Context, e *erp.Event) ([]byte, string, error) {
	mime := e.Mime
	if mime == "" {
		mime = "application/pdf"
	}
	if len(e.Bytes) > 0 {
		return e.Bytes, mime, nil
	}
	if e.FileRef == "" || s.src == nil {
		return nil, "", &erp.ValidationError{Field: "bytes", Msg: "no inline bytes and no file_ref/render source"}
	}
	data, fetchedMime, err := s.src.Fetch(ctx, e.FileRef)
	if err != nil {
		return nil, "", err
	}
	if fetchedMime != "" {
		mime = fetchedMime
	}
	return data, mime, nil
}

// DecodeEvent parses a stored job payload back into an Event.
func DecodeEvent(payload []byte) (*erp.Event, error) {
	var e erp.Event
	if err := json.Unmarshal(payload, &e); err != nil {
		return nil, err
	}
	return &e, nil
}
