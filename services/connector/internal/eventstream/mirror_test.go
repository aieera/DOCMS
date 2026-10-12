package eventstream

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// fakeJS records mirror publishes. Only the methods the mirror touches are
// implemented; anything else panics via the nil embedded interface, which
// is itself an assertion that the mirror stays on this narrow surface.
type fakeJS struct {
	nats.JetStreamContext
	published []*nats.Msg
	streams   map[string]bool
}

func (f *fakeJS) StreamInfo(name string, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	if f.streams[name] {
		return &nats.StreamInfo{}, nil
	}
	return nil, nats.ErrStreamNotFound
}

func (f *fakeJS) AddStream(cfg *nats.StreamConfig, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	if f.streams == nil {
		f.streams = map[string]bool{}
	}
	f.streams[cfg.Name] = true
	return &nats.StreamInfo{Config: *cfg}, nil
}

func (f *fakeJS) PublishMsg(m *nats.Msg, _ ...nats.PubOpt) (*nats.PubAck, error) {
	f.published = append(f.published, m)
	return &nats.PubAck{}, nil
}

const mirrorTenant = "3b0e6c8e-2f0f-4b9e-8b0a-6c1d2e3f4a5b"

func newMirrorSvc(js nats.JetStreamContext) *Service {
	return &Service{js: js, log: zerolog.Nop(), streamSet: map[string]struct{}{}}
}

func mirrorEnvelope(t *testing.T, eventType string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"specversion": "1.0",
		"id":          "e1",
		"type":        eventType,
		"tenantid":    mirrorTenant,
		"tenant_id":   mirrorTenant,
		"data":        map[string]any{"tenant_id": mirrorTenant, "token": "plaintext-reset-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMirror_SkipsNonSubscribable(t *testing.T) {
	for _, subj := range []string{
		"dms.auth.password_reset_requested.v1",
		"dms.session.created.v1",
		"dms.apikey.issued.v1",
		"dms.user.invited.v1",
		"dms.audit.event.v1",
		"dms.billing.invoice_paid.v1",
	} {
		js := &fakeJS{}
		s := newMirrorSvc(js)
		s.handleUpstream(context.Background(), &nats.Msg{Subject: subj, Data: mirrorEnvelope(t, subj), Header: nats.Header{}})
		if len(js.published) != 0 {
			t.Fatalf("mirror forwarded non-subscribable %s to %s", subj, js.published[0].Subject)
		}
	}
	// Allowed subject, auth type in the envelope: still skipped.
	js := &fakeJS{}
	s := newMirrorSvc(js)
	s.handleUpstream(context.Background(), &nats.Msg{
		Subject: "dms.document.created.v1",
		Data:    mirrorEnvelope(t, "dms.auth.password_reset_requested.v1"),
		Header:  nats.Header{},
	})
	if len(js.published) != 0 {
		t.Fatalf("mirror forwarded an auth-typed envelope on a document subject")
	}
}

func TestMirror_SubscribesOnlySubscribableFamilies(t *testing.T) {
	for _, f := range mirrorSubjects() {
		switch f {
		case "dms.auth.>", "dms.session.>", "dms.apikey.>", "dms.user.>", "dms.audit.>":
			t.Fatalf("mirror subscribes to internal family %s", f)
		}
	}
}

// Events mirrored before the allow-list existed stay in the per-tenant
// stream for the retention window; the read paths (polling + SSE tail)
// must not serve them.
func TestStreamRead_FiltersRetainedInternalEvents(t *testing.T) {
	cases := map[string]bool{
		"tenant." + mirrorTenant + ".events.document.created.v1":              true,
		"tenant." + mirrorTenant + ".events.signature.completed.v1":           true,
		"tenant." + mirrorTenant + ".events.auth.password_reset_requested.v1": false,
		"tenant." + mirrorTenant + ".events.session.created.v1":               false,
		"tenant." + mirrorTenant + ".events.user.invited.v1":                  false,
		"garbage": false,
	}
	for subj, want := range cases {
		if got := servable(subj); got != want {
			t.Errorf("servable(%q) = %v, want %v", subj, got, want)
		}
	}
}

// N-2: pkg/events CloudEvents carry the tenant as top-level `tenantid`;
// the mirror used to read only `tenant_id` and dropped nearly every event.
func TestMirror_ForwardsTenantidEnvelope(t *testing.T) {
	js := &fakeJS{}
	s := newMirrorSvc(js)
	data, _ := json.Marshal(map[string]any{
		"specversion": "1.0",
		"id":          "e1",
		"type":        "dms.document.created.v1",
		"tenantid":    mirrorTenant,
		"data":        map[string]any{"document_id": "d1"},
	})
	s.handleUpstream(context.Background(), &nats.Msg{Subject: "dms.document.created.v1", Data: data, Header: nats.Header{}})
	if len(js.published) != 1 {
		t.Fatalf("want 1 mirrored event, got %d", len(js.published))
	}
	if want := "tenant." + mirrorTenant + ".events.document.created.v1"; js.published[0].Subject != want {
		t.Fatalf("mirrored to %q, want %q", js.published[0].Subject, want)
	}
}

// The tenant becomes a NATS subject token, so anything but a UUID (dots,
// wildcards, another tenant's subtree) is refused.
func TestMirror_RejectsNonUUIDTenant(t *testing.T) {
	for _, tenant := range []string{"a.b", "*", ">", "x", mirrorTenant + ".events.>", ""} {
		js := &fakeJS{}
		s := newMirrorSvc(js)
		data, _ := json.Marshal(map[string]any{"type": "dms.document.created.v1", "tenantid": tenant,
			"data": map[string]any{"tenant_id": tenant}})
		s.handleUpstream(context.Background(), &nats.Msg{Subject: "dms.document.created.v1", Data: data, Header: nats.Header{}})
		if len(js.published) != 0 {
			t.Errorf("tenant %q: mirrored to %s", tenant, js.published[0].Subject)
		}
	}
	// Legacy shape: tenant only under data.tenant_id still works.
	js := &fakeJS{}
	s := newMirrorSvc(js)
	data, _ := json.Marshal(map[string]any{"type": "dms.folder.created.v1", "data": map[string]any{"tenant_id": mirrorTenant}})
	s.handleUpstream(context.Background(), &nats.Msg{Subject: "dms.folder.created.v1", Data: data, Header: nats.Header{}})
	if len(js.published) != 1 {
		t.Fatalf("legacy data.tenant_id envelope not mirrored")
	}
}
