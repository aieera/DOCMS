// Package erp models the ERP boundary: the 4 canonical events the integration
// consumes, and the client interfaces for fetching document bytes + checking
// customer authorization. The mock ERP (cmd/mockerp) implements the same shape;
// a real ERP swaps in behind these contracts.
package erp

// Event kinds (the canonical contract — see the build prompt §2).
const (
	KindCustomerCreated    = "customer.created"
	KindCustomerUpdated    = "customer.updated"
	KindDocumentCommitted  = "document.committed"
	KindAttachmentUploaded = "attachment.uploaded"
)

// DocType is the ERP document classification.
const (
	DocQuote   = "quote"
	DocPO      = "po"
	DocSO      = "so"
	DocDO      = "do"
	DocInvoice = "invoice"
)

// Event is the envelope every ERP webhook carries. ERPEventID MUST be stable per
// logical event (at-least-once delivery dedup + the seed for the SeDoc
// Idempotency-Key). Fields are populated per Kind.
type Event struct {
	ERPEventID string `json:"erp_event_id"`
	Kind       string `json:"kind"`

	// customer.*
	CustomerRef string `json:"customer_ref"`
	Name        string `json:"name,omitempty"`

	// document.committed
	DocType   string `json:"doc_type,omitempty"`
	DocNumber string `json:"doc_number,omitempty"`
	Status    string `json:"status,omitempty"`

	// document.committed / attachment.uploaded — bytes source. Either inline
	// Bytes (base64 over the wire → decoded to []byte) or a FileRef the worker
	// resolves via the render endpoint.
	Filename string `json:"filename,omitempty"`
	FileRef  string `json:"file_ref,omitempty"`
	Bytes    []byte `json:"bytes,omitempty"`
	Mime     string `json:"mime,omitempty"`
}

// Validate checks the required fields per kind.
func (e *Event) Validate() error {
	if e.ERPEventID == "" {
		return errMissing("erp_event_id")
	}
	switch e.Kind {
	case KindCustomerCreated, KindCustomerUpdated:
		if e.CustomerRef == "" {
			return errMissing("customer_ref")
		}
	case KindDocumentCommitted:
		if e.CustomerRef == "" {
			return errMissing("customer_ref")
		}
		if e.DocType == "" {
			return errMissing("doc_type")
		}
		if e.DocNumber == "" {
			return errMissing("doc_number")
		}
	case KindAttachmentUploaded:
		if e.CustomerRef == "" {
			return errMissing("customer_ref")
		}
	default:
		return &ValidationError{Field: "kind", Msg: "unknown event kind: " + e.Kind}
	}
	return nil
}

// ValidationError is a terminal (non-retryable) ingress error.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return e.Field + " is required"
}

func errMissing(field string) error {
	return &ValidationError{Field: field, Msg: field + " is required"}
}
