// Wave 10 — Permission Matrix endpoint.
//
// GET /api/v1/permissions/matrix returns a read-only description of
// which role grants which capability on which resource type. The
// matrix is derived from policy.rego — the rules hand-coded here
// mirror the rego exactly.
//
// This is NOT live rego introspection (OPA doesn't expose a stable
// Go API for rule enumeration without a re-parser pass). Keeping
// the matrix as a curated constant means we update it in the same
// commit when policy.rego changes — and a CI guard catches drift
// (scripts/check-policy-matrix-sync.sh, future Wave 11 work).
package handler

import (
	"encoding/json"
	"net/http"
)

// PermissionMatrix is the wire shape. Roles are the outer axis,
// resource_types the inner axis, cells are the max capability the
// role holds on that resource.
type PermissionMatrix struct {
	Roles         []string            `json:"roles"`
	ResourceTypes []string            `json:"resource_types"`
	Capabilities  []string            `json:"capabilities"`
	Cells         []PermissionCell    `json:"cells"`
	Notes         []string            `json:"notes"`
}

// PermissionCell is one (role, resource_type) pairing.
type PermissionCell struct {
	Role         string `json:"role"`
	ResourceType string `json:"resource_type"`
	MaxCapability string `json:"max_capability"` // one of Capabilities, or "" for none
	Source        string `json:"source"`         // rego rule label
}

// buildMatrix mirrors services/policy/internal/opa/policy.rego. The
// rules used:
//
//   Rule 5: workspace_members.role == admin → admin on workspace + contents
//   Rule 6: user_role ∈ {owner, admin} → admin on everything
//   Otherwise: member / viewer / external get only explicit grants.
//
// "member" / "viewer" / "external" cells are intentionally "" (no
// baseline) — those roles rely on explicit permission rows, which
// operators see per-resource on the document/folder/workspace share
// dialogs, not in this global matrix.
func buildMatrix() PermissionMatrix {
	const (
		admin  = "admin"
		del    = "delete"
		edit   = "edit"
		share  = "share"
		view   = "view"
	)
	roles := []string{"owner", "admin", "workspace_admin", "member", "viewer", "external"}
	resources := []string{"workspace", "folder", "document"}
	caps := []string{admin, del, edit, share, view}

	// Curated cells — mirrors rego.
	cells := []PermissionCell{
		// Rule 6: org owner / org admin — full admin on everything.
		{"owner", "workspace", admin, "rego rule 6 (user_role=owner)"},
		{"owner", "folder", admin, "rego rule 6 (user_role=owner)"},
		{"owner", "document", admin, "rego rule 6 (user_role=owner)"},
		{"admin", "workspace", admin, "rego rule 6 (user_role=admin)"},
		{"admin", "folder", admin, "rego rule 6 (user_role=admin)"},
		{"admin", "document", admin, "rego rule 6 (user_role=admin)"},
		// Rule 5: workspace admin (from workspace_members.role) — admin on
		// the workspace + any folder/document inside (via rules 3 + 4).
		{"workspace_admin", "workspace", admin, "rego rule 5"},
		{"workspace_admin", "folder", admin, "rego rules 3 + 5 (cascade)"},
		{"workspace_admin", "document", admin, "rego rules 3 + 4 + 5 (cascade)"},
		// Plain members / viewers / external — no baseline, only explicit
		// grants. Exposed as "" so the UI renders a dash.
		{"member", "workspace", "", "explicit grant required"},
		{"member", "folder", "", "explicit grant required"},
		{"member", "document", "", "explicit grant required"},
		{"viewer", "workspace", "", "explicit grant required"},
		{"viewer", "folder", "", "explicit grant required"},
		{"viewer", "document", "", "explicit grant required"},
		{"external", "workspace", "", "share-link only"},
		{"external", "folder", "", "share-link only"},
		{"external", "document", "", "share-link only"},
	}

	notes := []string{
		"The capability hierarchy is admin > delete > edit > share > view — a role holding `edit` also holds `share` and `view`.",
		"Workspace-level admin cascades down to folders and documents inside that workspace (policy.rego rules 3 + 4).",
		"Members, viewers, and external users see only the resources for which an admin has explicitly granted them a capability (or a share link, for external).",
		"Disposed documents are denied for all non-owner / non-admin roles (rego deny-rule #1).",
		"Deactivated users are denied on every resource regardless of grants (rego deny-rule #2).",
	}

	return PermissionMatrix{
		Roles:         roles,
		ResourceTypes: resources,
		Capabilities:  caps,
		Cells:         cells,
		Notes:         notes,
	}
}

// MatrixHandler writes the permission matrix as JSON.
func (h *HTTPHandler) MatrixHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300") // 5 min — matrix is nearly static
	_ = json.NewEncoder(w).Encode(buildMatrix())
}
