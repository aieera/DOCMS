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
allow if {
    input.subject_type == "user"
    input.context.workspace_id != ""
    some wm in data.workspace_members
    wm.user_id == input.subject_id
    wm.workspace_id == input.context.workspace_id
    wm.role == "admin"
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

# Rule 7: compliance_officer can export evidence (ediscovery.export action).
# ADR 0038 separation-of-duties: admin authors retention policies and
# legal-holds; compliance_officer approves the destruction / export of
# evidence under those policies. Owner can do both as a break-glass.
# Rule 6 grants admin every capability — Rule-7-deny narrows that for
# this specific action.
allow if {
    input.action == "ediscovery.export"
    input.context.user_role == "compliance_officer"
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

# ADR 0038 separation-of-duties on ediscovery.export. Rule 6 grants
# admin universal capability; this deny narrows that one action so
# admins must escalate to compliance_officer (or break-glass via
# owner) to ship evidence outside the system. Without this, an
# admin who'd configured a too-broad retention policy could also
# export the evidence proving that — exactly what SoD prevents.
deny if {
    input.action == "ediscovery.export"
    input.context.user_role == "admin"
    not input.context.user_role == "owner"
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

# Capability hierarchy: admin > delete > edit > share > view.
capability_includes(granted, requested) if {
    hierarchy := {"admin": 50, "delete": 40, "edit": 30, "share": 20, "view": 10}
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
