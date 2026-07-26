package autolink

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

// fakeStore records InsertEdge calls and serves canned metadata/targets.
type fakeStore struct {
	meta    map[string]any
	targets map[string][]uuid.UUID // key: rule Kind + ":" + Key
	inserts []insertedEdge
	failOn  string // method name to fail, for error-path tests
}

type insertedEdge struct {
	src, dst   uuid.UUID
	confidence float64
}

func (f *fakeStore) DocumentMetadata(_ context.Context, _ uuid.UUID, _ uuid.UUID) (map[string]any, error) {
	if f.failOn == "meta" {
		return nil, errors.New("db down")
	}
	return f.meta, nil
}

func (f *fakeStore) FindTargets(_ context.Context, _ uuid.UUID, r Rule, _ uuid.UUID) ([]uuid.UUID, error) {
	if f.failOn == "find" {
		return nil, errors.New("db down")
	}
	return f.targets[r.Kind+":"+r.Key], nil
}

func (f *fakeStore) InsertEdge(_ context.Context, _ uuid.UUID, src, dst uuid.UUID, confidence float64, _ map[string]any) error {
	if f.failOn == "insert" {
		return errors.New("db down")
	}
	f.inserts = append(f.inserts, insertedEdge{src, dst, confidence})
	return nil
}

var (
	tenant = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	docA   = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	docB   = uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
)

func TestProcess_PointerCreatesOutboundEdge(t *testing.T) {
	fs := &fakeStore{
		meta: map[string]any{
			"erp_entity_type": "delivery_note",
			"erp_invoice_id":  "inv-9",
		},
		targets: map[string][]uuid.UUID{"pointer:erp_invoice_id": {docB}},
	}
	c := &Consumer{store: fs}
	if err := c.process(context.Background(), tenant, docA); err != nil {
		t.Fatal(err)
	}
	if len(fs.inserts) != 1 {
		t.Fatalf("want 1 edge, got %+v", fs.inserts)
	}
	e := fs.inserts[0]
	if e.src != docA || e.dst != docB || e.confidence != 1.0 {
		t.Fatalf("pointer edge wrong: %+v", e)
	}
}

func TestProcess_ReversePointerCreatesInboundEdge(t *testing.T) {
	fs := &fakeStore{
		meta: map[string]any{
			"erp_entity_type": "invoice",
			"erp_invoice_id":  "inv-9",
		},
		targets: map[string][]uuid.UUID{"reverse-pointer:erp_invoice_id": {docB}},
	}
	c := &Consumer{store: fs}
	if err := c.process(context.Background(), tenant, docA); err != nil {
		t.Fatal(err)
	}
	if len(fs.inserts) != 1 || fs.inserts[0].src != docB || fs.inserts[0].dst != docA {
		t.Fatalf("reverse edge must point matched→me: %+v", fs.inserts)
	}
}

func TestProcess_NumberEdgesAreCanonicalAndDeduped(t *testing.T) {
	fs := &fakeStore{
		meta: map[string]any{
			"reference_number": "INV-123",
			"document_number":  "INV-123", // second rule hits the SAME target
		},
		targets: map[string][]uuid.UUID{
			"number:reference_number": {docB},
			"number:document_number":  {docB},
		},
	}
	c := &Consumer{store: fs}
	if err := c.process(context.Background(), tenant, docB); err != nil {
		t.Fatal(err)
	}
	// Wait — self is docB here and target is docB: self-matches skip.
	if len(fs.inserts) != 0 {
		t.Fatalf("self target must be skipped: %+v", fs.inserts)
	}

	fs2 := &fakeStore{
		meta:    map[string]any{"reference_number": "INV-123", "document_number": "INV-123"},
		targets: map[string][]uuid.UUID{"number:reference_number": {docA}, "number:document_number": {docA}},
	}
	c2 := &Consumer{store: fs2}
	// Processing docB (larger uuid than docA): canonical src must be docA.
	if err := c2.process(context.Background(), tenant, docB); err != nil {
		t.Fatal(err)
	}
	if len(fs2.inserts) != 1 {
		t.Fatalf("same pair via two number keys must insert once: %+v", fs2.inserts)
	}
	if fs2.inserts[0].src != docA || fs2.inserts[0].dst != docB || fs2.inserts[0].confidence != 0.95 {
		t.Fatalf("number edge must be canonical (smaller uuid = src): %+v", fs2.inserts)
	}
}

func TestProcess_StoreErrorPropagates(t *testing.T) {
	for _, failOn := range []string{"meta", "find"} {
		fs := &fakeStore{
			meta:    map[string]any{"erp_entity_type": "invoice", "erp_invoice_id": "x"},
			targets: map[string][]uuid.UUID{"reverse-pointer:erp_invoice_id": {docB}},
			failOn:  failOn,
		}
		c := &Consumer{store: fs}
		if err := c.process(context.Background(), tenant, docA); err == nil {
			t.Fatal(fmt.Errorf("failOn=%s: want error for redelivery", failOn))
		}
	}
}
