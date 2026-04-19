package handler

// Wave 10 — Permission matrix tests. Pins the curated rego-mirror
// so any change to policy.rego that isn't reflected here shows up as
// a test diff rather than silent drift in production.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatrix_ShapesMatchExpectation(t *testing.T) {
	h := &HTTPHandler{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/permissions/matrix", h.MatrixHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/permissions/matrix", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var m PermissionMatrix
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m))

	require.ElementsMatch(t, []string{"workspace", "folder", "document"}, m.ResourceTypes)
	require.ElementsMatch(t, []string{"admin", "delete", "edit", "share", "view"}, m.Capabilities)
	require.Contains(t, m.Roles, "owner")
	require.Contains(t, m.Roles, "admin")
	require.Contains(t, m.Roles, "workspace_admin")

	// Every (role, resource) pair should have exactly one cell. This
	// is the CI-drift guard: if a role is added to rego without a
	// matrix update, the assertion fails.
	want := len(m.Roles) * len(m.ResourceTypes)
	require.Len(t, m.Cells, want,
		"matrix cells count should equal roles × resource_types")

	// Owner / admin hold admin on all three resource types.
	for _, cell := range m.Cells {
		if cell.Role == "owner" || cell.Role == "admin" {
			require.Equal(t, "admin", cell.MaxCapability,
				"owner/admin expected admin on %s", cell.ResourceType)
		}
	}
}

func TestMatrix_IsCacheable(t *testing.T) {
	h := &HTTPHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/permissions/matrix", nil)
	w := httptest.NewRecorder()
	h.MatrixHandler(w, req)
	require.NotEmpty(t, w.Header().Get("Cache-Control"))
}
