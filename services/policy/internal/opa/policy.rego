package vaultdms.authz

import future.keywords.in
import future.keywords.if

# Default: deny everything.
default allow := false
default deny  := false

# ─── ALLOW RULES ───────────────────────────────────────────────────────────

# Rule 1: Direct permission grant (subject matches principal exactly).
allow if {
    some p in data.permissions
    p.principal_type == input.subject_type
    p.principal_id == input.subject_id
    p.resource_type == input.resource_type
    p.resource_id == input.resource_id
    capability_includes(p.capability, input.action)
    not permission_expired(p)
}

# Rule 2: Permission via a group the user is a member of.
allow if {
    input.subject_type == "user"
    some group_id in data.user_groups
    some p in data.permissions
    p.principal_type == "group"
    p.principal_id == group_id
    p.resource_type == input.resource_type
    p.resource_id == input.resource_id
    capability_includes(p.capability, input.action)
    not permission_expired(p)
}

# Rule 3: Folder permission cascades to documents in that folder.
allow if {
    input.resource_type == "document"
    input.context.folder_id != ""
    some p in data.permissions
    matches_principal(p, input)
    p.resource_type == "folder"
    p.resource_id == input.context.folder_id
    capability_includes(p.capability, input.action)
    not permission_expired(p)
}

# Rule 4: Workspace permission cascades to all content in that workspace.
allow if {
    input.resource_type in {"document", "folder"}
    input.context.workspace_id != ""
    some p in data.permissions
    matches_principal(p, input)
    p.resource_type == "workspace"
    p.resource_id == input.context.workspace_id
    capability_includes(p.capability, input.action)
    not permission_expired(p)
}

# Rule 5: Workspace admin has every capability on the workspace + contents.
# Accepts the workspace id from either input.context.workspace_id (set
# when evaluating document/folder resources via the cascade rules) or
# from input.resource_id directly when the resource itself is a
# workspace. Without that second path, asking "can user X view
# workspace Y" never fired Rule 5 even when X is a wm.role=admin row
# for Y — the caller in services/document/internal/service/
# documents.go:ListDocuments passes extra=nil so context.workspace_id
# is empty.
allow if {
    input.subject_type == "user"
    some wm in data.workspace_members
    wm.user_id == input.subject_id
    wm.role == "admin"
    workspace_id_of_input(input) == wm.workspace_id
}

# Rule 5a: Any workspace member (admin / member / viewer) has at least
# `view` on the workspace itself + its content cascade. Without this, a
# user added as wm.role=member could see the workspace card in the list
# (the document service's workspace_repo.List grants visibility on
# membership) but the inner /workspaces/{id}/documents call 403'd
# because no OPA rule recognised non-admin membership as a view grant.
# Higher capabilities (edit/share/delete/admin) still require Rule 1/2
# explicit grants or Rule 5 workspace-admin membership.
allow if {
    input.subject_type == "user"
    input.action == "view"
    some wm in data.workspace_members
    wm.user_id == input.subject_id
    wm_ws_in_scope(wm, input)
}

# Rule 6: Organization owner/admin has every capability.
allow if {
    input.subject_type == "user"
    input.context.user_role == "owner"
}
allow if {
    input.subject_type == "user"
    input.context.user_role == "admin"
}

# ─── DENY RULES (override allow) ───────────────────────────────────────────

# Disposed documents are not accessible to non-owner/non-admin users.
deny if {
    input.resource_type == "document"
    input.context.lifecycle_state == "disposed"
    not input.context.user_role == "owner"
    not input.context.user_role == "admin"
}

# Deactivated users cannot access anything.
deny if {
    input.context.user_status == "deactivated"
}

# ─── HELPERS ───────────────────────────────────────────────────────────────

matches_principal(p, inp) if {
    p.principal_type == inp.subject_type
    p.principal_id == inp.subject_id
}

matches_principal(p, inp) if {
    inp.subject_type == "user"
    p.principal_type == "group"
    some gid in data.user_groups
    p.principal_id == gid
}

# workspace_id_of_input resolves the workspace ID the rule should check
# against, regardless of whether the resource itself is a workspace
# (resource_id IS the workspace id) or a document/folder under a
# workspace (context.workspace_id was set by the caller).
workspace_id_of_input(inp) := id if {
    inp.resource_type == "workspace"
    id := inp.resource_id
}
workspace_id_of_input(inp) := id if {
    inp.resource_type != "workspace"
    inp.context.workspace_id != ""
    id := inp.context.workspace_id
}

# wm_ws_in_scope reports whether a workspace_members row's workspace
# matches the request — either the resource itself or via context.
wm_ws_in_scope(wm, inp) if {
    wm.workspace_id == workspace_id_of_input(inp)
}

# Capability hierarchy: admin > delete > edit > share > view_unredacted > view.
# `view_unredacted` (ADR 0079) sits intentionally between view and share.
# Granting `view` does NOT cascade to seeing the source of a redacted
# document — that's the whole point. Granting any of {share, edit,
# delete, admin} does cascade (your administrators see everything).
capability_includes(granted, requested) if {
    hierarchy := {"admin": 50, "delete": 40, "edit": 30, "share": 20, "view_unredacted": 15, "view": 10}
    hierarchy[granted] >= hierarchy[requested]
}

permission_expired(p) if {
    p.expires_at != ""
    time.now_ns() > time.parse_rfc3339_ns(p.expires_at)
}
permission_expired(p) if {
    p.valid_to != ""
    time.now_ns() > time.parse_rfc3339_ns(p.valid_to)
}

# ─── FINAL DECISION ────────────────────────────────────────────────────────
final_decision := "allow" if {
    allow
    not deny
}
final_decision := "deny" if { not allow }
final_decision := "deny" if { deny }
