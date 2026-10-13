package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/workflow/internal/service"
)

func newMux(t *testing.T) *http.ServeMux {
	t.Helper()
	h := &Handler{}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func TestListDefinitions_RequiresTenant(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/definitions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestCreateDefinition_HeaderAndBodyValidation(t *testing.T) {
	cases := []struct {
		name     string
		tenantID string
		userID   string
		body     string
		want     int
	}{
		{"no tenant", "", "u1", `{"name":"x"}`, http.StatusBadRequest},
		{"no user", "t1", "", `{"name":"x"}`, http.StatusBadRequest},
		{"empty name", "t1", "u1", `{"name":""}`, http.StatusBadRequest},
		{"missing name field", "t1", "u1", `{}`, http.StatusBadRequest},
		{"malformed json", "t1", "u1", `{{{`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := newMux(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/definitions", bytes.NewBufferString(tc.body))
			if tc.tenantID != "" {
				req.Header.Set("X-Tenant-ID", tc.tenantID)
			}
			if tc.userID != "" {
				req.Header.Set("X-User-ID", tc.userID)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("want %d, got %d (body: %s)", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

func TestStartInstance_RejectsInvalidJSON(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/instances", bytes.NewBufferString("not json"))
	req.Header.Set("X-Tenant-ID", "t1")
	req.Header.Set("X-User-ID", "u1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestSignalStep_RejectsInvalidJSON(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/instances/i1/signal", bytes.NewBufferString("{bad"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestWriteJSON_Shape(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Errorf("body: %v", body)
	}
}

func TestListTasks_RequiresHeaders(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/tasks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestListTasks_InvalidStatus(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/tasks?status=bogus", nil)
	req.Header.Set("X-Tenant-ID", "t1")
	req.Header.Set("X-User-ID", "u1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestCreateDefBody_Binding(t *testing.T) {
	raw := `{"name":"Invoice Approval","description":"2-step","steps":[{"name":"review","type":"approval","assignee_id":"u1"}]}`
	var body createDefBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Name != "Invoice Approval" {
		t.Errorf("name: %q", body.Name)
	}
	if len(body.Steps) != 1 {
		t.Fatalf("steps: %d", len(body.Steps))
	}
}

// H-10: the template write gates map to 404 (missing or unreadable private
// template — no existence leak) and 403 (visible but not manageable);
// anything else is a 500 that does not echo internals.
func TestDefinitionWriteErrorMapping(t *testing.T) {
	h := &Handler{log: zerolog.Nop()}
	cases := []struct {
		err  error
		want int
	}{
		{service.ErrNotFound, http.StatusNotFound},
		{fmt.Errorf("wrapped: %w", service.ErrForbidden), http.StatusForbidden},
		{errors.New("pq: connection refused at 10.0.0.5"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		h.writeDefinitionWriteError(w, tc.err, "update")
		if w.Code != tc.want {
			t.Errorf("%v: got %d, want %d", tc.err, w.Code, tc.want)
		}
		if strings.Contains(w.Body.String(), "10.0.0.5") {
			t.Errorf("%v: response leaks internals: %s", tc.err, w.Body.String())
		}
	}
}
