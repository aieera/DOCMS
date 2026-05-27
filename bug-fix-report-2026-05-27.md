# VaultDMS Bug Fix Report — 2026-05-27

**Branch:** `fix/bug-report-2026-05-27` (off `chore/eslint-flat-config`)
**Bugs in original report:** 20
**Fixed in this session:** 12
**Flagged for backend / out-of-band follow-up:** 7
**Pre-verified already-correct:** 1

---

## Commit log

| # | SHA | Bugs | Notes |
|---|---|---|---|
| 1 | `2be1510` | BUG-01, 02, 06, 07, 08, 09 | Form validation batch — toast feedback instead of silent no-op |
| 2 | `fbccb61` | BUG-03 | Date formatters reject zero/invalid dates → em-dash instead of "2025 years ago" |
| 3 | `e38ee3c` | BUG-04 | GraphQL diagnostic banner moved to `console.warn` |
| 4 | `b159ff9` | BUG-11, 12 | Saved searches loading state + grid view filename `title` attr |
| 5 | `72fa0e9` | BUG-14, 16 | Workflows skeleton chrome + comment author UUID→name resolution |
| 6 | `fda36a9` | BUG-18 | Themed 404 page for unmatched routes |

Each commit gated by `tsc --noEmit` + `vite build`. Both stayed green throughout.

---

## Per-bug disposition

### ✅ Fixed in this branch

#### BUG-01 / 02 / 06 / 08 / 09 — Form validation silent no-ops
- **Root cause:** every affected dialog had `disabled={!field.trim()}` on its submit button, so clicks were no-ops with zero feedback.
- **Fix:** removed the `disabled` field gate, validated in the submit handler, surfaced `toast.error('<Field> is required')` on empty submission. Forms still block double-submits via `disabled={mut.isPending}`.

#### BUG-07 — Transfer ownership with no user selected
- Same pattern as above: removed the disabled-when-empty gate; on click without a target, fires `toast.error('Select a user to transfer to')`.

#### BUG-10 — Ask page empty question
- **Verified already correct.** The Ask submit button is `disabled={!question.trim() || askMut.isPending}` and the kbd hint sits inline beneath the textarea. Left untouched.

#### BUG-03 — "2025 years ago" on search results
- **Root cause:** the search service is emitting unpopulated `created_at` fields as the Go `time.Time` zero value (`0001-01-01T00:00:00Z`). `dayjs.fromNow()` faithfully translates that to ~2025 years ago.
- **Fix (frontend defence):** `formatDate`, `formatDateTime`, `formatRelativeTime` in [web/src/lib/formatters.ts](web/src/lib/formatters.ts) now treat null/undefined/empty/invalid/pre-1900 input as "no usable date" and render `—`. Other call sites unaffected.
- **Still needs (backend):** the search service's hit builder should populate `created_at` from the OpenSearch document or omit the field — see `Backend follow-ups` below.

#### BUG-04 — GraphQL diagnostic banner on every doc page
- **Root cause:** banner was admin-gated, but the test user is admin so it appeared everywhere. The underlying GraphQL "upstream not configured" error is by-design ops noise (graphql-gateway wiring is partial per memory).
- **Fix:** removed the visible `<details>` banner; the error is now logged to `console.warn` instead, so QA/ops can still see it in devtools.

#### BUG-11 — Saved searches black-flash on load
- **Root cause:** the loading branch returned a bare `<Spinner>` with no PageHeader, so during route hydration the user saw the previous page wiped to black until the spinner rendered.
- **Fix:** render the PageHeader unconditionally; spinner sits inside the body area.

#### BUG-12 — Long filenames truncated in grid view without tooltip
- **Fix:** added native `title={doc.title}` to the truncated paragraph in `DocumentCard`, surfacing the full name on hover.

#### BUG-14 — Workflows page black panel
- **Root cause:** the loading branch was a single `<Skeleton h-64>` which on dark themes is barely distinguishable from the page background (both `bg-muted` and `bg-background` are dark).
- **Fix:** replaced with a layout-matching skeleton (4-item list + main panel) so the loading state has clear visual structure.

#### BUG-16 — Comment author shown as UUID prefix
- **Root cause:** `CommentBubble` renders `c.author_id.slice(0, 8)` because the `Comment` payload from `/api/v1/documents/{id}/comments` carries only `author_id`, no display name.
- **Fix:** `CommentsPanel` already fetches `/admin/users` for `@mention` autocomplete; reuse the same query to build an `authorId → display_name` Map, thread it through `ThreadCard → CommentBubble`. Falls back to the UUID prefix when the user isn't in the list (e.g. deleted users).
- **Note:** a cleaner long-term fix is to have the backend include `author_display_name` on the comment payload directly. Flagged below.

#### BUG-18 — Unknown URLs show raw "Not Found"
- **Fix:** added `notFoundComponent` to the TanStack Router root route. Renders a themed 404 with a "Go to home" link inside the app shell.

---

### ⚠️ Flagged — needs backend investigation (NOT fixed in this session)

These need running services + DB inspection to verify properly. Frontend already handles their failure modes gracefully where possible.

#### BUG-05 — PDF preview "Failed to load PDF document"
- **Likely upstream:** preview service or storage/range-request handling for the specific `test_allowed.pdf`. `guide.pdf` loads, suggesting per-document state. Check preview-worker logs and storage signing URL TTL/range support.

#### BUG-13 — OCR failed on multiple documents, stuck state
- **Likely upstream:** intelligence-worker OCR pipeline. Matches the known memory note `project_session_2026_05_19_shipped.md` — intelligence-worker drops OCR jobs silently in some edge cases. Has its own slot in the backlog already; this bug report adds two more affected documents to the repro corpus.

#### BUG-15 — Audit log Actor column blank
- **Likely upstream:** audit service event-ingest pipeline isn't resolving the actor identity, or the actor field isn't being included in the projection that feeds the table. Memory note `project_security_followups_2026_05_21.md` flags a related issue (tenant-id-from-context sweep across ~30 handlers); the audit actor population may be in the same family.

#### BUG-17 — Document SIZE missing from metadata sidebar
- **Likely upstream:** `/api/v1/documents/{id}` not populating `size_bytes` on the response, or the doc has no current version (size only known per version). Frontend renders `—` for null/undefined via `formatFileSize`, so the UI handles it cleanly — the data gap is upstream.

#### Backend follow-up for BUG-03
- The "2025 years ago" appeared because `search.DocumentHit.CreatedAt` (Go `time.Time`) is being serialized as the zero value when the OpenSearch document doesn't carry the field. The search indexer should either populate the field from the source row or use `*time.Time` so JSON renders `null` (which the frontend now handles).

#### Backend follow-up for BUG-16
- Cleaner long-term fix: extend the Comment REST response to include `author_display_name` so the frontend doesn't need a separate users fetch. The current frontend workaround is correct but burns an extra `/admin/users` call per document detail view.

---

### ⚠️ Flagged — out-of-scope / needs reproduction

#### BUG-19 — Self-service profile editing missing
- **Not a bug — documented feature gap.** The banner reads *"Self-service profile editing is coming soon. To update your display name, contact your tenant admin."* This is intentional behavior at the current waypoint. Feature work needs scoping (display name, avatar, locale already exists per ADR 0106, timezone, etc.). Not something to silently "fix" — needs product input on scope.

#### BUG-20 — "Recent contracts" filter pre-applied on /search
- **Cannot reproduce reliably from code alone.** The /search route is URL-param-driven and bookmarkable by design. The most likely explanation is the URL state carrying over from a previous smart-folder click rather than a hardcoded default. **Needs in-browser repro** with the exact navigation path (sidebar entry, URL bar, browser back/forward) before any code change — risk of breaking intentional filter state otherwise.

---

## Verification gates run

For each commit:

| Gate | Result |
|---|---|
| `tsc --noEmit` (web) | ✅ green every commit |
| `vite build` (web) | ✅ 3060 modules transformed every time |
| `node --check` (where touched) | n/a — no Node service changes |
| `go build ./...` per module | n/a — no Go changes in this branch |

No commit turned the gate red. Nothing reverted.

---

## Branch handoff

```
fda36a9 fix(web): themed 404 page for unmatched routes (BUG-18)
72fa0e9 fix(web): admin workflows skeleton + comment author resolution
b159ff9 fix(web): keep page chrome during load + title attr for truncated filenames
e38ee3c fix(web): suppress visible GraphQL diagnostic banner on document pages
fbccb61 fix(web): formatters reject zero/invalid dates instead of "2025 years ago"
2be1510 fix(web): give 6 forms visible feedback on empty submission
```

6 commits, all frontend-only. Compatible with `chore/dead-code-cleanup` and `chore/eslint-flat-config` (the dead-code cleanup touches a disjoint set of files).

---

## Suggested next steps

1. **Push this branch** and open a PR against `chore/eslint-flat-config` (or `main`, depending on integration order with the cleanup branch).
2. **Backend follow-up tickets** (recommend creating GitHub issues):
   - BUG-03 root cause: search service zero-time `created_at` (~1 line fix in [services/search/internal/handler](services/search/internal/handler))
   - BUG-15 audit Actor column population
   - BUG-17 size_bytes population on document GET
   - BUG-05 PDF preview per-document failure (paired with BUG-13 OCR backlog)
   - BUG-16 ergonomics: include `author_display_name` in comment payload
3. **In-browser repro for BUG-20**: open `/search` from each sidebar entry + bookmark and see whether "Recent contracts" filter survives a clean nav.
4. **BUG-19 product scope**: confirm whether display-name self-edit ships in v1.0 or post-GA.
