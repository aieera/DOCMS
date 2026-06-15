package erp

import "context"

// DocumentSource fetches document bytes for events that carry a FileRef instead
// of inline Bytes (e.g. the worker calls the ERP render endpoint).
type DocumentSource interface {
	// Fetch returns the bytes + mime for a file reference.
	Fetch(ctx context.Context, fileRef string) (data []byte, mime string, err error)
}

// Authorizer answers "may this ERP user see this customer's files?" — used by
// the BFF before proxying any per-customer request to SeDoc.
type Authorizer interface {
	CanAccessCustomer(ctx context.Context, userID, customerRef string) (bool, error)
}
