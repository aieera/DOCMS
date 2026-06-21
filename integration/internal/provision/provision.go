// Package provision owns customer-folder-tree provisioning in SeDoc: turning a
// customer (ref + name) into a Company-Files → Customer → doc-type-subfolders
// layout, idempotently. Shared by the event worker (internal/sync) and the
// CRM-contract adapter (internal/bff) so both file documents into the same
// per-customer tree.
package provision

import (
	"context"
	"fmt"

	"github.com/aieera/sedoc/integration/internal/bucket"
	"github.com/aieera/sedoc/integration/internal/erp"
	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
)

// Subfolders is the fixed per-customer layout. Keys match document.committed
// doc_type plus "attachments" for uploads.
var Subfolders = []struct{ Key, Name string }{
	{erp.DocQuote, "Quotes"},
	{erp.DocPO, "POs"},
	{erp.DocSO, "SOs"},
	{erp.DocDO, "DOs"},
	{erp.DocInvoice, "Invoices"},
	{"attachments", "Attachments"},
}

// Config tunes provisioning.
type Config struct {
	WorkspaceID string
	// RootFolderID, when set, is the "Company Files" folder under which every
	// customer folder is created (flat: Company Files → Customer → subfolders).
	// Empty = legacy: customers are bucket-sharded under the workspace root.
	RootFolderID string
	Buckets      int
}

// Provisioner creates/refreshes customer folder trees.
type Provisioner struct {
	st  *store.Store
	doc *sedoc.Client
	cfg Config
}

// New constructs a Provisioner.
func New(st *store.Store, doc *sedoc.Client, cfg Config) *Provisioner {
	if cfg.Buckets <= 0 {
		cfg.Buckets = bucket.DefaultBuckets
	}
	return &Provisioner{st: st, doc: doc, cfg: cfg}
}

// EnsureCustomer provisions the customer's main folder + subfolders (or renames
// the main folder if it already exists). Idempotent: an existing mapping
// short-circuits; partial failures converge because the SeDoc folder creates use
// stable logical Idempotency-Keys (a retry replays the same folder id). With
// RootFolderID set, the customer folder is created directly under it (flat);
// otherwise it is bucket-sharded under the workspace root.
func (p *Provisioner) EnsureCustomer(ctx context.Context, ref, name string) (*store.CustomerMapping, error) {
	existing, err := p.st.GetCustomer(ctx, ref)
	if err == nil {
		// Already provisioned. Only rename when the caller carries an explicit,
		// changed name — the document/attachment paths pass name="" and must NOT
		// clobber the real name with the ref.
		if name != "" && existing.Name != name {
			if rerr := p.doc.RenameFolder(ctx, "rename:"+p.cfg.WorkspaceID+":"+ref, existing.MainFolderID, name); rerr != nil {
				return nil, rerr
			}
			existing.Name = name
			if uerr := p.st.UpdateCustomerName(ctx, ref, name); uerr != nil {
				return nil, uerr
			}
		}
		return existing, nil
	}
	if err != store.ErrNotFound {
		return nil, err
	}
	if name == "" {
		name = ref // first sighting via a doc/attachment — use the ref until a real name arrives
	}

	ws := p.cfg.WorkspaceID
	// Root-qualified idempotency keys so the same customer can't collide across
	// different roots (e.g. re-rooting to a new "Company Files" folder).
	rootSeg := p.cfg.RootFolderID
	if rootSeg == "" {
		rootSeg = "ws-root"
	}
	var (
		parentID       *string
		bucketFolderID string
	)
	if p.cfg.RootFolderID != "" {
		rid := p.cfg.RootFolderID
		parentID = &rid
	} else {
		label := bucket.Label(ref, p.cfg.Buckets)
		bid, berr := p.st.EnsureBucket(ctx, ws, label, func() (string, error) {
			return p.doc.EnsureFolder(ctx, "bucket:"+ws+":"+label, ws, nil, label)
		})
		if berr != nil {
			return nil, fmt.Errorf("ensure bucket: %w", berr)
		}
		bucketFolderID = bid
		parentID = &bid
	}
	mainID, err := p.doc.EnsureFolder(ctx, "main:"+ws+":"+rootSeg+":"+ref, ws, parentID, name)
	if err != nil {
		return nil, fmt.Errorf("ensure main folder: %w", err)
	}
	subs := make(map[string]string, len(Subfolders))
	for _, sf := range Subfolders {
		id, ferr := p.doc.EnsureFolder(ctx, "sub:"+ws+":"+rootSeg+":"+ref+":"+sf.Key, ws, &mainID, sf.Name)
		if ferr != nil {
			return nil, fmt.Errorf("ensure subfolder %s: %w", sf.Key, ferr)
		}
		subs[sf.Key] = id
	}
	m := &store.CustomerMapping{
		CustomerRef: ref, Name: name, BucketFolderID: bucketFolderID,
		MainFolderID: mainID, SubfolderIDs: subs,
	}
	if serr := p.st.SaveCustomer(ctx, m); serr != nil {
		return nil, serr
	}
	return m, nil
}

// SubfolderKey maps an ERP doc type (the CRM's erp_entity_type / dms_doc_type)
// to the per-customer subfolder key. Unknown types fall back to "attachments".
func SubfolderKey(docType string) string {
	switch docType {
	case "invoice":
		return erp.DocInvoice
	case "quote":
		return erp.DocQuote
	case "so", "sales_order", "salesorder", "sales-order":
		return erp.DocSO
	case "po", "purchase_order", "purchaseorder":
		return erp.DocPO
	case "do", "delivery_order", "deliveryorder":
		return erp.DocDO
	default:
		return "attachments"
	}
}
