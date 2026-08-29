package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestERPContractRoutesAcceptAPIKey pins the ERP integration contract
// (INTEGRATION.md §1, docs/integrations/erp-integration.md §3.1): every
// document-service route the ERP calls with a Bearer vdms_ key must be
// registered through a SessionOrAPIKey chain with the documented scope.
//
// Why this exists: the document service's catch-all route is cookie-only
// (SessionAuthOptional → TenantHTTP). A route that is merely *forgotten*
// from the API-key allow-list doesn't 403 with a helpful "missing scope" —
// it falls through to the catch-all and 401s with "missing or invalid
// tenant", which reads like a broken key. That is exactly how
// DELETE /api/v1/documents/{id} shipped unreachable for the ERP.
func TestERPContractRoutesAcceptAPIKey(t *testing.T) {
	mainGo := filepath.Join(findRepoRoot(t), "services", "document", "cmd", "server", "main.go")
	b, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatalf("read %s: %v", mainGo, err)
	}
	src := string(b)

	// method+pattern → required API-key scope. Only routes whose scope is
	// named at the registration site are listed; the /ingest and
	// /review-queue routes go through named chain closures and are
	// covered by their own handler docs.
	contract := map[string]string{
		"POST /api/v1/documents":                                  "documents:write",
		"GET /api/v1/documents/{document_id}":                     "documents:read",
		"DELETE /api/v1/documents/{document_id}":                  "documents:delete",
		"POST /api/v1/documents/{document_id}/versions":           "documents:write",
		"POST /api/v1/documents/{document_id}/move":               "documents:write",
		"DELETE /api/v1/folders/{folder_id}":                      "documents:delete",
		"GET /api/v1/workspaces/{workspace_id}/folders":           "documents:read",
		"POST /api/v1/workspaces/{workspace_id}/folders":          "documents:write",
		"GET /api/v1/workspaces/{workspace_id}/documents":         "documents:read",
		"POST /api/v1/workspaces/{workspace_id}/documents:upsert": "documents:write",
		"GET /api/v1/documents:byExternalKey":                     "documents:read",
	}

	var problems []string
	for pattern, scope := range contract {
		stmt, ok := handleStatement(src, pattern)
		if !ok {
			problems = append(problems, pattern+": not registered on rootMux (falls through to the cookie-only catch-all)")
			continue
		}
		if !strings.Contains(stmt, "apiKeyGateway") && !strings.Contains(stmt, "SessionOrAPIKey") {
			problems = append(problems, pattern+": registered without an API-key chain")
			continue
		}
		if !strings.Contains(stmt, `"`+scope+`"`) {
			problems = append(problems, pattern+": expected scope "+scope+" at the registration site")
		}
	}
	if len(problems) > 0 {
		t.Fatalf("ERP contract route(s) not API-key reachable in services/document/cmd/server/main.go:\n  %s",
			strings.Join(problems, "\n  "))
	}
}

// handleStatement returns the source text of the rootMux.Handle statement
// registering pattern — from the Handle call up to the next Handle call
// (registrations may span several lines).
func handleStatement(src, pattern string) (string, bool) {
	marker := `rootMux.Handle("` + pattern + `",`
	i := strings.Index(src, marker)
	if i < 0 {
		return "", false
	}
	rest := src[i+len(marker):]
	if j := strings.Index(rest, "rootMux.Handle("); j >= 0 {
		rest = rest[:j]
	}
	return rest, true
}
