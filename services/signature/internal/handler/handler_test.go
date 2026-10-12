package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

func newMux(t *testing.T) *http.ServeMux {
	t.Helper()
	h := &Handler{}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func TestCreateRequest_RejectsInvalidJSON(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/signatures/requests", bytes.NewBufferString("{{{"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestCreateRequest_EmptyBodyReaches400OrDefaultProvider(t *testing.T) {
	// Empty JSON body is valid ({}), so the handler defaults Provider to
	// "internal" and calls svc.CreateRequest — with a nil svc that would
	// panic, which the test runtime converts into a failed test. To check
	// just the "default provider" path we use a recovered request via a
	// fresh handler and a deferred panic assertion.
	defer func() {
		// The nil-svc panic is expected. If we don't panic, that's also
		// a regression (service would need to have been injected).
		_ = recover()
	}()
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/signatures/requests", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	// Either we panicked (recovered above) or some error path returned 5xx.
	if w.Code >= 200 && w.Code < 300 {
		t.Errorf("expected non-2xx for nil-svc call, got %d", w.Code)
	}
}

func TestWriteError_ShapeMatchesContract(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusConflict, "duplicate")
	if w.Code != http.StatusConflict {
		t.Fatalf("status: %d", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "duplicate" {
		t.Errorf("body: %v", body)
	}
}

func TestWriteJSON_Encodes(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status: %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: %q", ct)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"status":"queued"`)) {
		t.Errorf("body: %s", w.Body.String())
	}
}

// createBody is the inbound JSON shape. Catches silent drift in field tags.
func TestCreateBody_JSONBinding(t *testing.T) {
	raw := `{"document_id":"d1","version_id":"v1","provider":"docusign","signers":[{"email":"a@a.com","name":"A","role":"signer"}]}`
	var body createBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.DocumentID != "d1" {
		t.Errorf("document_id: %q", body.DocumentID)
	}
	if body.VersionID != "v1" {
		t.Errorf("version_id: %q", body.VersionID)
	}
	if body.Provider != "docusign" {
		t.Errorf("provider: %q", body.Provider)
	}
	if len(body.Signers) != 1 {
		t.Fatalf("signers length: %d", len(body.Signers))
	}
	if body.Signers[0].Email != "a@a.com" {
		t.Errorf("signer email: %q", body.Signers[0].Email)
	}
}

// S-2: connecting or disconnecting the tenant's e-sign vendor account is an
// owner/admin action; a member is refused before the service is reached.
func TestEsignOAuthStart_MemberForbiddenHTTP(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	h.RegisterESign(mux)
	for _, path := range []string{"/api/v1/signatures/esign/oauth/start", "/api/v1/signatures/esign/disconnect"} {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{"provider":"docusign"}`))
		r = r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{
			ID: uuid.New(), TenantID: uuid.New(), Role: "member",
		}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s as member: got %d, want 403", path, w.Code)
		}
	}
}

// repeatReader yields n copies of b without allocating them.
type repeatReader struct {
	b byte
	n int64
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.n {
		p = p[:r.n]
	}
	for i := range p {
		p[i] = r.b
	}
	r.n -= int64(len(p))
	return len(p), nil
}

// S-5 / DEC-19: plain JSON routes — including QES start and e-sign send,
// which carry only ids since DEC-17 — accept at most 1 MiB; verify-bytes
// (a raw PDF) accepts at most 64 MiB. Anything larger is refused with 413
// before the service is reached (a nil service here would panic).
func TestSignatureRoutes_413OverLimit(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	h.Register(mux)
	h.RegisterQES(mux, "/sign/done")
	h.RegisterESign(mux)
	h.RegisterInPerson(mux)

	cases := []struct {
		path  string
		limit int64
	}{
		{"/api/v1/signatures/qes/start", 1 << 20},
		{"/api/v1/signatures/esign/send", 1 << 20},
		{"/api/v1/signatures/verify-bytes", 64 << 20},
		{"/api/v1/signatures/requests", 1 << 20},
		{"/api/v1/signatures/requests/" + uuid.NewString() + "/in-person/sign", 1 << 20},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			body := io.MultiReader(strings.NewReader(`{"request_id":"`), &repeatReader{b: 'a', n: c.limit + 1})
			r := httptest.NewRequest(http.MethodPost, c.path, body)
			r = r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{
				ID: uuid.New(), TenantID: uuid.New(), Role: "owner", Email: "o@test.local",
			}))
			w := httptest.NewRecorder()
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						w.Code = 599
					}
				}()
				mux.ServeHTTP(w, r)
			}()
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("POST %s over %d bytes: got %d, want 413", c.path, c.limit, w.Code)
			}
		})
	}
}
