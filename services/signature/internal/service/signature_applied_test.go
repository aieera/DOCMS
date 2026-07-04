package service

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// The full SealVersion path needs a configured DSS sidecar + document service
// (covered by the signing integration tests). These pin the new
// dms.signature.applied.v1 wire contract with pure helpers — matching this
// package's DB-free test convention — so a payload/field/aggregate change that
// would break consumers fails here, not in staging.

func TestSealAggregateID_PrefersVersionThenDocument(t *testing.T) {
	ver := uuid.Must(uuid.NewV7())
	doc := uuid.Must(uuid.NewV7())

	got, err := sealAggregateID(ver.String(), doc.String())
	if err != nil || got != ver {
		t.Fatalf("version id should win: got %v err %v", got, err)
	}
	got, err = sealAggregateID("not-a-uuid", doc.String())
	if err != nil || got != doc {
		t.Fatalf("should fall back to document id: got %v err %v", got, err)
	}
	if _, err := sealAggregateID("nope", "also-nope"); err == nil {
		t.Fatal("expected error when neither id parses")
	}
}

func TestBuildSignatureAppliedEvent_WireContract(t *testing.T) {
	tenant := uuid.Must(uuid.NewV7())
	ver := uuid.Must(uuid.NewV7())
	doc := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())

	evt := buildSignatureAppliedEvent(tenant, ver, doc.String(), ver.String(), user.String(),
		"SeDoc Organizational Seal", "Q2 close", "B-LT", "ab12cd")

	if evt.EventType != "dms.signature.applied.v1" {
		t.Fatalf("event type = %q", evt.EventType)
	}
	if evt.AggregateType != "version" {
		t.Fatalf("aggregate type = %q", evt.AggregateType)
	}
	if evt.AggregateID != ver {
		t.Fatalf("aggregate id = %v, want %v", evt.AggregateID, ver)
	}

	// Flat payload (no double-nesting): every field present at the top level.
	var p map[string]string
	if err := json.Unmarshal(evt.Payload, &p); err != nil {
		t.Fatalf("payload is not a flat JSON object: %v", err)
	}
	want := map[string]string{
		"tenant_id":         tenant.String(),
		"document_id":       doc.String(),
		"version_id":        ver.String(),
		"signer_name":       "SeDoc Organizational Seal",
		"reason":            "Q2 close",
		"level":             "B-LT",
		"fingerprint":       "ab12cd",
		"sealed_by_user_id": user.String(),
	}
	for k, v := range want {
		if p[k] != v {
			t.Fatalf("payload[%q] = %q, want %q", k, p[k], v)
		}
	}
	if _, nested := p["data"]; nested {
		t.Fatal("payload double-nests under data.* — would break webhook consumers")
	}
}
