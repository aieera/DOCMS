# SeDoc Audit — Phase A: Missing Items

**Date:** 2026-04-16

Items specified in the architecture playbook that are NOT present in the repo.

---

## Missing Services

| Expected Service | Status | Notes |
|-----------------|--------|-------|
| **gateway** | **MISSING** | API gateway service (rate limit, WAF, routing). Logic exists in `pkg/gateway/` but no standalone service with `cmd/server/main.go` |
| **controlplane** | **MISSING** | Tenant provisioning control plane. Logic partially exists in `services/billing/internal/provisioner/` but no separate service |

## Missing Proto Generated Code

| Item | Status |
|------|--------|
| proto/gen/go/vaultdms/v1/*.pb.go | **MISSING** — zero generated files. `buf generate` never run. **This blocks compilation of document, policy, storage, and auth services.** |

## Missing Migrations

| Service | Expected | Status |
|---------|----------|--------|
| auth | users, sessions, mfa_secrets, api_keys, sso_configs | **MISSING** — 0 migration files. Tables defined in document/000001 but auth service has no own migrations |
| policy | permissions, permission_grants | **MISSING** |
| storage | upload_sessions, content_blobs, scan_results | **MISSING** — tables in document/000001 |
| audit | audit_events | **MISSING** |
| workflow | workflow_definitions, workflow_instances, workflow_tasks | **MISSING** |
| notification | notifications, notification_preferences | **MISSING** |
| signature | signature_requests | **MISSING** |
| billing | subscriptions, usage_records (+ organizations.settings) | **MISSING** |
| connector | webhook_subscriptions, webhook_deliveries, connector_configs | **MISSING** |

**Note:** Many tables ARE defined in `services/document/migrations/000001_initial_schema.up.sql` (the 45-table mega-migration), but the individual services don't own their own migrations. This is a single-DB-shared-schema design choice, not necessarily a defect.

## Missing Deploy

| Expected | Status |
|----------|--------|
| deploy/terraform/ | **MISSING** — no Terraform modules for AWS/GCP/Azure |
| deploy/ansible/ | **MISSING** — no Ansible playbooks for on-prem |

## Missing Desktop App

| Expected | Status |
|----------|--------|
| desktop/ | **MISSING** — no Electron/Tauri desktop application |

## Missing README Files

**Zero service-level README.md files exist.** All 14 services lack documentation.

## Missing pkg/ Modules

All expected pkg modules are present. No gaps.

## Missing Docker Compose Services

| Expected | docker-compose.yml | Notes |
|----------|-------------------|-------|
| postgres | ✓ | |
| redis | ✓ | |
| opensearch | ✓ | |
| minio | ✓ | User uses AWS S3 instead |
| nats | ✓ | |
| qdrant | ✓ | |
| temporal | ✓ | |
| temporal-ui | ✓ | |
| clamav | ✓ | |
| **collaboration (Node WS)** | **MISSING** | Not in compose file |
| **Celery workers** | **MISSING** | intelligence + preview workers not in compose |

## Missing Helm Templates

| Service | Full template set (deploy+svc+hpa+pdb+netpol+monitor) | Actual |
|---------|------------------------------------------------------|--------|
| audit | 6 expected | 6 ✓ |
| auth | 6 expected | 6 ✓ |
| billing | 6 expected | **1 only** (deployment.yaml) — missing hpa, pdb, netpol, servicemonitor |
| collaboration | 6 expected | **1 only** (deployment.yaml) |
| connector | 6 expected | **0** — no Helm templates at all |
| intelligence | 6 expected | **2 only** (deployment + service) |
| preview | 6 expected | **0** — no Helm templates at all |
| web | 6 expected | **1 only** (deployment.yaml) |

## Missing Frontend Routes/Components

| Playbook item | Status |
|--------------|--------|
| workflow/WorkflowDesigner.tsx (ReactFlow canvas) | **MISSING** |
| workflow/WorkflowNodePalette.tsx | **MISSING** |
| workflow/WorkflowNode.tsx | **MISSING** |
| workflow/WorkflowPropertiesPanel.tsx | **MISSING** |
| workflow/ApprovalCard.tsx | **MISSING** |
| search/SearchPage.tsx (standalone component) | Route exists, component inlined |
| search/SearchBar.tsx | **MISSING** (logic inlined in route) |
| search/FacetPanel.tsx | **MISSING** |
| search/SavedSearches.tsx | **MISSING** |
| search/SearchSuggestions.tsx | **MISSING** |
| documents/DocumentGrid.tsx | **MISSING** |
| documents/DocumentTable.tsx | **MISSING** |
| documents/MoveDialog.tsx | **MISSING** |
| viewer/AnnotationLayer.tsx | **MISSING** |
| viewer/CompareView.tsx | **MISSING** |
| admin/InviteUserDialog.tsx | **MISSING** |
| admin/GroupEditor.tsx | **MISSING** |
| admin/PermissionEditor.tsx | **MISSING** |
| admin/RetentionPolicyForm.tsx | **MISSING** |
| admin/LegalHoldManager.tsx | **MISSING** |
| admin/MetadataSchemaEditor.tsx | **MISSING** |
| admin/SSOConfigWizard.tsx | **MISSING** |
| admin/WebhookManager.tsx | **MISSING** |
| shared/ProtectedRoute.tsx | **MISSING** |
| ui/ContextMenu.tsx | **MISSING** |
| ui/Table.tsx (raw wrapper) | **MISSING** (DataTable exists) |
| ui/Toast.tsx (styled) | **MISSING** (using react-hot-toast default) |

**Frontend test files: 0** (zero test coverage for 107 TS/TSX files)

## Missing CI/CD

| Item | Status |
|------|--------|
| `buf generate` step in CI | **MISSING** — proto gen not in CI pipeline |
| Frontend test step in CI | **MISSING** — web tests not run (none exist) |
| Integration test step | **MISSING** — no docker-compose-based integration tests in CI |

## Summary Counts

| Category | Expected | Present | Missing |
|----------|----------|---------|---------|
| Services | 16 | 14 | 2 (gateway, controlplane) |
| Proto generated code | 13 packages | 0 | **13** |
| Service migrations | 11 services | 2 | 9 |
| Service READMEs | 14 | 0 | 14 |
| Helm full template sets | 14 | 9 | 5 (partial/missing) |
| Deploy tools | 3 (helm+terraform+ansible) | 1 | 2 |
| Frontend components | ~80 specified | ~54 built | ~26 |
| Frontend tests | any | 0 | all |
| Desktop app | 1 | 0 | 1 |
