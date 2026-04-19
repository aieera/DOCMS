# Remediation 11d — Wave 3: workspace CRUD + folder/document ops wiring

**Date:** 2026-04-17
**Source finding:** [11-coverage-matrix.md](../11-coverage-matrix.md)
rows C-7, C-8, B2-08, B2-09, B2-10, plus the implicit Workspace
`GetWorkspace`/`UpdateWorkspace`/`DeleteWorkspace` gap surfaced while
building CRUD.

## What shipped

### Backend — Workspace CRUD (ground-up)

The `workspaces` table existed ([services/document/migrations/000001_initial_schema.up.sql:166-188](../../../services/document/migrations/000001_initial_schema.up.sql#L166))
but had no proto, no repo, no service methods, no handlers. All five
layers added.

**Proto** — [proto/vaultdms/v1/document.proto](../../../proto/vaultdms/v1/document.proto)

- `message Workspace` with `id, tenant_id, name, description,
  region_pin, settings (Struct), created_by, created_at, updated_at,
  document_count, folder_count`.
- `message CreateWorkspaceRequest / GetWorkspaceRequest /
  ListWorkspacesRequest / ListWorkspacesResponse /
  UpdateWorkspaceRequest / DeleteWorkspaceRequest`.
- Five RPCs on `DocumentService` with grpc-gateway annotations:
  - `POST /api/v1/workspaces`
  - `GET /api/v1/workspaces/{workspace_id}`
  - `GET /api/v1/workspaces`
  - `PATCH /api/v1/workspaces/{workspace_id}`
  - `DELETE /api/v1/workspaces/{workspace_id}`

**Model** — [services/document/internal/model/document.go](../../../services/document/internal/model/document.go)

- New `Workspace` struct with counts populated inline by the repo.

**Repository** — [services/document/internal/repository/workspace_repo.go](../../../services/document/internal/repository/workspace_repo.go)
(new file)

- `Create`, `GetByID`, `List`, `Update`, `SoftDelete` methods. Counts
  computed via scalar subqueries on documents + folders tables (same
  pattern as `folder_repo.go`).
- Registered on the `Repositories` struct and `New()` wiring in
  [services/document/internal/repository/repository.go](../../../services/document/internal/repository/repository.go).

**Service** — [services/document/internal/service/workspaces.go](../../../services/document/internal/service/workspaces.go)
(new file)

- `CreateWorkspace`: validates name/description length, generates UUIDv7
  id, writes via `WithTenantTx`, emits `dms.workspace.created.v1` to
  outbox.
- `GetWorkspace`, `ListWorkspaces` — tenant-scoped reads.
- `UpdateWorkspace`: requires `admin` permission on `workspace`
  resource; empty name is no-change; emits `dms.workspace.updated.v1`.
- `DeleteWorkspace`: requires `admin`; refuses when document_count or
  folder_count > 0 (returns `Conflict`); soft-delete + emit
  `dms.workspace.deleted.v1`.

**Handler** — [services/document/internal/handler/workspaces.go](../../../services/document/internal/handler/workspaces.go)
(new file)

- Five thin wrappers delegating to the service; `workspaceToProto`
  mapper with `Settings` JSON → `google.protobuf.Struct` fallthrough.

### Frontend — folder + document ops (B2-08 / B2-09 / B2-10)

The backend already exposed `UpdateFolder`, `DeleteFolder`, and
`MoveDocument` RPCs — Wave 3's job was pure client wiring.

- [web/src/api/workspaces.ts](../../../web/src/api/workspaces.ts) —
  added `updateFolder`, `deleteFolder`, `updateWorkspace`,
  `deleteWorkspace`, `getWorkspace`. Also: `getWorkspaces` /
  `getFolders` now tolerate both the proto-shaped `{workspaces:…}` /
  `{folders:…}` wrappers and the legacy raw-array response, so rollout
  is safe during a phased deploy. Fixed a stale parameter name:
  `parent_id` → `parent_folder_id` to match the backend.
- [web/src/api/documents.ts](../../../web/src/api/documents.ts) — added
  `moveDocument(id, folderId)`.
- [web/src/hooks/useFolders.ts](../../../web/src/hooks/useFolders.ts)
  — new `useRenameFolder`, `useMoveFolder`, `useDeleteFolder` hooks,
  each invalidating the `['folders']` query on success.
- [web/src/hooks/useDocuments.ts](../../../web/src/hooks/useDocuments.ts)
  — new `useMoveDocument` hook.

## What's intentionally not in this PR

- **Folder/workspace context-menu UI** — the API + hook surface is
  present, but the actual "right-click a folder → Rename/Move/Delete"
  interaction + confirm-on-delete modals are design work I didn't want
  to ad-lib. The hooks can be consumed by components as you land the
  UX.
- **Workspace detail page** (`/workspaces/$workspaceId/settings` etc.)
  — `getWorkspace` / `updateWorkspace` / `deleteWorkspace` adapters
  exist; add routes when you have the settings page design.
- **Permission enforcement gap (pre-existing)** — `CreateWorkspace`
  does not yet require a specific tenant-wide role. Any authenticated
  caller in the tenant can create a workspace. Left that behaviour
  intact because tightening it is a policy decision (should it be
  admin-only? owner-only?). File a follow-up to decide.

## Required codegen step

Proto added new messages + RPCs. Before `go build ./services/document/...`
will succeed:

```bash
cd proto && buf generate
```

IDE diagnostics until then will include `undefined: vaultdmsv1.Workspace`,
`CreateWorkspaceRequest`, `ListWorkspacesResponse`, and the
gRPC-server method signatures the `UnimplementedDocumentServiceServer`
expects.

## Wave 3 scorecard

- C-7 `GET /workspaces` ✅
- C-8 `POST /workspaces` ✅
- B2-08 folder rename ✅ (API + hook; UI pending)
- B2-09 folder delete ✅ (API + hook; UI pending)
- B2-10 document move ✅ (API + hook; UI pending)

Backend coverage from the original audit (28% of user-facing endpoints
actively called) moves meaningfully forward: 5 new B2 endpoints wired
and 4 C items cleared across the full three-wave pass.
