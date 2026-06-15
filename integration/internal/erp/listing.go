package erp

import "context"

// The backfill enumerates EXISTING ERP data (vs. live event delivery) and
// synthesizes the canonical events from it. These types model that inventory;
// the bytes for each document are still resolved at sync time via the render
// endpoint (FileRef), exactly like a live document.committed event.

// Customer is one ERP customer in the backfill inventory.
type Customer struct {
	Ref  string `json:"customer_ref"`
	Name string `json:"name"`
}

// DocumentRef is one ERP document (or attachment) in the backfill inventory.
type DocumentRef struct {
	Kind      string `json:"kind"`       // "document" (→ committed/upsert) | "attachment" (→ /ingest)
	DocType   string `json:"doc_type"`   // quote|po|so|do|invoice (an optional class hint for attachments)
	DocNumber string `json:"doc_number"` // the ERP number; external_id = <doc_type>-<doc_number>
	Status    string `json:"status"`
	Filename  string `json:"filename"`
	FileRef   string `json:"file_ref"` // resolved to bytes via the ERP render endpoint
	Mime      string `json:"mime"`
}

// Lister enumerates existing ERP data for backfill. Both the HTTP ERP client and
// the NDJSON file source implement it.
type Lister interface {
	ListCustomers(ctx context.Context) ([]Customer, error)
	ListDocuments(ctx context.Context, customerRef string) ([]DocumentRef, error)
}
