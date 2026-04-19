package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every handler here is tested at the validation layer only — we rely on the
// route-registration + header-parse code running before any svc call so a nil
// service is safe. If validation passes, reaching the nil svc.* method panics;
// all tests assert a 4xx short-circuit *before* that happens.

func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	h := &Handler{} // svc nil on purpose
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func TestAuditHandler_ListEvents_RejectsWithoutTenant(t *testing.T) {
	mux := newTestMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if !strings.Contains(body["error"], "X-Tenant-ID") {
		t.Errorf("error should mention tenant: %v", body)
	}
}

func TestAuditHandler_ExportCSV_RejectsWithoutTenant(t *testing.T) {
	mux := newTestMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/export", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestAuditHandler_VerifyIntegrity_RejectsWithoutTenant(t *testing.T) {
	mux := newTestMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audit/verify-integrity", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestAuditHandler_DataSubject_Validation(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		tenant string
		body   string
		want   int
	}{
		{"export: no tenant, no body", "/api/v1/audit/data-subject/export", "", "", http.StatusBadRequest},
		{"export: tenant but empty body", "/api/v1/audit/data-subject/export", "t1", `{}`, http.StatusBadRequest},
		{"export: malformed JSON", "/api/v1/audit/data-subject/export", "t1", `{bad json`, http.StatusBadRequest},
		{"export: tenant + empty subject", "/api/v1/audit/data-subject/export", "t1", `{"subject_id":""}`, http.StatusBadRequest},
		{"anonymize: no tenant", "/api/v1/audit/data-subject/anonymize", "", `{"subject_id":"u1"}`, http.StatusBadRequest},
		{"anonymize: empty subject", "/api/v1/audit/data-subject/anonymize", "t1", `{"subject_id":""}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := newTestMux(t)
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
			if tc.tenant != "" {
				req.Header.Set("X-Tenant-ID", tc.tenant)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("want %d, got %d (body: %s)", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

func TestAuditHandler_Writers_EmitJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusTeapot, map[string]any{"ok": true, "n": 42})
	if w.Code != http.StatusTeapot {
		t.Fatalf("want 418, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: %q", ct)
	}
	var got struct {
		OK bool    `json:"ok"`
		N  float64 `json:"n"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK || got.N != 42 {
		t.Errorf("decoded: %+v", got)
	}
}

func TestAuditHandler_WriteError_ShapeIsConsistent(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusForbidden, "no access")
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["error"] != "no access" {
		t.Errorf("want 'no access', got %q", got["error"])
	}
}
