# VaultDMS Cleanup Report — Phase 2

**Branch:** `chore/dead-code-cleanup` (off `chore/eslint-flat-config`)
**Date:** 2026-05-27
**Status:** Approved Bucket A subset applied. Not pushed.

---

## Commits on this branch

| # | SHA | Message | Files | Net lines |
|---|---|---|---|---|
| 1 | `ce386ab` | `chore(cleanup): add Phase 1 dead-code inventory` | +`cleanup-inventory.md` | +467 |
| 2 | `2678b2d` | `chore(intelligence): drop unused imports (ruff F401/F811)` | 19 `.py` files | -23 |
| 3 | `b0cf03f` | `chore: ignore Python __pycache__/*.pyc` | `.gitignore` + 25 untracked `.pyc` | (gitignore + untrack only) |
| 4 | `65aab20` | `chore(preview): drop unused typing.Optional import` | `services/preview/app/processors/image.py` | -1 |
| 5 | `bb0f8c1` | `chore(collaboration): drop unused 'uuid' dependency` | `services/collaboration/package.json` | -1 |
| 6 | `ca06367` | `chore(web): remove 6 superseded layout/ui orphan components` | 6 `.tsx` files | -447 |
| 7 | `d1abc63` | `chore(web): drop unused react-intl + @radix-ui/react-switch deps` | `web/package.json` + lockfile | -244 (lockfile churn included) |

**Branch summary:** 7 commits, ~715 lines removed from production code paths, 25 stray `.pyc` files un-tracked + future Python build-artifacts gitignored.

---

## Per-commit verification gate (§9)

| # | Group | Gate run | Result |
|---|---|---|---|
| 2 | A.2 intelligence | `ruff check app/ --select F401,F841,F811` re-run; `python -m py_compile` on each of 19 changed `.py` files | ✅ ruff: 2 errors remaining (2 F841 deferred to Bucket B); 0 compile errors |
| 3 | gitignore fix | n/a (no source change) | ✅ |
| 4 | A.2 preview | ruff re-run; `py_compile image.py` | ✅ ruff: 0 errors; compile ok |
| 5 | A.3 collaboration | `node --check src/*.js` on all 7 source files | ✅ all 7 ok |
| 6 | A.4a web orphans | `tsc --noEmit`; `vite build` | ✅ tsc clean; vite built 3060 modules, no missing imports |
| 7 | A.4b two deps | `npm install` (clean tree); `tsc --noEmit`; `vite build` | ✅ 16 packages dropped (2 direct + 14 transitive); tsc clean; vite ok |

No commit triggered the gate red. Nothing reverted.

---

## What was approved and applied

### A.2 — Python unused imports

**Intelligence (commit `2678b2d`):** removed 28 unused imports and one F811 redefinition across 19 files:

- `app/dedupe.py` (asyncio, typing.Optional)
- `app/llm_gateway.py` (AirGappedError, BreakerOpenError, BudgetExceededError)
- `app/llm_routing.py` (dataclasses.field)
- `app/models/embedder.py` (typing.Optional)
- `app/nats_consumer.py` (collections.defaultdict, compliance_scan F811 redef, extract_fields, local `exc`)
- `app/qa_persist.py` (typing.Any)
- `app/tasks/anomaly_detect.py` (qdrant_client.models.MatchAny)
- `app/tasks/auto_tag.py` (typing.Any, publish_cloudevent)
- `app/tasks/duplicate.py` (datasketch.MinHashLSH, numpy)
- `app/tasks/lang_detect.py` (typing.Any)
- `app/tasks/model_evaluate.py` (typing.Any)
- `app/tasks/model_retrain.py` (typing.Any)
- `app/tasks/ocr.py` (signal, contextlib.contextmanager)
- `app/tasks/ocr_quality.py` (typing.Any)
- `app/tasks/rag.py` (json, celery_app)
- `app/tasks/smart_route.py` (collections.Counter, typing.Any)
- `app/tasks/translate.py` (typing.Any)
- `app/tenant_classifier.py` (typing.Any)
- `app/tenant_llm_config_repo.py` (time import — 2 F841 locals deliberately kept; see Bucket B notes)

**Preview (commit `65aab20`):** removed `typing.Optional` from `app/processors/image.py`.

### A.3 — Node `uuid` dep

Removed `"uuid": "^9.0.0"` from `services/collaboration/package.json`. Verified zero `require`/`import` references; code uses `crypto.randomUUID()`.

### A.4a — Six web orphan components

Deleted (447 lines):

- `web/src/components/ui/CommandPalette.tsx` (superseded by `ui/shadcn/command.tsx`)
- `web/src/components/ui/ErrorState.tsx`
- `web/src/components/ui/SearchInput.tsx`
- `web/src/components/layout/Breadcrumb.tsx` (duplicate of `breadcrumbs.tsx` exporting `Breadcrumbs`)
- `web/src/components/layout/FolderTree.tsx`
- `web/src/components/layout/WorkspaceSelector.tsx`

All six had zero importers in `web/src` (verified via knip + grep). The only mention was a header comment in `web/e2e/40-autocomplete.spec.ts` referencing ADR 0084 by feature name, not the deleted file — the e2e test runs against the live `shadcn/command.tsx` palette.

### A.4b partial — two web npm deps

Removed from `web/package.json`:

- `react-intl` (project standardized on `react-i18next` per ADR 0106; zero source refs)
- `@radix-ui/react-switch` (primitive had no consumers in `web/src`)

`npm install` removed 16 packages (2 direct + 14 transitive). Lockfile updated.

### Bonus — `.gitignore` Python rules

Added `__pycache__/`, `*.pyc`, `*.pyo` to `.gitignore` (commit `b0cf03f`) and untracked 25 pre-existing `.pyc` files that had slipped into the repo over time.

---

## What was deferred from Phase 1's Bucket A

### A.1 — `marshalMap` — RECLASSIFIED to Bucket C

While preparing the removal I noticed the file ends with:

```go
var _ = marshalMap // keep exported-ish helper available to future handlers
```

This is a deliberate keep-alive pattern with an explicit author-intent comment. Staticcheck did not flag `marshalMap` because the `var _ = …` line counts as a "use"; only the call-graph–based `deadcode` tool flagged it. The Phase 1 inventory missed the keep-alive marker — apologies. **Reclassified to Bucket C (WIP / intentional).** No change made.

### A.4b — four deps deferred for peer-dep verification

- `graphql` — likely a `urql` peerDependency at runtime; needs verification before removal
- `@hookform/resolvers` — pairs with `react-hook-form` (still in tree; `ui/form.tsx` is in Bucket B)
- `@dnd-kit/core`, `@dnd-kit/sortable` — likely pair with a drag-drop feature loaded via a route lazy chunk

### A.5a — empty `ops/prometheus/{prometheus.yml/, rules/}` dirs

Deferred pending user intent: should these be **restored as files** (i.e., `prometheus.yml` was supposed to live there) or **deleted** (config migrated to `deploy/monitoring/`)?

### A.5b — Helm `VAULTDMS_SMTP_TLS` typo rename

Deferred pending explicit nod (touches `deploy/`).

---

## Suggested follow-ups (out of scope here, flagged only)

1. **Resolve the four deferred deps** with a peer-dep check before merging this branch into mainline cleanup.
2. **Bucket B `web/src/api/*` unused exports** (50 items per knip) — needs per-export `grep -r '<name>(' web/src/routes web/src/components` because knip can't trace TanStack Router lazy chunks.
3. **Two F841 unused locals** in `services/intelligence/app/tenant_llm_config_repo.py:109, 115` (`api_key_encrypted`, `api_key_set_at_clear`) — ruff considers them "unsafe to fix" (DB query result locals). Worth a human read — likely either a wire-up gap or debug remnants.
4. **`services/mcp-server` is missing `go.sum`** — anomalous in a reproducible workspace. Worth running `go mod tidy` in that module specifically.
5. **`go mod tidy` direct/indirect rebalances** across `auth`, `connector`, `document`, `mcp-server` — out of scope for dead-code work but worth a separate `chore: go mod tidy across modules` commit.
6. **`web/src/components/ui/form.tsx`** is in Bucket B (only importer of `react-hook-form`); if you decide it's dead, the `react-hook-form` + `@hookform/resolvers` deps drop with it as a chain.
7. **`services/signature-signer/README.md`** references a `KeystoreAdapter.java` that doesn't exist on disk. Doc fix, not dead code.

---

## Branch handoff

```
git log --oneline chore/dead-code-cleanup ^chore/eslint-flat-config
d1abc63 chore(web): drop unused react-intl + @radix-ui/react-switch deps
ca06367 chore(web): remove 6 superseded layout/ui orphan components
bb0f8c1 chore(collaboration): drop unused 'uuid' dependency
65aab20 chore(preview): drop unused typing.Optional import
b0cf03f chore: ignore Python __pycache__/*.pyc
2678b2d chore(intelligence): drop unused imports (ruff F401/F811)
ce386ab chore(cleanup): add Phase 1 dead-code inventory
```

Not pushed. Each commit is independently revertible. The branch can be PR'd as-is or rebased onto whatever lands first on `chore/eslint-flat-config`.
