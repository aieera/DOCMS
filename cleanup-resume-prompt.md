# Resume Prompt — Finish the VaultDMS Dead-Code Cleanup

Paste the section between the `---PROMPT START---` and `---PROMPT END---` markers below into a fresh Claude Code session inside the `c:\Users\dell\Documents\DOCMS` working directory. It is self-contained — it tells the assistant what was already done, what's left, the reachability/WIP rules, the verification gate, the commit conventions, and exactly which artifacts to read first.

If you skip the read step, the assistant will redo work that's already on disk.

---PROMPT START---

You are a senior platform engineer resuming a CONSERVATIVE dead-code and tidiness pass on VaultDMS, a multi-tenant enterprise DMS (Go workspace + Python workers + Node collab + Java sidecar + React/Vite frontend + Office add-ins + browser extension + Helm/Ansible deploy assets + ~65 ADRs).

A previous session has already done **Phase 1 (full inventory)** and **Phase 2 part 1 (the safe Bucket A subset)**. The work landed on branch `chore/dead-code-cleanup` and has been pushed to `origin`. Your job is to finish the remaining items.

## 0. Read these FIRST, in this order. Do not skip.

1. `cleanup-inventory.md` (at repo root) — Phase 1 inventory with Buckets A/B/C, methodology, exclusions. Reachability and WIP rules.
2. `cleanup-report.md` (at repo root) — what was already done in Phase 2 part 1, what was deferred and why, suggested follow-ups.
3. `CLAUDE.md` (at repo root) — project conventions (per-service migrations, outbox invariant, RLS, KEK, ADR culture).
4. `docs/architecture.md` — confirm the architecture before touching cross-service code.

After reading those four files, do NOT re-run the per-language detection tools (staticcheck, deadcode, ruff, vulture, deptry, knip, ts-prune, depcheck). The inventory is current as of 2026-05-27 and re-running them costs ~5 min for net-zero new information. Only re-run a tool if you've made a change in the same module and need to confirm the §9 gate.

## 1. Prime Directive — non-negotiable

**Nothing you do may change the runtime behavior, public API surface, wire contracts, or build/test outcomes of the project.** When in doubt, keep the code and flag it rather than remove it. A false positive that deletes live or future code is far worse than leaving a dead symbol in place.

You must NOT:

- Modify generated code (`proto/gen/go/`, `proto/gen/openapi/`, generated mocks, `// Code generated ... DO NOT EDIT`).
- Touch DB migrations.
- Change proto contracts, `events.proto` payloads, REST/grpc-gateway annotations, GraphQL persisted-query allow-list, or MCP method names.
- Remove anything that breaks tenant isolation (RLS), the transactional outbox, envelope-encryption / KEK flows, or `pkg/archtest` invariants.
- Reformat or mass-edit untouched files (no blame-polluting reflow).
- Modify CODEOWNERS-protected paths (`docs/security/`) or CI security gate config without explicit human sign-off (flag only).

## 2. §4 reachability guards — rule these out before removing anything

Static tools over-report dramatically in this repo. Before flagging a symbol/file/dep as removable, rule out all of:

- **gRPC** — `Register*Server` registration; symbols are reachable over the wire even if no static caller.
- **NATS** — `dms.{domain}.{action}.v1` subject dispatch. Cross-check `pkg/events` coverage-matrix test and the `nats-subjects` CI stage: every emitted subject must keep ≥1 consumer.
- **Transactional outbox** — `pkg/database` outbox repo + publisher. Validate against `scripts/check-outbox-only-publishing.sh`.
- **Temporal** — workflow/activity funcs are registered via `RegisterWorkflow`/`RegisterActivity` on `cmd/worker`, not `cmd/server`. Deadcode run against `cmd/server` misses worker reachability. Always cross-check both mains.
- **OPA** — `policy.rego` inputs; Go structs feeding OPA may have no static caller.
- **MCP JSON-RPC dispatch** (`services/mcp-server`) — methods invoked by name via a dispatch table.
- **grpc-gateway** — REST handlers generated from `google.api.http` annotations on the `document` service; WOPI endpoints.
- **Build tags** — re-run analysis with `-tags integration` (9 files in repo).
- **TanStack Router file-based routes** — 82 files in `web/src/routes/**` auto-discovered; knip can't see them.
- **Lazy/dynamic imports**, **i18n namespace JSONs**, **MSW handlers** — runtime-loaded.
- **`web/src/api/*`** — 66 client modules consumed across lazy chunks. Flag "unused" verdicts here as Bucket B, never A.
- **shadcn primitives** — may be imported transitively even if static analysis says zero direct importers.
- **Reflection / config / DI / `var _ =` keep-alive patterns** — search for `var _ = <symbol>` before removing any Go private symbol. The previous session aborted A.1 (`marshalMap`) because of exactly this pattern.

## 3. §5 — Intentionally-incomplete WIP — KEEP, don't remove

These look dead but are deliberately partial. If something matches one of these, it goes in "WIP — keep" (Bucket C), not "Safe to remove":

- **License enforcement** (ADR 0095, `pkg/license`): shipped only in `document` service; 12 other services + grace middleware + seat counting + per-feature gates pending.
- **Native connectors** (ADR 0089): Google OAuth slice landed; 7 more vendors pending (Drive import action, Salesforce, M365 wrappers including `/api/v1/oauth/*` backend the browser extension blocks on).
- **iPaaS** (ADR 0090): backend + admin UI built, tile/chip commented out until Zapier/Make/n8n apps publish — commented-out code is intentional.
- **DocuSign live cutover**: still on `ESIGN_MOCK_OK=true`; live path is staged.
- **PAdES** (ADR 0025): scaffolded but Wave 12.9b deferred — all of `services/signature/internal/pades/*` and `services/signature-signer/SignerService.java`.
- **mobile/** (Expo), **addins/** (Office.js), **extension/** (MV3 Phase 2) — scaffolding by design.
- **M365 email → (tid, oid) link-table refactor** (services/auth/internal/service/m365_exchange.go) and the **tenant-id-from-context sweep** (`services/policy/internal/service/service.go:205 ctxWithTenant` is a deliberate no-op shim per the comment) — open security follow-ups.
- **Intelligence worker** edge cases (silent OCR drops) — known bug, leave alone.

## 4. What's already done — DO NOT redo

Branch `chore/dead-code-cleanup` (pushed to origin) carries 8 commits off `chore/eslint-flat-config`:

```
chore(cleanup): add Phase 2 cleanup report
chore(web): drop unused react-intl + @radix-ui/react-switch deps
chore(web): remove 6 superseded layout/ui orphan components
chore(collaboration): drop unused 'uuid' dependency
chore(preview): drop unused typing.Optional import
chore: ignore Python __pycache__/*.pyc
chore(intelligence): drop unused imports (ruff F401/F811)
chore(cleanup): add Phase 1 dead-code inventory
```

Approx ~715 lines removed, all §9 gates green per commit, nothing reverted.

**Do not re-attempt these same removals.** They are already on the branch.

## 5. What's still on the table — your work list

### 5.1 — Deferred Bucket A items (low risk, fast)

These were left for explicit human nod or peer-dep verification. Confirm with the user before touching, then apply with the §9 gate.

**5.1a — `ops/prometheus/{prometheus.yml/, rules/}` empty directories.** Both are zero-byte directories created 2026-05-15 where files were expected. Either:
- Restore as files (you'd need to find what was supposed to live there — check git log, `deploy/monitoring/`, helm templates), OR
- Remove the directories (if config has migrated to `deploy/monitoring/` already).

Ask the user which interpretation is correct. Do not act blind.

**5.1b — `VAULTDMS_SMTP_TLS` typo in `deploy/helm/vaultdms/values-onprem.yaml:47`.** Code consumes `VAULTDMS_SMTP_STARTTLS`; the `_TLS` variant has zero consumers. Rename the key (or remove if duplicate). One-line Helm values change — get explicit nod first since it touches `deploy/`.

**5.1c — Remaining A.4b deps**, deferred for peer-dep verification:

- `graphql` — almost certainly a `urql` peerDependency at runtime. Verify by reading `web/node_modules/urql/package.json` `peerDependencies` and `@urql/exchange-persisted` peerDeps. If `graphql` is a peer dep, KEEP. Otherwise remove.
- `@hookform/resolvers` — the zod-bridge for `react-hook-form`. Currently `react-hook-form` is imported by exactly one file (`web/src/components/ui/form.tsx`), which is itself in Bucket B as orphan-candidate. Decide as a chain: if `form.tsx` is dead → drop all three (`react-hook-form`, `@hookform/resolvers`, `zod` ONLY if zod has no other consumers — check first).
- `@dnd-kit/core`, `@dnd-kit/sortable` — grep for `useDraggable`, `useSortable`, `DndContext` across `web/src` (including route files). If truly zero callers, removable. If anything in `routes/` uses them, KEEP.

### 5.2 — Bucket B follow-ups worth doing (manual review)

Each of these needs human-judgment review with the codebase open. Cite evidence in commit messages.

**5.2a — `web/src/api/*` unused exports (50 items per knip).** Knip can't trace TanStack Router lazy chunks. Each export needs `grep -r '<exportname>(' web/src/routes web/src/components` to confirm zero true usage. The full list lives in `cleanup-inventory.md` §B.6.1. Recommend: walk the list, batch deletions per file, one commit per file (small) or one commit per logical group (e.g. all `mfa.ts` orphan exports together).

**5.2b — Two F841 unused locals** in `services/intelligence/app/tenant_llm_config_repo.py:109,115` (`api_key_encrypted`, `api_key_set_at_clear`). Ruff considers them "unsafe to fix" (DB query result locals). Read the function bodies. They are likely either (a) debug remnants → drop them, or (b) a wire-up gap → either log the value or pass it onward. Pick the right one.

**5.2c — shadcn primitives with zero importers + their radix deps (B.6.2 + B.6.3 chain).** 11 primitives and ~9 chained radix deps. Decide as a unit:
- For each unused primitive, confirm grep shows zero importers in `web/src` (excluding `node_modules`, `coverage/`, `dist/`).
- If zero, remove the primitive `.tsx` AND its paired radix dep in the same commit.
- Verify with `tsc --noEmit && vite build` after each.

**5.2d — Go module dependency direct/indirect rebalances.** Run `go mod tidy` per module — auth, connector, document, and mcp-server need direct/indirect reshuffling. mcp-server has no `go.sum` (anomalous). Do this as one standalone `chore: go mod tidy across modules` commit AFTER all dead-code work — NOT bundled with removals.

**5.2e — Stale doc snapshots** in `docs/reports/` (4 files dated 2026-04-18/19) and `docs/audit/00-summary.md`–`11-frontend-calls.md`. Superseded by `docs/STATE_OF_THE_PROJECT.md` and `docs/PROJECT_STATUS.md`. Either delete or move to `docs/archive/`. Ask the user.

**5.2f — One-off migration scripts** under `web/scripts/{fix-directional-import, migrate-directional-icons, migrate-logical-tw, strip-tsc-unused-lucide, strip-unused-lucide}.mjs`. These ran once for ADR 0107/0108 RTL/i18n work. Confirm via git log that they've been executed; then move to `web/scripts/archive/` or delete. Ask the user.

**5.2g — Repo-root binaries** (`dms-admin.exe`, `server.exe`). Already gitignored, NOT tracked. ~65MB of local-only build cruft. Just tell the user to `rm` them locally if they want disk back — no repo action.

### 5.3 — Bucket B Go items to investigate (read before deciding)

Each needs a careful read. **Default: KEEP** unless reachability is genuinely zero across all §4 paths. Many of these touched WIP or interface-table reachability the prior session couldn't fully rule out.

- `services/auth/internal/service/m365_exchange.go:206 snippet` — JWT-truncation helper. The §5 M365 (tid, oid) refactor is open; might be planned for re-use. KEEP unless certain.
- `services/auth/internal/handler/middleware.go:92 Handler.RequireScope` — search chi router setup for `.Use(h.RequireScope(...))`; verify against API-key scope routes.
- `services/auth/internal/scim/filter.go:57 IsUnsupported`, `services/auth/internal/sso/saml.go:74 LoadSPKeyMaterial` — check SCIM error mapping and SAML init in `cmd/server/main.go`.
- `services/billing/internal/{flags, provisioner, repository, service}/*` flagged unused — per memory `project_license_wired_partial.md`, billing/license are partial. Default KEEP, mark as Bucket C if confirmed WIP.
- `services/connector/internal/email/*` unused — `createVersion`, `mu`, `emailIngestedPayload`, `base64Of`, `formatBody`. Verify against IMAP poller code path. Could be deprecated.
- `services/connector/internal/handler/handler.go:276 oauthCallback` — comment says "legacy per-provider callback preserved for compatibility." Bucket C unless you know no vendor hits the legacy path.
- `services/connector/internal/providers/m365/*`, `providers/google/{Name, PollGmail, ListDriveFiles}` — §5 native-connector WIP. KEEP.
- `services/document/internal/model/{annotation.AnnotationLayer, lifecycle.AllowedActions}` — likely used by serialization or tests. Check `*_test.go` files in `services/document/` before removing.
- `services/graphql-gateway/internal/loader/loader.go:91 New` — per memory `project_graphql_gateway_dial_bugs.md`, graphql-gateway has known wiring issues; this might be unwired-on-purpose WIP.
- `services/notification/internal/service/slack.go` — Slack channel adapter scaffolding per the docstring ("§10.8 / E8"). KEEP as WIP.
- `services/search/internal/repository/saved_search_alerts.go` admin helpers — verify against admin route handlers.
- `services/signature/internal/repository/repository.go {Create, Complete, UpdateSigners}` — these are core methods. Almost certainly a deadcode false-positive (interface satisfaction). KEEP.
- `services/storage/internal/service/service.go:688 scanObject` — likely the ClamAV NATS-consumer path. Verify reachability via storage's NATS subscriber init before any action. **Strong default: KEEP.**
- `services/workflow/internal/repository/repository.go:249 CompleteTask` — should be active. Investigate why deadcode misses it. Likely interface-table reachability. KEEP.

### 5.4 — Items explicitly NOT to do

- Do NOT touch `services/signature/internal/pades/*` — PAdES is Wave 12.9b deferred (ADR 0025).
- Do NOT touch `services/policy/internal/service/service.go:205 ctxWithTenant` — deliberate no-op shim per the comment, matches §5 tenant-id-from-context sweep.
- Do NOT touch `services/document/internal/handler/mappers.go:304 marshalMap` — has `var _ = marshalMap` keep-alive a few lines below.
- Do NOT touch `services/workflow/internal/workflows/saved_search_alert*.go` or `RegisterRetentionSchedules` — they are registered by `services/workflow/cmd/worker/main.go`, not the server main (deadcode on cmd/server misclassifies).
- Do NOT touch `web/src/routes/_authenticated/admin/integrations/ipaas.tsx` or anything iPaaS-related (ADR 0090).
- Do NOT touch any file under `docs/security/` or `docs/adr/` — CODEOWNERS-protected; flag only.
- Do NOT touch `pkg/license/*`, `cmd/license-gen/*` — ADR 0095 partial wire-up.
- Do NOT touch any test file (`*_test.go`, `*.test.{ts,tsx}`, `e2e/*.spec.ts`, `pkg/testharness/*`, `pkg/testutil/*`, `pkg/archtest/*`) — they pin behavior.

## 6. Tooling — already installed

You don't need to install anything. Verified present on PATH at session start:

- Go (1.26.2 with go.work), `staticcheck`, `deadcode`, `golangci-lint`, `govulncheck`, `buf`, `migrate` — at `C:\Users\dell\go\bin\`
- Node 22, npm 11.10 — system PATH
- Python 3.13 — `C:\Users\dell\AppData\Local\Programs\Python\Python313\python.exe`
- `ruff`, `vulture`, `deptry` — `C:\Users\dell\AppData\Roaming\Python\Python313\Scripts\` (NOT on PATH — call by full path or set PATH in the session)
- `knip`, `ts-prune`, `depcheck` — via `npx --yes` (no install needed)

If `go mod tidy` produces `.bak` files, ensure they're cleaned up — the prior session's pattern was `cp go.mod go.mod.bak && go mod tidy; diff; mv go.mod.bak go.mod`.

## 7. §9 verification gate — run after EVERY change set

A commit is only allowed to stand if all of these stay green (subset relevant to the language touched, plus always-on invariants):

**Always:**
- `go build ./...` across the touched modules (per-module — workspace root cannot resolve `./...`)
- `go vet ./...`
- `pkg/archtest` tests pass (`go test ./...` in `pkg/`)
- `bash scripts/check-outbox-only-publishing.sh`
- `bash scripts/openapi/check-drift.sh`
- Linters green: `go vet`, `staticcheck`, `golangci-lint`; `ruff` for Python; ESLint for web/

**When relevant:**
- `web/`: `tsc --noEmit`, `npx --yes vite build`, `npx --yes vitest run` (vitest is slow — run only when touching components that have tests).
- Python: their pytest suite if it runs locally (often it doesn't without docker — accept that).
- Node: `node --check src/*.js` per file at minimum.

If you can't run a gate (missing service, no Docker), do NOT proceed on that area. Mark "unverifiable, deferred" and move on.

## 8. Commit conventions

- One logical change per commit. Never bundle unrelated removals.
- Use the existing message style: `chore(<service-or-area>): <imperative summary>` with a 1–3 line body explaining what was removed and the §4 reachability evidence.
- Sign with `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.
- Use HEREDOC for the commit message body (the project standard).
- After every commit run the §9 gate. If anything goes red, immediately `git revert HEAD` and reclassify the item to Bucket B with a note.
- Never amend or force-push.
- Push only when the user asks.

## 9. Deliverables for this session

1. Read the four files in §0.
2. Confirm understanding of the Prime Directive, §4 reachability rules, §5 WIP keep-list, and §7 "do NOT do" list before touching anything.
3. Ask the user which of §5.1 (deferred Bucket A) and §5.2 (Bucket B follow-ups) they want done — quote the IDs (`5.1a`, `5.2c`, etc.) so the user can pick by name.
4. Work through the approved items one logical commit at a time, with the §9 gate after each.
5. Append to `cleanup-report.md` (do NOT overwrite — add a new section "## Phase 2 part 2 — <date>") with what you removed and gate results.
6. Stop and present results. Do not push without explicit approval.

Start by reading `cleanup-inventory.md` and `cleanup-report.md` and then asking the user which §5.1 / §5.2 items they want done.

---PROMPT END---

## Notes for you (the human) before pasting

- **Branch state at handoff (2026-05-27):** `chore/dead-code-cleanup` is 8 commits ahead of `chore/eslint-flat-config`, pushed to `origin`. Last commit `10cf992`.
- **Open PR URL** when you're ready: `https://github.com/aieera/DOCMS/pull/new/chore/dead-code-cleanup`
- **The two artifact files** the resumed session will read (`cleanup-inventory.md` + `cleanup-report.md`) live at repo root and are tracked on the branch. Do not delete them.
- If you do any work on the branch between now and resuming (e.g., merge in upstream, run formatters), pull the branch state first so the resumed session reads a current `cleanup-report.md`.
- If you rebase the branch onto a new base, the resume prompt still works — just note the new base in the user reply when prompted.
- Estimated time for §5.1 (deferred Bucket A) ≈ 30 min including gates. §5.2 (Bucket B) is variable, often 1–2 hours depending on how aggressive you want to be with `web/src/api/*` exports.
