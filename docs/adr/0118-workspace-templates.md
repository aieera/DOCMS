# ADR 0118 — Workspace Templates (folder-tree provisioning)

- **Status**: Accepted
- **Date**: 2026-07-03
- **Relates to**: ADR 0021 (event emission), §4.7/C5 (outbox-only), the
  folder model (ltree paths, visibility + folder_grants)

## Context

Teams recreate the same project folder structure (with placeholder
documents, metadata defaults, and access grants) by hand for every new
project/customer/matter. The ERP integration already scaffolds
per-customer trees programmatically; this generalizes the capability
into a tenant-level, user-authorable feature.

## Decision

### 1. Model: a JSONB definition validated in the service layer

`workspace_templates` (document migration 000092, RLS-forced) stores a
recursive `definition`:

```json
{ "nodes": [ { "name": "Project {{project_name}}",
               "visibility": "shared" | "private",
               "metadata": {"key": "value or {{var}}"},
               "grants": [{"grantee_type": "user|group", "grantee_id": "<uuid>"}],
               "docs": [{"title": "…", "metadata": {}, "tags": []}],
               "children": [ … ] } ] }
```

Strings may reference `{{variables}}` (folder names, doc titles,
metadata values, tags), prompted for at provision time. Folder-node
`metadata` is INHERITED: folders themselves carry no metadata column,
so a node's metadata becomes the default set for every placeholder
document at that node and below, overridden key-by-key by deeper nodes
and by per-doc metadata. Validation (`model.ParseTemplateDefinition`)
enforces ≤200 folders, ≤200 docs, ≤10 template depth, ≤256 KiB raw
definition (also enforced with a request-body LimitReader), known
fields only, valid grantee UUIDs. Authoring is admin/owner-gated;
reading and provisioning are member-level.

### 2. Provision = one tenant transaction, subtree in an existing workspace

`POST /api/v1/templates/{id}/provision {workspace_id, parent_folder_id?,
variables}` requires **edit on the target workspace** (the CreateFolder
gate) and walks the tree inside ONE `WithTenantTx`: folders (ltree
path/depth computed exactly like `CreateFolder`, honoring
`maxFolderDepth` from the anchor), `folder_grants` rows, and placeholder
documents (draft, no version — bytes arrive later through the normal
upload path; metadata validated against the tenant schema). Either the
whole structure exists afterwards or none of it does. All referenced
variables must be supplied — a half-substituted `Project {{name}}` is
rejected, not created.

Provisioning a NEW workspace per project was considered and deferred:
workspace creation is admin-gated and membership bootstrapping is its
own problem; a template provisioned at workspace root covers the
whole-workspace-layout case.

### 3. Events: per-entity events + a summary, all via the outbox

Each provisioned folder/document emits the same `dms.folder.created.v1`
/ `dms.document.created.v1` (with materialized `readable_by`) the
one-at-a-time paths emit — the search indexer and sync delta treat
provisioned entities identically to hand-created ones.
`dms.template.provisioned.v1` (subject bound via the new
`dms.template.>` binding on the DOC_EVENTS stream) summarizes the run
for audit/automation. Everything is outbox-inserted in the provision tx
(C5).

### 4. Surface: hand-written REST + a gallery/editor/provision UI

REST on the document service mux (`/api/v1/templates…`, same idiom as
tasks/annotations/sync — not grpc-gateway; templates are a leaf feature
that doesn't need cross-service proto contracts). Web: `/templates`
gallery (cards with folder/doc/variable badges), a structured tree
editor (add/nest/remove folders; per-node visibility, metadata,
placeholder docs, grants), and a provision dialog that prompts for the
workspace, optional parent folder, and every `{{variable}}` the
definition references. `GET /templates/{id}/variables` exposes the
prompt list to API clients.

## Consequences

- Grants reference user/group UUIDs directly; a people-picker in the
  editor is a follow-up (UUID input v1). A grant referencing a deleted
  principal simply never matches — same semantics as stale
  folder_grants rows anywhere else.
- Placeholder docs are draft documents without versions; OCR/search
  index them on their first uploaded version like any other document.
- Provision is not idempotent by key — re-provisioning creates sibling
  folders with the same names (folders are not name-unique). An
  Idempotency-Key gate on the provision route is a cheap follow-up if
  double-click dupes show up in practice.
- No runtime E2E here: verified by unit tests (definition validation,
  variable extraction/substitution) + build/archtest; the provision
  walk needs the integration suite (testcontainers) for DB-backed
  coverage.
