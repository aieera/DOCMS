package siem

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestNormalize_Envelope(t *testing.T) {
	raw := `{"data":{"tenant_id":"t1","action":"document.created","actor":"u1","resource_id":"d1","resource_type":"document","event_hash":"abc"}}`
	ev := Normalize("dms.document.created.v1", []byte(raw), fixedTime)
	if ev.TenantID != "t1" || ev.Action != "document.created" || ev.Actor != "u1" {
		t.Fatalf("bad normalize: %+v", ev)
	}
	if ev.ResourceID != "d1" || ev.ResourceType != "document" || ev.EventHash != "abc" {
		t.Fatalf("bad resource/hash: %+v", ev)
	}
	if ev.Subject != "dms.document.created.v1" {
		t.Fatalf("subject not preserved: %s", ev.Subject)
	}
}

func TestNormalize_FlatNoAction(t *testing.T) {
	ev := Normalize("dms.record.declared.v1", []byte(`{"tenant_id":"t2","record_id":"r1"}`), fixedTime)
	if ev.TenantID != "t2" || ev.ResourceID != "r1" {
		t.Fatalf("bad flat normalize: %+v", ev)
	}
	// No action field → falls back to the subject.
	if ev.Action != "dms.record.declared.v1" {
		t.Fatalf("action should default to subject, got %s", ev.Action)
	}
}

func TestNormalize_NonJSON(t *testing.T) {
	ev := Normalize("dms.x.v1", []byte("not json"), fixedTime)
	if ev.Subject != "dms.x.v1" || ev.TenantID != "" {
		t.Fatalf("non-json should keep subject, empty tenant: %+v", ev)
	}
}

func TestSplunkHEC_SuccessAndHeaders(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fwd := NewForwarder(srv.Client())
	ev := Normalize("dms.document.created.v1", []byte(`{"tenant_id":"t1"}`), fixedTime)
	if err := fwd.Forward(context.Background(), Sink{Type: SinkSplunk, Endpoint: srv.URL, Token: "tok"}, ev); err != nil {
		t.Fatalf("splunk forward failed: %v", err)
	}
	if gotAuth != "Splunk tok" {
		t.Fatalf("expected Splunk auth header, got %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"sourcetype":"sedoc:audit"`) {
		t.Fatalf("body missing sourcetype: %s", gotBody)
	}
}

func TestSplunkHEC_ErrorStatusPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("bad token"))
	}))
	defer srv.Close()
	fwd := NewForwarder(srv.Client())
	err := fwd.Forward(context.Background(), Sink{Type: SinkSplunk, Endpoint: srv.URL, Token: "x"}, NormalizedEvent{})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
}

func TestSentinelHEC_BearerAndLogType(t *testing.T) {
	var gotAuth, gotLogType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotLogType = r.Header.Get("Log-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	fwd := NewForwarder(srv.Client())
	if err := fwd.Forward(context.Background(), Sink{Type: SinkSentinel, Endpoint: srv.URL, Token: "beartok"}, NormalizedEvent{}); err != nil {
		t.Fatalf("sentinel forward failed: %v", err)
	}
	if gotAuth != "Bearer beartok" || gotLogType != "SedocAudit" {
		t.Fatalf("bad sentinel headers: auth=%q logtype=%q", gotAuth, gotLogType)
	}
}

func TestForward_UnknownType(t *testing.T) {
	fwd := NewForwarder(nil)
	if err := fwd.Forward(context.Background(), Sink{Type: "bogus"}, NormalizedEvent{}); err == nil {
		t.Fatal("expected error for unknown sink type")
	}
}
