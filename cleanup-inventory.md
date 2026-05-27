# VaultDMS Cleanup Inventory — Phase 1

**Branch:** `chore/eslint-flat-config`
**Date:** 2026-05-27
**Status:** Phase 1 (inventory) complete. **No code modified.** Awaiting approval before any removal.

---

## Methodology

### Tools run

| Tool | Version | Scope | Command form |
|---|---|---|---|
| `go build ./...` | go1.26.2 | per module (17 modules in `go.work`) | baseline gate |
| `go vet ./...` | go1.26.2 | per module | baseline gate |
| `staticcheck` | 2026.1 (v0.7.0) | per module, `-checks=U1000,U1001`, both with and without `-tags integration` | unused unexported symbols |
| `deadcode` | golang.org/x/tools/cmd/deadcode | per `main` package (`cmd/server`, `cmd/worker`, etc.) | reachability-based |
| `go mod tidy` | go1.26.2 | per module — backed up `go.mod`/`go.sum`, diffed, restored. **All originals restored; no .bak files left behind.** | unused deps |
| `ruff check --select F401,F841,F811` | 0.15.14 | `services/intelligence`, `services/preview` | unused imports / locals / redefinitions |
| `vulture --min-confidence 80` | 2.16 | same | reachability-based unused |
| `deptry` | 0.25.1 | same | dep declared/used mismatch |
| `knip --no-progress --reporter compact` | npx-latest | `web/` | unused files / exports / deps |
| `ts-prune` | npx-latest | `web/` | unused TS exports |
| `depcheck --json` | npx-latest | `web/` | unused npm deps |
| Grep + Glob | built-in | repo-wide | orphan scripts / dead env vars / asset references |

### Baseline state

- Go: `go build ./...` is **green** in every workspace module.
- `go vet ./...` is **clean** in every workspace module.
- One module (`services/mcp-server`) had no `go.sum` file (and is excluded from the `go mod tidy` diff for that reason).

### Build-tag handling

Re-ran `staticcheck` with `-tags integration` across every module. **Result: identical to the no-tag run** — no integration-tagged code uses any of the U1000-flagged symbols, but also no integration code introduces new U1000s. The 9 `//go:build integration` files all hold tests, not production code.

### Reachability-rule application

Every static finding was cross-checked against the §4 dynamic-reachability guards:

- gRPC `Register*Server` registration — symbols registered into gRPC servers were retained.
- NATS subject dispatch — confirmed against handler-table entries.
- Temporal workflow/activity registration — every workflow function flagged by deadcode in `services/workflow/cmd/server` was cross-checked against `services/workflow/cmd/worker/main.go`. Multiple findings moved from candidate to KEEP on that basis.
- grpc-gateway annotations + WOPI — REST handlers generated from `google.api.http` are kept.
- TanStack Router file-based routing — 82 route files in `web/src/routes/**` flagged by knip as orphan are KEPT.
- i18n namespace JSONs, MSW handlers, lazy imports — same treatment.
- §5 WIP list — license enforcement scaffolding, native-connector M365 stubs, iPaaS, ESIGN mock, mobile/addins/extension, M365 (tid,oid) refactor, tenant-id-from-context sweep, OCR worker edge cases.

### Exclusions (NOT analyzed)

- `proto/gen/go/`, `proto/gen/openapi/` — generated.
- Every `migrations/` directory (per-service, ~10 dirs) — append-only history.
- `web/src/routeTree.gen.ts`, `web/src/generated/**` — generated.
- `web/public/locales/{en,ar}/**` — i18n bundles loaded dynamically.
- `node_modules/`, `__pycache__/`, `coverage/`, `bin/`, `dist/`.
- `pkg/archtest/` — pins invariants; tests are NOT dead.
- `*_test.go`, `*.test.{ts,tsx}`, `e2e/*.spec.ts`, `pkg/testharness/`, `pkg/testutil/` — pin behavior.
- `docs/security/`, `docs/adr/`, `.github/CODEOWNERS`, `.github/workflows/` security gates, `release.yml` cosign config — flag only, never remove.
- `deploy/license/*.pem` — KEYs used by `pkg/license`.
- `signer.proto`-generated Java stubs under `services/signature-signer/build/generated/`.

### Tool gaps / unverifiable

- `services/signature-signer` Java compiler not invoked (`mvn`/`gradle` not on PATH); manual static read on 2 hand-written `.java` files.
- `services/collaboration` knip not run (no per-service node_modules); manual reference-count for 7 JS files.

---

## Counts per bucket

| Language / area | Bucket A (safe) | Bucket B (review) | Bucket C (WIP / keep) |
|---|---|---|---|
| Go (`pkg/` + 17 modules) | **1** | 22 | 22 |
| Python (intelligence + preview) | **29** (28 unused imports, 1 in preview) | 12 (deptry DEP002 + vulture locals) | — |
| Node (`services/collaboration`) | **1** (`uuid` dep) | 3 | 4 |
| Java (`services/signature-signer`) | 0 | 2 | 1 (whole `SignerService.java` body) |
| Web (`web/`) | **6** files + **5** deps | ~70 (50 unused exports + ~20 chained shadcn primitives + deps) | scaffolding sections (whole-area) |
| Repo orphans (`scripts/`, `ops/`, env, root) | **3** (2 empty dirs + 1 typo env key) | 8 | all CI / helm / regional / monitoring |
| **TOTAL Bucket A** | **45** items | | |

---

## Bucket A — Safe to remove (high confidence)

> Criteria: unexported / private OR confirmed zero references repo-wide, NOT in a test file, NOT pinning an archtest invariant, NOT in any §5 WIP area, no §4 dynamic-reachability path remains plausible.

### A.1 — Go

| Path | Symbol | Lang | Category | Detection tool | Evidence | §4 check | WIP/ADR | Confidence | Action | Risk |
|---|---|---|---|---|---|---|---|---|---|---|
| `services/document/internal/handler/mappers.go:304` | `marshalMap` | Go | unused-private-func | deadcode | unreachable in `document/cmd/server` build graph; no NATS/gRPC/Temporal/OPA dispatch | passed | none | High | Remove | None — private helper, no callers |

(Just one Go Bucket A item. Most Go findings touch WIP areas — see Bucket B/C.)

### A.2 — Python (`services/intelligence` + `services/preview`)

| Path | Line | Symbol | Category | Tool | Confidence | Action |
|---|---|---|---|---|---|---|
| `services/intelligence/app/dedupe.py` | 14, 19 | `asyncio`, `typing.Optional` | unused-import (F401) | ruff | High | Remove |
| `services/intelligence/app/llm_gateway.py` | 114 | `AirGappedError, BreakerOpenError, BudgetExceededError` (3 imports) | unused-import (F401) | ruff | High | Remove |
| `services/intelligence/app/llm_routing.py` | 21 | `dataclasses.field` | unused-import | ruff | High | Remove |
| `services/intelligence/app/models/embedder.py` | 4 | `typing.Optional` | unused-import | ruff | High | Remove |
| `services/intelligence/app/nats_consumer.py` | 18, 35, 45 | `collections.defaultdict`, redefinition of `compliance_scan`, `extract_fields` | F401 / F811 | ruff | High | Remove (35 is a `compliance_scan` redef — review whether intended) |
| `services/intelligence/app/nats_consumer.py` | 281 | local `exc` | unused-local (F841) | ruff | High | Remove |
| `services/intelligence/app/qa_persist.py` | 11 | `typing.Any` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/anomaly_detect.py` | 320 | `qdrant_client.models.MatchAny` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/auto_tag.py` | 38, 40 | `typing.Any`, `publish_cloudevent` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/duplicate.py` | 9, 97 | `MinHashLSH`, `numpy` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/lang_detect.py` | 19 | `typing.Any` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/model_evaluate.py` | 23 | `typing.Any` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/model_retrain.py` | 28 | `typing.Any` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/ocr.py` | 14, 19 | `signal`, `contextlib.contextmanager` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/ocr_quality.py` | 30 | `typing.Any` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/rag.py` | 4, 15 | `json`, `celery_app` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/smart_route.py` | 32, 35 | `collections.Counter`, `typing.Any` | F401 | ruff | High | Remove |
| `services/intelligence/app/tasks/translate.py` | 26 | `typing.Any` | F401 | ruff | High | Remove |
| `services/intelligence/app/tenant_classifier.py` | 20 | `typing.Any` | F401 | ruff | High | Remove |
| `services/intelligence/app/tenant_llm_config_repo.py` | 17, 110, 116 | `time` + 2 unused locals | F401 / F841 | ruff | High | Remove |
| `services/preview/app/processors/image.py` | 5 | `typing.Optional` | F401 | ruff | High | Remove |

**Test-file F401s deliberately omitted from Bucket A** (the prompt's "tests pin behavior" rule). They are valid removals but lower-priority and live in `tests/conftest.py`, `tests/test_anomaly_detect.py`, `tests/test_compliance_scan.py`, `tests/test_llm_routing.py`, `tests/test_ocr_pipeline.py`, `tests/test_ocr_word_boxes.py`, `tests/test_rag_workspace_query.py`, `tests/test_redact_burn_roundtrip.py`, `tests/test_secrets.py`, `tests/test_translation.py` — 12 imports total. Move to Bucket B for separate review.

### A.3 — Node (`services/collaboration`)

| Path | Symbol | Category | Tool | Evidence | Confidence | Action |
|---|---|---|---|---|---|---|
| `services/collaboration/package.json` | dep `uuid@^9.0.0` | unused-dep | manual grep | 0 `import` references in `src/`; code uses `crypto.randomUUID()` at `index.js:1,22` | High | Remove from package.json |

### A.4 — Web (`web/`)

| Path | Symbol | Category | Tool | Evidence | Confidence | Action |
|---|---|---|---|---|---|---|
| `web/src/components/ui/CommandPalette.tsx` | file | orphan-file | knip + grep | 0 imports; superseded by `ui/shadcn/command.tsx` | High | Delete file |
| `web/src/components/ui/ErrorState.tsx` | file | orphan-file | knip + grep | 0 imports in `src/` | High | Delete file |
| `web/src/components/ui/SearchInput.tsx` | file | orphan-file | knip + grep | 0 imports (the `FederatedSearchInput` substring match is unrelated) | High | Delete file |
| `web/src/components/layout/Breadcrumb.tsx` | file | orphan-file (duplicate) | knip + grep | 0 imports; live one is plural `breadcrumbs.tsx` exporting `Breadcrumbs` (used by `app-topbar.tsx`) | High | Delete file |
| `web/src/components/layout/FolderTree.tsx` | file | orphan-file | knip + grep | 0 imports (Lucide `FolderTree` icon name is unrelated) | High | Delete file |
| `web/src/components/layout/WorkspaceSelector.tsx` | file | orphan-file | knip + grep | 0 imports (only a stale comment reference inside `breadcrumbs.tsx`) | High | Delete file |
| `web/package.json` | dep `react-intl` | unused-dep | knip + depcheck + grep | 0 references in `web/src` | High | Remove dep |
| `web/package.json` | dep `@dnd-kit/core` | unused-dep | knip + depcheck + grep | 0 references in `web/src` | High | Remove dep |
| `web/package.json` | dep `@dnd-kit/sortable` | unused-dep | knip + depcheck + grep | 0 references in `web/src` | High | Remove dep |
| `web/package.json` | dep `@hookform/resolvers` | unused-dep | knip + depcheck + grep | 0 references in `web/src` (only `react-hook-form` itself is referenced — once, in `ui/form.tsx`, which is itself Bucket B because unused) | High | Remove dep |
| `web/package.json` | dep `@radix-ui/react-switch` | unused-dep | knip + depcheck + grep | 0 references in `web/src` | High | Remove dep |
| `web/package.json` | dep `graphql` | unused-dep | knip + depcheck + grep | 0 references in `web/src` (urql brings it transitively if needed) | Medium-High | Remove dep — verify urql doesn't need it as peer first |

### A.5 — Repo

| Path | Type | Tool | Evidence | Confidence | Action |
|---|---|---|---|---|---|
| `ops/prometheus/prometheus.yml/` | empty directory (this is supposed to be a file, not a dir!) | `ls -la` | created 2026-05-15; 0 bytes inside; no files | High | Convert to file (or remove dir if config has migrated to `deploy/monitoring/`) |
| `ops/prometheus/rules/` | empty directory | `ls -la` | 0 files inside | High | Remove dir (alert rules now live under `deploy/monitoring/alerts/`) |
| `deploy/helm/vaultdms/values-onprem.yaml:47` | env key `VAULTDMS_SMTP_TLS` | dead-config | grep | code uses `VAULTDMS_SMTP_STARTTLS`; the `_TLS` variant has zero consumers and is a typo from `_STARTTLS` | High | Rename to `VAULTDMS_SMTP_STARTTLS` (or remove if duplicate) |

---

## Bucket B — Needs human review

> Criteria: exported / public surface; or possibly-dynamic reachability not fully ruled out; or sits adjacent to a §5 WIP area; or removal would have non-trivial blast radius.

### B.1 — Go (exported / WIP-adjacent / dispatch-table risk)

| Path | Symbol | Why review | Hint for the reviewer |
|---|---|---|---|
| `services/auth/internal/service/m365_exchange.go:206` | `snippet` | Log-truncation helper; could be the M365 audit utility per §5 | Comment block above it mentions the (tid, oid) refactor — likely WIP-adjacent. KEEP if planned for the (tid, oid) link table rollout. |
| `services/auth/internal/handler/middleware.go:92` | `Handler.RequireScope` | Middleware constructor; could be used by routes I missed | Search chi router definitions for `.Use(h.RequireScope(...))` — if unused, dead; if reserved for API-key scopes, keep |
| `services/auth/internal/scim/filter.go:57` | `IsUnsupported` | SCIM error helper; potentially called by SCIM provider | Check SCIM error-mapping paths |
| `services/auth/internal/sso/saml.go:74` | `LoadSPKeyMaterial` | Exported SAML SP key loader | Likely called from main.go SAML init at runtime |
| `services/billing/internal/flags/flags.go:54` | `Checker.IsEnabled` | Feature flag checker, "for middleware" | Per memory `project_license_wired_partial.md` — license enforcement partial; billing flags likely same. Could be WIP. |
| `services/billing/internal/provisioner/provisioner.go:40` | `New` constructor | unused constructor | Likely awaiting wiring in main.go |
| `services/billing/internal/repository/repository.go:33` | `GetOrg` | Repo accessor | Could be test-only or planned |
| `services/billing/internal/service/service.go:61, 66` | `IsFeatureEnabled`, `GetPlan` | Service methods | Per memory — license/billing partial. KEEP. |
| `services/connector/internal/email/document_client.go:217` | `createVersion` | IMAP→document version path | Could be deprecated or pending wire-up |
| `services/connector/internal/email/email.go:136` | field `mu` | unused mutex | Likely fossil from a refactor; safe to remove BUT verify no future goroutine concurrency planned |
| `services/connector/internal/email/email.go:573` | `emailIngestedPayload` | Looks like an event-payload builder | Could be a NATS publish path not yet wired |
| `services/connector/internal/email/format.go:18, 31` | `base64Of`, `formatBody` | Email-body helpers | Likely deprecated helpers — verify against IMAP poller |
| `services/connector/internal/handler/handler.go:276` | `Handler.oauthCallback` | Comment says "legacy per-provider callback preserved for compatibility" | Intentionally retained shim. KEEP unless you're sure no vendor still hits the legacy path. |
| `services/connector/internal/providers/base.go:82` | `BaseOAuth.EnsureValid` | OAuth lifecycle helper | Used reflectively from concrete providers — verify |
| `services/connector/internal/providers/google/google.go:43, 46, 75` | `Connector.Name, PollGmail, ListDriveFiles` | Provider-interface methods | Per §5 native-connector roadmap — Google slice partial. Drive import action pending. KEEP. |
| `services/connector/internal/service/google.go:105` | `GetGoogleConfigPublic` | Public config getter | Used by admin UI — verify route handler chain |
| `services/connector/internal/service/m365.go:75, 260` | `GetM365ConfigPublic, GetM365DriveItemContent` | M365 service methods | Per §5 — M365 connector partial. KEEP. |
| `services/connector/internal/service/service.go:268` | `Service.UpsertConnector` | Generic upsert | Likely used by both Google and M365 admin paths |
| `services/document/internal/model/annotation.go:100` | `AnnotationLayer` | Model type alias / constant | Could be used by serialization / tests |
| `services/document/internal/model/lifecycle.go:76` | `AllowedActions` | Lifecycle helper | Could be UI-only or test-only |
| `services/graphql-gateway/internal/loader/loader.go:91` | `New` | DataLoader bundle constructor | Per memory `project_graphql_gateway_dial_bugs.md` — graphql-gateway has known wiring issues. Likely WIP. |
| `services/notification/internal/service/slack.go:61, 76, 84, 133` | `NewSlackSender, slackSender.{Enabled, Send}, slackTextFrom` | Slack channel adapter | Code comment "§10.8 / E8 — Slack channel adapter" — feature work in progress. KEEP. |
| `services/search/internal/repository/repository.go:87, 94` | `UpdateLastRunAt, ListNotifiable` | Saved-search admin helpers | Used by Temporal alert workflow OR admin route — verify against `cmd/worker` |
| `services/search/internal/repository/saved_search_alerts.go:107, 122, 135, 240` | `UpdateLastMatchDocIDs, GetSavedSearchAdmin, scanSavedSearchAdminRow, ListSubscribersAdmin` | Admin endpoints | Verify admin handler registrations |
| `services/signature/internal/repository/repository.go:27, 219, 231` | `Repository.Create, Complete, UpdateSigners` | Signature repo methods | These look core — almost certainly used. **Strong suspicion** this is a deadcode false-positive due to interface satisfaction reachability. KEEP. |
| `services/storage/internal/scanner/mimecheck.go:74, 136` | `DetectFromReader, PeekHead` | MIME-sniff helpers | Could be ClamAV pre-check |
| `services/storage/internal/service/service.go:688` | `Service.scanObject` | ClamAV scan invocation | Method body docstring "scanObject streams the S3 object through ClamAV. An unreachable scanner [...]". Verify scan path; could be wired in NATS subscriber not seen by deadcode. |
| `services/workflow/internal/repository/repository.go:249` | `Repository.CompleteTask` | Task completion | Should be active code — investigate why deadcode misses it. Likely interface-table reachability lost. KEEP. |

### B.2 — Go module dependencies (`go mod tidy` diffs)

None of the diffs are **removable** deps. The differences are **direct vs indirect** drift — `go mod tidy` would re-classify them, not remove them. Examples:

- `services/auth`: `golang-jwt/jwt/v5` currently `// indirect`; tidy would mark direct.
- `services/connector`: `fsnotify` currently indirect; tidy → direct. `chi/v5` would be added as direct.
- `services/document`: `sergi/go-diff` indirect → direct; `golang-jwt/jwt/v5` would be added as indirect.

These are not Phase 2 cleanup targets — they are **directness rebalances** that should be done in a separate `chore: go mod tidy across modules` commit after the dead-code work, since `tidy` also changes the indirect block formatting.

### B.3 — Python (Bucket B)

| Path | Symbol | Tool | Reason for review |
|---|---|---|---|
| `services/intelligence/app/tasks/ocr.py:354,355` | locals `content_blob_id`, `region_pin` | vulture 100% | Per memory — intelligence-worker has known OCR edge-case drops. These could be debug vestiges OR diagnostics that should have been logged but weren't wired. Review against §5. |
| `services/intelligence/app/tasks/redact.py:331` | local `source_version_id` | vulture 100% | Same — could be a wire-up gap |
| `services/intelligence/requirements.txt` | DEP002: `uvicorn`, `pytest`, `pytest-asyncio`, `testcontainers` | deptry | Uvicorn is invoked at command line (not imported); same for pytest tooling. These are required even when no Python `import` references them. **Do NOT remove without confirming Dockerfile/entrypoint usage.** |
| `services/preview/requirements.txt` | DEP002: `uvicorn`, `httpx`, `pytest*` | deptry | Same as above |
| `services/intelligence/tests/test_vault_transit.py` | ~12 unused `vault_env` / `local_kek_env` fixture locals | vulture 100% | These look like pytest fixture parameters that mark dependencies but aren't used inside the body. Standard pytest pattern — KEEP unless converting fixtures. |

### B.4 — Node (Bucket B)

| Path | Symbol | Why review |
|---|---|---|
| `services/collaboration/src/connections.js:83` | `ConnectionManager.sendToUser` | 0 callers in repo, but public method on a manager — may be wired by future admin diagnostics or Wave-13 push channel |
| `services/collaboration/src/connections.js:94` | `ConnectionManager.getOnlineUsers` | Same |
| `services/collaboration/src/yjs-server.js:273` | `export const _rooms` | Comment "Exposed for tests." — no Node tests exist yet, but zero-cost to keep |

### B.5 — Java (Bucket B)

| Path | Symbol | Why review |
|---|---|---|
| `services/signature-signer/build.gradle.kts:54` | `compileOnly("org.apache.tomcat:annotations-api:6.0.53")` | Inline comment says it's required for `@Generated` on protoc-generated stubs at compile time. KEEP — load-bearing for codegen. |
| `services/signature-signer/build.gradle.kts:60-61` | `testImplementation` blocks | No test sources under `src/test/` yet. KEEP unless test plan moves elsewhere. |
| `services/signature-signer/README.md` (doc drift) | claims `KeystoreAdapter.java` exists | File not on disk. **Doc fix, not dead code.** Flag for README update. |

### B.6 — Web (Bucket B)

**B.6.1 — Unused `web/src/api/*` exports (per §4 — frontend equivalent of `pkg/*`).** Always Bucket B, never A.

50 exports flagged by knip across 26 API client modules. Examples:

| Module | Exports flagged unused |
|---|---|
| `api/anomaly.ts` | `getAnomalyConfig`, `updateAnomalyConfig` |
| `api/classify-corrections.ts` | `listClassificationCorrections`, `bulkReclassify` |
| `api/clauses.ts` | `getClause` |
| `api/client.ts` | `__resetHydrationBackoffForTests` (test-only — KEEP) |
| `api/comments.ts` | `updateComment` |
| `api/connectors.ts` | `listConnectors`, `disconnectM365`, `listM365Sites`, `listM365DriveItems` (M365 §5 — likely KEEP) |
| `api/doc-qa.ts` | `askQASync` |
| `api/documents.ts` | `restoreVersion` |
| `api/holds.ts` | `getHold`, `createHold` |
| `api/intelligence.ts` | `askQuestion`, `summarizeDocument`, `detectRedactions` |
| `api/ldap.ts` | `getActiveLDAPConfig` |
| `api/mfa.ts` | `listEnrolledMethodsByMFASession`, `registerPushDevice`, `getTenantMFAPolicy`, `saveTenantMFAPolicy` |
| `api/models.ts` | `getModelVersion`, `deleteTrainingExample` |
| `api/ner.ts` | `listEntityCorrections` |
| `api/notif-providers.ts` | `deleteTwilioConfig`, `deleteSMTPConfig` |
| `api/notification-prefs.ts` | `patchCell` |
| `api/privacy.ts` | `getDSR` |
| `api/redaction.ts` | `downloadUnredactedURL` |
| `api/retention.ts` | `getRetentionPolicy` |
| `api/search.ts` | `autocomplete`, `suggest` |
| `api/shareLinks.ts` | `listShareLinks`, `deleteShareLink` |
| `api/signatures.ts` | `deleteESignProviderConfig` |
| `api/smart-routing.ts` | `getSmartRoutingConfig`, `updateSmartRoutingConfig` |
| `api/tasks.ts` | `listTasks`, `getTask`, `updateTask`, `assignTask`, `unassignTask` (5 methods — entire admin slice?) |
| `api/webauthn.ts` | `stepUp` |
| `api/workflows.ts` | `startWorkflow`, `listDelegations`, `createDelegation`, `revokeDelegation` |
| `api/workspaces.ts` | `updateFolder`, `deleteFolder` |
| `api/ztShare.ts` | `revokeZTShare`, `getZTTelemetry` |

**Treat as a single review item.** Many will turn out to be used by routes via TanStack Router's file-based discovery (knip can't see those wires reliably). Recommend running a manual `grep -r '<exportname>(' web/src/routes web/src/components` for each before removing any.

**B.6.2 — shadcn primitives with 0 direct importers**

| Path | Status | Note |
|---|---|---|
| `web/src/components/ui/shadcn/accordion.tsx` | unused per knip | Likely transitive only |
| `web/src/components/ui/shadcn/calendar.tsx` | unused per knip | Pairs with `react-day-picker` (B.6.3) |
| `web/src/components/ui/shadcn/context-menu.tsx` | unused per knip | |
| `web/src/components/ui/shadcn/date-picker.tsx` | unused per knip | Pairs with `react-day-picker` |
| `web/src/components/ui/shadcn/drawer.tsx` | unused per knip | Pairs with `vaul` dep |
| `web/src/components/ui/shadcn/navigation-menu.tsx` | unused per knip | |
| `web/src/components/ui/shadcn/progress.tsx` | unused per knip | |
| `web/src/components/ui/shadcn/scroll-area.tsx` | unused per knip | |
| `web/src/components/ui/shadcn/tabs.tsx` | unused per knip | |
| `web/src/components/ui/shadcn/tooltip.tsx` | unused per knip | |
| `web/src/components/ui/form.tsx` | unused per knip | Pairs with `react-hook-form` dep (1 import = this file) |

Recommendation: **review as a set with B.6.3 (deps).** Removing a primitive's npm dep without removing the primitive (or vice versa) would leave a broken file.

**B.6.3 — npm deps flagged by knip — only 1 importer each, often the unused-primitive in B.6.2**

| Dep | Sole importer | Verdict |
|---|---|---|
| `react-hook-form` | `ui/form.tsx` (itself in B.6.2) | Chain — remove together if you remove `form.tsx` |
| `vaul` | `ui/shadcn/drawer.tsx` (itself in B.6.2) | Chain |
| `react-day-picker` | `ui/shadcn/calendar.tsx` (itself in B.6.2) | Chain |
| `date-fns` | `documents/ZTShareActivity.tsx` + `routes/_authenticated/index.tsx` (2 real callers) | KEEP — knip wrong |
| `react-dropzone` | `components/documents/DocumentUpload.tsx` (1 importer, but `DocumentUpload.tsx` itself flagged unused) | Chain — review upload flow |
| `zod` | `components/admin/RetentionPolicyForm.tsx` (1 caller, but may be more) | KEEP — verify route usage first |
| `@radix-ui/react-accordion`, `react-checkbox`, `react-navigation-menu`, `react-progress`, `react-scroll-area`, `react-tabs`, `react-tooltip` | each pairs with one shadcn primitive in B.6.2 | Chain |
| `@axe-core/react` (devDependency) | only `src/main.tsx` (1 caller, conditional in dev) | KEEP — a11y instrumentation |
| `@tailwindcss/vite` (devDependency) | not directly imported but referenced by `vite.config.ts` / postcss chain | KEEP — knip false positive |

**B.6.4 — Components flagged unused but ARE imported (knip routing blindspot)**

These will need manual route-by-route verification before any removal:

| File | Confirmed importers |
|---|---|
| `components/viewer/DocumentViewer.tsx` | imported by 4 files (verified) |
| `components/ai/AIChatPanel.tsx` | only self-reference found — possibly truly dead, but used to be live |
| `components/documents/DocumentUpload.tsx` | likely route-level lazy import |
| `components/documents/MetadataPanel.tsx` | uses `ComingSoon` placeholder — could be WIP |
| `components/documents/UploadReviewDialog.tsx` | likely route-level |
| ... ~45 more from knip's "Unused files (59)" list | each needs `grep -r '<basename>' web/src/routes` |

Recommendation: do not auto-remove ANY of these. Run a focused per-route trace.

**B.6.5 — Unlisted dependency**

`@radix-ui/react-visually-hidden` is imported by `web/src/components/documents/DocumentViewerModal.tsx` but is not in `package.json`. **Add it as a direct dep** (this is a missing-dep fix, not a removal).

### B.7 — Repo (Bucket B)

| Path | Type | Reason for review |
|---|---|---|
| `scripts/renumber-adrs.sh` | 0 refs | One-off ADR renumber tool; useful again next time ADRs renumber. KEEP. |
| `scripts/snapshot-for-claude.sh` | 0 refs | Dev tool for repo snapshots. Used ad-hoc. KEEP. |
| `scripts/backfill_ocr_quality.py` | 0 refs | One-off backfill — likely already run in prod. Could archive. |
| `scripts/dev-up.sh`, `scripts/dev-down.sh` | 0 / 1 refs | Dev convenience pair; likely used by humans even without grep hits |
| `scripts/migrate-health-region.mjs` | 1 ref | One-off migration helper; likely already done. Archive candidate. |
| `web/scripts/fix-directional-import.mjs`, `migrate-directional-icons.mjs`, `migrate-logical-tw.mjs`, `strip-tsc-unused-lucide.mjs`, `strip-unused-lucide.mjs` | 0 in-tree refs | One-off migration scripts for ADR 0107/0108 RTL/i18n work. Already run. Archive candidate. |
| `dms-admin.exe`, `server.exe` at repo root | 65 MB total | Gitignored (`*.exe` in `.gitignore:15`), NOT tracked. **No git action needed.** Recommend deleting locally to free disk. |
| `scripts/seed/seed.exe`, `web/coverage/` | local artifacts | Gitignored. Local-only. |
| `docs/reports/{PHASE_STATUS_2026-04-19, PROJECT_ANALYTICAL_REPORT_2026-04-18, BLUEPRINT_COMPLETION_PLAN_2026-04-19, TECH_STACK_AND_FEATURES_2026-04-19}.md` | self-referential | Stale Apr-18/19 snapshots. Superseded by `docs/STATE_OF_THE_PROJECT.md` and `docs/PROJECT_STATUS.md`. Archive candidates. |
| `docs/audit/00-summary.md` through `11-frontend-calls.md` | self-referential | Wave-1 audit artifacts. Likely complete. Archive candidate. |
| `DMS Architecture/*.md` (4 files) | self-referential | Pre-codebase planning notes. Archive candidate. |
| `restart-dev.ps1` (repo root) | referenced only in PROJECT_STATUS | PowerShell helper. Should probably move to `scripts/`. |
| Helm `VAULTDMS_LICENSE_MODE`, `VAULTDMS_LICENSE_JWT_SECRET` in `values-airgapped.yaml:68-69` | 0 consumers | Per memory — license enforcement partial. KEEP, track. |

---

## Bucket C — WIP / dynamic / KEEP

> Looks dead but is intentional. Cite the ADR / `PROJECT_STATUS` / known-gap / reachability source.

### C.1 — Go reachability misses

| Path | Symbol | Why KEEP |
|---|---|---|
| `services/workflow/internal/workflows/saved_search_alert.go:44` | `SavedSearchAlertWorkflow` | Registered in `services/workflow/cmd/worker/main.go:131` via `RegisterWorkflow`. Deadcode on `cmd/server` misses worker reachability. |
| `services/workflow/internal/workflows/saved_search_alert_schedule.go` | `SavedSearchAlertScheduleID`, `CreateSavedSearchAlertSchedule`, `DeleteSavedSearchAlertSchedule`, `ReconcileSavedSearchAlertSchedules`, `RegisterSavedSearchAlertSchedules` | Called from `cmd/worker/main.go:155` (`RegisterSavedSearchAlertSchedules`). Cross-package. |
| `services/workflow/internal/workflows/retention_schedule.go:37` | `RegisterRetentionSchedules` | Called from `cmd/worker/main.go:146`. |
| `services/policy/internal/service/service.go:205` | `ctxWithTenant` | Comment: "no-op shim retained for readability in call sites." Matches §5 tenant-id-from-context sweep. |

### C.2 — PAdES (ADR 0025, Wave 12.9b — deferred)

| Path | Symbols | Why KEEP |
|---|---|---|
| `services/signature/internal/pades/cms.go:61` | `signedAttrSet` type | PAdES partial implementation |
| `services/signature/internal/pades/parser.go:141` | `sigDictRE` var | PAdES partial |
| `services/signature/internal/pades/dss.go:57-225` | `embedDSS`, `encodeStream`, `refList`, `bytesToInt`, `findOriginalCatalog` | PAdES DSS/LTV scaffolding |
| `services/signature/internal/pades/ltv.go:397-403` | `NewEmbedder`, `Embedder.Embed` | LTV scaffolding |
| `services/signature/internal/pades/tsa.go:40-179` | `NewHTTPTSAClient`, `HTTPTSAClient.Stamp`, `extractGenTime`, `hashForTSA` | RFC 3161 TSP scaffolding |
| `services/signature-signer/src/main/java/io/vaultdms/signer/SignerService.java` | both RPCs (`sign`, `verify`) | Return `Status.UNIMPLEMENTED` — explicit Wave 12.9b deferral |

### C.3 — Native connectors §5 (only Google slice landed; M365/Salesforce/Drive-import partial)

| Path | Symbols | Why KEEP |
|---|---|---|
| `services/connector/internal/providers/m365/client.go:77-465` | `WithHTTPClient`, `Client.GetDriveItemContent`, `UploadDriveItem`, `ListMessages`, `SendMail`, `PostChannelMessage`, `graphPut`, `graphPostNoResp`, `toRecipients` | M365 connector partial (ADR 0112) |
| `services/connector/internal/providers/m365/m365.go:116, 121` | `Connector.Name`, `Connector.TenantID` | Same |
| `services/connector/internal/providers/m365/types.go:59` | `rawMessage.materialize` | Same |
| `services/connector/internal/providers/google/google.go:43, 46, 75` | `Connector.Name`, `PollGmail`, `ListDriveFiles` | Google native connector — Drive import action pending per §5 |

### C.4 — Web (dynamic / WIP)

| Surface | Why KEEP |
|---|---|
| `web/src/routes/**/*.tsx` (82 files) | TanStack Router file-based discovery (auto-imported by build plugin) |
| `web/src/routeTree.gen.ts` | Generated |
| `web/src/generated/load-test-reports.ts` | Generated (ADR 0105) |
| `web/public/locales/{en,ar}/*.json` | i18n bundles loaded dynamically by i18next-http-backend (ADR 0106) |
| `web/src/test/mocks/{handlers,server}.ts` | MSW handlers, used by test setup |
| `web/src/routes/_authenticated/admin/integrations/ipaas.tsx` | ADR 0090 — tile/chip commented out pending Zapier/Make/n8n app publication |
| `web/src/routes/_authenticated/admin/tenant/license.tsx` | ADR 0095 — license UI partial wire-up |
| `web/src/components/admin/M365ConnectorModal.tsx` and similar | §5 native-connector roadmap |

### C.5 — Other surfaces (whole-area WIP)

| Path | Why KEEP |
|---|---|
| `mobile/**` (17 ts/tsx files, Expo) | §5 — skeleton, divergent auth model intentional |
| `addins/word/**`, `addins/outlook/**` | §5 — Office.js scaffolding (ADR 0113) |
| `extension/**` (MV3 browser extension) | §5 — Phase 1 shipped, Phase 2 placeholders, blocked on `/api/v1/oauth/*` backend per ADR 0097 |
| `services/collaboration/src/{nats-bridge.js, yjs-server.js, yjs-persistence.js, handler.js}` | WS/NATS/CRDT dispatch — §4 dynamic-reachability paths |

### C.6 — Repo (deploy / CI / docs always-keep)

| Path | Why KEEP |
|---|---|
| `.github/workflows/{ci, esign-sandbox, images, openapi-pages, release}.yml` | Event-driven, never orphan |
| `deploy/license/{dev.priv.pem, dev.pub.pem}` | ADR 0095 — used by `pkg/license` JWT validator |
| `deploy/helm/vaultdms/{values-airgapped.yaml, values-onprem.yaml, values.yaml}` | Helm verification deferred per memory; do not remove |
| `deploy/ansible/**` | ADR 0093 on-prem provisioner |
| `deploy/load-test/**` | ADR 0105 — harness alive even if real campaign deferred |
| `deploy/regions/uae-central.yaml` | ADR 0110 regional override |
| `deploy/gateway/{kong, routes, rate-limits}.yaml` | Kong gateway config, referenced by `pkg/archtest/gateway_routes_test.go` |
| `deploy/monitoring/{alerts/, dashboards/, provisioning/}` | Prometheus + Grafana provisioning |
| `deploy/helm/vaultdms/templates/**` | Helm rendering via Chart + `_helpers.tpl` |
| `docker-compose.{passkeys, prebuilt, prod}.yml` + `deploy/docker-compose.integration.yml` | All actively referenced by Makefile/docs/testharness |
| `docs/security/**`, `docs/adr/**` | CODEOWNERS-protected — flag only |
| `scripts/pentest-labels.sh`, `soc2-evidence.sh`, `check-no-background-in-handlers.sh`, `check-no-math-rand.sh`, `check-outbox-only-publishing.sh`, `openapi/check-drift.sh`, `mutesting/`, `airgap/build-bundle.sh` | All referenced by CI / Makefile / playbooks |
| `cmd/license-gen/` + `pkg/license/**` | ADR 0095 partial wire-up; KEEP scaffolding |

---

## Notes / unverifiable items

1. **`services/storage/internal/service/service.go:688 scanObject`** — the comment in the source ("scanObject streams the S3 object through ClamAV. An unreachable scanner …") strongly suggests this IS the AV scan entrypoint. It's flagged as unreachable by static analysis but the wire-up is likely via a NATS consumer registered in a goroutine. **Bucket B — needs human verification before any action.**

2. **Workflow Temporal symbols** — moved several to Bucket C after manually cross-checking `cmd/worker/main.go`. The deadcode tool reports reflect reachability **per main package**, not workspace-wide reachability. If you accept any of these as removable, you'd risk breaking the worker.

3. **Frontend `web/src/api/*` unused exports (50 items)** — knip cannot trace TanStack Router lazy chunks reliably. All 50 entries are in Bucket B.6.1 and need per-export `grep` verification.

4. **Coverage artifacts under `web/coverage/`** — knip flags them as "unused files." They ARE artifacts, not source. Should already be gitignored (verify `web/.gitignore`). If they're tracked, that's an unrelated `.gitignore` fix.

5. **`go mod tidy` diffs** are all **direct/indirect rebalances**, not removals. Out of scope for the dead-code pass; do separately.

6. **One non-finding worth flagging:** `services/mcp-server` has no `go.sum`. That's anomalous in a vendored / reproducible workspace. Not a removal candidate — just unusual.

---

## What I'm NOT proposing for Bucket A

Even when static tools call them dead, these have been deliberately kept out of "safe to remove":

- Anything exported in `pkg/*` (cross-workspace consumption risk).
- Anything in `web/src/api/*` (lazy-chunk routing blindspot).
- Anything matching §5 WIP (license, M365/Salesforce native connectors, iPaaS, ESIGN mock, mobile/addins/extension, M365 (tid,oid) refactor, tenant-id-from-context sweep, OCR worker edge cases, PAdES).
- Anything CODEOWNERS-protected (`docs/security/`, security gates).
- Test files (they pin behavior).
- Anything in a `migrations/` directory.
- Generated code or anything with `// Code generated DO NOT EDIT`.

---

## Decision request — Phase 2 approval

Please review and indicate which of the following you approve for Phase 2 removal. Default is "do nothing" — anything you don't explicitly approve will stay.

### Quick-decision groups

- [ ] **A.1 — Go.** Remove `marshalMap` in `services/document/internal/handler/mappers.go`. (1 line, no risk.)
- [ ] **A.2 — Python unused imports + locals.** ~30 F401/F841 fixes in `services/intelligence` + `services/preview`. Apply via `ruff check --fix` ONLY on the listed files. Test files excluded from this group.
- [ ] **A.3 — Node.** Remove `uuid` dep from `services/collaboration/package.json`.
- [ ] **A.4a — Web orphan components.** Delete the 6 confirmed-dead .tsx files (`CommandPalette`, `ErrorState`, `SearchInput`, `Breadcrumb`, `FolderTree`, `WorkspaceSelector`).
- [ ] **A.4b — Web unused deps.** Remove `react-intl`, `@dnd-kit/core`, `@dnd-kit/sortable`, `@hookform/resolvers`, `@radix-ui/react-switch`, `graphql` from `web/package.json`. (Verify urql peer-dep on graphql first.)
- [ ] **A.5a — Empty ops dirs.** Decide what to do with the `ops/prometheus/prometheus.yml/` and `ops/prometheus/rules/` empty directories — they appear to be filesystem accidents.
- [ ] **A.5b — Env key typo.** Fix `VAULTDMS_SMTP_TLS` → `VAULTDMS_SMTP_STARTTLS` in `deploy/helm/vaultdms/values-onprem.yaml:47`.
- [ ] **Bucket B items you want to promote to A** — list them by name in your reply.

After approval I will:
1. Cut a `chore/dead-code-cleanup` branch from `chore/eslint-flat-config`.
2. Remove **one logical group per commit**, lowest-risk first.
3. Run the §9 verification gate (`go build` + `go vet` + `staticcheck` per touched module; `npm run build` + `npm test` for `web/`; ruff for Python) after every commit.
4. Revert any commit that turns the gate red and reclassify the item to Bucket B.
5. Produce `cleanup-report.md` with before/after counts.

**No Phase 2 work will start until you reply.**
