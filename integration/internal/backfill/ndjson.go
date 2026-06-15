package backfill

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/aieera/sedoc/integration/internal/erp"
)

// NDJSONSource is an erp.Lister backed by a newline-delimited JSON file — an
// alternative backfill inventory when there's no ERP listing API (e.g. a
// one-off export). Each line is one record:
//
//	{"type":"customer","customer_ref":"CUST-1","name":"Acme"}
//	{"type":"document","customer_ref":"CUST-1","doc_type":"invoice","doc_number":"188","status":"confirmed","file_ref":"f-1"}
//	{"type":"attachment","customer_ref":"CUST-1","filename":"contract.pdf","file_ref":"f-2"}
//
// A "document"/"attachment" line for an unseen customer implicitly registers that
// customer (name defaults to the ref). Bytes are still resolved at sync time via
// the ERP render endpoint (file_ref), as for live events.
type NDJSONSource struct {
	customers []erp.Customer
	docs      map[string][]erp.DocumentRef
}

type ndjsonRecord struct {
	Type        string `json:"type"`
	CustomerRef string `json:"customer_ref"`
	Name        string `json:"name"`
	DocType     string `json:"doc_type"`
	DocNumber   string `json:"doc_number"`
	Status      string `json:"status"`
	Filename    string `json:"filename"`
	FileRef     string `json:"file_ref"`
	Mime        string `json:"mime"`
}

// NewNDJSONSource parses an NDJSON inventory into memory, preserving the
// first-seen order of customers.
func NewNDJSONSource(r io.Reader) (*NDJSONSource, error) {
	s := &NDJSONSource{docs: map[string][]erp.DocumentRef{}}
	seen := map[string]bool{}
	register := func(ref, name string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		s.customers = append(s.customers, erp.Customer{Ref: ref, Name: name})
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var rec ndjsonRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("ndjson line %d: %w", line, err)
		}
		if rec.CustomerRef == "" {
			return nil, fmt.Errorf("ndjson line %d: customer_ref required", line)
		}
		switch rec.Type {
		case "customer":
			register(rec.CustomerRef, rec.Name)
		case "document", "attachment":
			register(rec.CustomerRef, "")
			s.docs[rec.CustomerRef] = append(s.docs[rec.CustomerRef], erp.DocumentRef{
				Kind:      rec.Type,
				DocType:   rec.DocType,
				DocNumber: rec.DocNumber,
				Status:    rec.Status,
				Filename:  rec.Filename,
				FileRef:   rec.FileRef,
				Mime:      rec.Mime,
			})
		default:
			return nil, fmt.Errorf("ndjson line %d: unknown type %q", line, rec.Type)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

// ListCustomers implements erp.Lister.
func (s *NDJSONSource) ListCustomers(_ context.Context) ([]erp.Customer, error) {
	return s.customers, nil
}

// ListDocuments implements erp.Lister.
func (s *NDJSONSource) ListDocuments(_ context.Context, customerRef string) ([]erp.DocumentRef, error) {
	return s.docs[customerRef], nil
}
