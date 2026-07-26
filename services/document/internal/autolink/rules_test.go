package autolink

import "testing"

// The linker is deliberately GENERIC: any erp_<entity>_id pointer key
// relates documents regardless of type (delivery note→invoice,
// payment→invoice, invoice→quote, …), and shared document-number
// metadata relates standalone uploads. No entity names are hardcoded.

func rulesByKind(rs []Rule, kind string) []Rule {
	var out []Rule
	for _, r := range rs {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func findRule(t *testing.T, rs []Rule, kind, key string) Rule {
	t.Helper()
	for _, r := range rs {
		if r.Kind == kind && r.Key == key {
			return r
		}
	}
	t.Fatalf("no %s rule for key %q in %+v", kind, key, rs)
	return Rule{}
}

func TestDeriveRules_DeliveryNotePointers(t *testing.T) {
	rs := DeriveRules(map[string]any{
		"erp_entity_type":      "delivery_note",
		"erp_delivery_note_id": "dn-1",
		"erp_delivery_number":  "DN-100",
		"erp_invoice_id":       "inv-9",
		"erp_invoice_number":   "INV-123",
		"erp_sales_order_id":   "so-3",
	})

	// Outbound pointers to the invoice and the sales order.
	inv := findRule(t, rs, "pointer", "erp_invoice_id")
	if inv.EntityType != "invoice" || inv.Value != "inv-9" || !inv.Outbound || inv.Confidence != 1.0 {
		t.Fatalf("invoice pointer wrong: %+v", inv)
	}
	so := findRule(t, rs, "pointer", "erp_sales_order_id")
	if so.EntityType != "sales_order" || so.Value != "so-3" {
		t.Fatalf("sales order pointer wrong: %+v", so)
	}

	// Its OWN id key must not become an outbound pointer to itself —
	// it becomes the reverse rule instead (others pointing at me).
	for _, r := range rulesByKind(rs, "pointer") {
		if r.Key == "erp_delivery_note_id" {
			t.Fatalf("own identity key must not be an outbound pointer: %+v", r)
		}
	}
	rev := findRule(t, rs, "reverse-pointer", "erp_delivery_note_id")
	if rev.Value != "dn-1" || rev.Outbound || rev.EntityType != "delivery_note" {
		t.Fatalf("reverse pointer wrong: %+v", rev)
	}

	// Number keys produce number rules at 0.95.
	num := findRule(t, rs, "number", "erp_invoice_number")
	if num.Value != "INV-123" || num.Confidence != 0.95 {
		t.Fatalf("number rule wrong: %+v", num)
	}
}

func TestDeriveRules_InvoiceIsSymmetricTarget(t *testing.T) {
	rs := DeriveRules(map[string]any{
		"erp_entity_type":    "invoice",
		"erp_invoice_id":     "inv-9",
		"erp_invoice_number": "INV-123",
	})
	rev := findRule(t, rs, "reverse-pointer", "erp_invoice_id")
	if rev.Value != "inv-9" || rev.Outbound {
		t.Fatalf("invoice must match docs pointing at it: %+v", rev)
	}
	if len(rulesByKind(rs, "pointer")) != 0 {
		t.Fatalf("invoice with no foreign keys must have no outbound pointers: %+v", rs)
	}
}

func TestDeriveRules_StandaloneNumberKeys(t *testing.T) {
	rs := DeriveRules(map[string]any{
		"reference_number": "INV-123",
		"document_number":  "DN-100",
	})
	findRule(t, rs, "number", "reference_number")
	findRule(t, rs, "number", "document_number")
	if len(rulesByKind(rs, "pointer"))+len(rulesByKind(rs, "reverse-pointer")) != 0 {
		t.Fatalf("no pointer rules without erp ids: %+v", rs)
	}
}

func TestDeriveRules_SkipsEmptyAndNonString(t *testing.T) {
	rs := DeriveRules(map[string]any{
		"erp_entity_type":  "invoice",
		"erp_invoice_id":   "",              // empty → skip
		"erp_quote_id":     42,              // non-string → skip
		"reference_number": "  ",            // blank → skip
		"erp_amount_cents": float64(120000), // not an id/number key
	})
	if len(rs) != 0 {
		t.Fatalf("expected no rules, got %+v", rs)
	}
}
