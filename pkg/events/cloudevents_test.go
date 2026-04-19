package events

import (
	"encoding/json"
	"testing"
	"time"
)

// The CloudEvents envelope is the wire contract between every service.
// Consumers (search indexer, audit) parse by name — a silent tag rename
// here breaks everything downstream.

func TestNewCloudEvent_PopulatesRequiredFields(t *testing.T) {
	data := []byte(`{"k":"v"}`)
	evt, err := NewCloudEvent("dms.document", "dms.document.created.v1", "doc/123", data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.SpecVersion != "1.0" {
		t.Errorf("specversion: %q", evt.SpecVersion)
	}
	if evt.ID == "" {
		t.Error("ID must be populated")
	}
	if evt.Source != "dms.document" {
		t.Errorf("source: %q", evt.Source)
	}
	if evt.Type != "dms.document.created.v1" {
		t.Errorf("type: %q", evt.Type)
	}
	if evt.Subject != "doc/123" {
		t.Errorf("subject: %q", evt.Subject)
	}
	if evt.DataContentType != "application/json" {
		t.Errorf("datacontenttype: %q", evt.DataContentType)
	}
	if time.Since(evt.Time) > 2*time.Second {
		t.Errorf("time: %v (expected now)", evt.Time)
	}
	if string(evt.Data) != `{"k":"v"}` {
		t.Errorf("data mismatch: %s", evt.Data)
	}
}

func TestNewCloudEvent_UniqueIDs(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		e, err := NewCloudEvent("s", "t", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[e.ID]; dup {
			t.Fatalf("duplicate id: %s", e.ID)
		}
		seen[e.ID] = struct{}{}
	}
}

func TestCloudEvent_JSONTagsMatchSpec(t *testing.T) {
	// The on-the-wire field names are the CloudEvents v1.0 spec names.
	// Any drift breaks consumers — pin every tag.
	evt := CloudEvent{
		SpecVersion:     "1.0",
		ID:              "id-1",
		Source:          "s",
		Type:            "t",
		Subject:         "subj",
		Time:            time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		DataContentType: "application/json",
		TenantID:        "tenant-1",
		RegionPin:       "us-east-1",
		CorrelationID:   "corr-1",
		Data:            json.RawMessage(`{"x":1}`),
	}
	b, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, key := range []string{
		"specversion", "id", "source", "type", "subject", "time",
		"datacontenttype", "tenantid", "regionpin", "correlationid", "data",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("wire field %q missing", key)
		}
	}
}

func TestCloudEvent_OptionalFieldsOmittedWhenEmpty(t *testing.T) {
	evt, _ := NewCloudEvent("s", "t", "", []byte(`{}`))
	// TenantID, RegionPin, CorrelationID all have `omitempty` — absent
	// from the JSON when not set. Subject also omitempty.
	b, _ := json.Marshal(evt)
	s := string(b)
	for _, field := range []string{`"tenantid"`, `"regionpin"`, `"correlationid"`, `"subject"`} {
		if containsAny(s, field) {
			t.Errorf("%s should be omitted when empty: %s", field, s)
		}
	}
}

func TestDefaultStreams_CoverageOfSubjectNamespaces(t *testing.T) {
	// Each stream must cover a distinct top-level namespace. If two
	// streams listen to overlapping subjects, ordering semantics break.
	seen := map[string]string{}
	for _, s := range DefaultStreams {
		for _, sub := range s.Subjects {
			if existing, dup := seen[sub]; dup {
				t.Errorf("subject %q claimed by both %s and %s", sub, existing, s.Name)
			}
			seen[sub] = s.Name
		}
	}
	// Sanity: the canonical streams per Wave 5 Prompt 5.2 are all here.
	want := []string{"DOC_EVENTS", "USER_EVENTS", "POLICY_EVENTS", "BILLING_EVENTS",
		"AUDIT_EVENTS", "SEARCH_EVENTS", "WORKFLOW_EVENTS", "INTEL_EVENTS", "NOTIFY_EVENTS"}
	present := map[string]bool{}
	for _, s := range DefaultStreams {
		present[s.Name] = true
	}
	for _, w := range want {
		if !present[w] {
			t.Errorf("canonical stream %q missing from DefaultStreams", w)
		}
	}
}

func containsAny(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 &&
		indexOf(haystack, needle) >= 0
}

func indexOf(hay, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
