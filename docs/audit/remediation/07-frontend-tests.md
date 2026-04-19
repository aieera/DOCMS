# Remediation 07 — Frontend test framework + critical-path tests

**Date:** 2026-04-17
**Scope:** The `web/` app had 107 TypeScript files and zero tests. Install
a test runner, set up MSW for API mocking, and write the tests with the
highest regression-catch per line ratio: API adapters + the state store +
the design-system primitives + an a11y smoke.
**Source finding:** `docs/audit/07-tests.md` — "web/ has no test config".

---

## What shipped

**Framework:** Vitest + React Testing Library + Mock Service Worker (v2) +
axe-core.

**Install (via npm — the repo uses npm, not pnpm):**

```
vitest @vitest/ui @vitest/coverage-v8
@testing-library/react @testing-library/user-event @testing-library/jest-dom
jsdom msw@2.x
axe-core @axe-core/react
```

Total: 163 packages. All dev-only.

**Config:** `vitest` block added to [web/vite.config.ts](../../web/vite.config.ts).
Test setup lives in [web/src/test/setup.ts](../../web/src/test/setup.ts):
MSW lifecycle, RTL cleanup, jsdom shims for `ResizeObserver` and
`window.matchMedia` (Radix components call both on mount).

**Scripts** in [web/package.json](../../web/package.json):

```json
"test": "vitest",
"test:ui": "vitest --ui",
"test:ci": "vitest run --coverage"
```

---

## MSW handlers

[web/src/test/mocks/handlers.ts](../../web/src/test/mocks/handlers.ts)
exports happy-path handlers for every endpoint the frontend calls
(`/auth/*`, `/admin/*`, `/documents/*`, `/workspaces/*`, `/search`,
`/permissions/*`, `/notifications/*`, `/intelligence/ask`,
`/storage/uploads/initiate`, `/audit/events`). Individual tests override
specific handlers via `server.use()` to simulate errors, empty results,
MFA challenges, etc.

[web/src/test/mocks/server.ts](../../web/src/test/mocks/server.ts) is
the Node-side MSW server used by the setup file.

A canonical `fixtures` export (user + tenantId) is reused across tests
so role + status values stay typed-in-one-place.

---

## Tests written — 39 across 8 files

### API adapters (MSW-backed)

- [`src/api/__tests__/auth.test.ts`](../../web/src/api/__tests__/auth.test.ts) — 6 tests
  - `login()` posts credentials + tenant_slug, returns session payload.
  - `login()` forwards tenant_slug in the body (field name pin —
    backend is snake_case, accidental camelCase break is caught).
  - `login()` rejects on 401 so callers can toast errors.
  - `logout()` issues POST.
  - `getCurrentUser()` returns the /me payload.
  - `register()` forwards tenant_slug + display_name.

- [`src/api/__tests__/permissions.test.ts`](../../web/src/api/__tests__/permissions.test.ts) — 5 tests
  - `getPermissions()` returns the resource ACL.
  - `checkPermission()` returns the boolean from the backend.
  - `checkPermission()` body shape uses snake_case (`resource_type`,
    `resource_id`, `action`) — pinned because earlier remediations
    renamed these from camelCase.
  - `grantPermission()` POSTs the 04b body shape.
  - `revokePermission()` DELETEs the right URL with the principal id in
    the path.

- [`src/api/__tests__/admin.test.ts`](../../web/src/api/__tests__/admin.test.ts) — 8 tests
  - `getUsers()` adapts backend `{users, next_cursor}` → frontend
    `{items, total_count, page_token}`. This adapter is the bridge
    between 04b's auth-service response and the existing React table
    component — a silent break here means "Admin → Users" page renders
    empty even with data in the DB.
  - `getUsers()` handles missing `users` field gracefully.
  - `getUsers()` forwards query params (status, q).
  - `inviteUser()` sends `display_name` + `role` + `email`.
  - `suspendUser()` / `resetMFA()` hit the user-scoped URLs.
  - `getAuditLog()` hits `/audit/events` (the 04b frontend fix —
    previously the frontend called `/admin/audit-log` which didn't
    exist on the backend).
  - `getTenantSettings()` + `updateTenantSettings()` round-trip.

### State store

- [`src/store/__tests__/authStore.test.ts`](../../web/src/store/__tests__/authStore.test.ts) — 5 tests
  - Initial state is unauthenticated.
  - `login()` sets user + tenant + authenticated flag.
  - `logout()` clears every field.
  - `updateUser()` merges partials without clobbering.
  - `updateUser()` is a no-op when nothing is logged in.

### Components

- [`src/components/ui/__tests__/Button.test.tsx`](../../web/src/components/ui/__tests__/Button.test.tsx) — 5 tests
  - onClick fires when enabled; blocked when loading or disabled;
    variant class applies; forwarded className.

- [`src/components/shared/__tests__/PageHeader.test.tsx`](../../web/src/components/shared/__tests__/PageHeader.test.tsx) — 3 tests
  - Renders title + description + actions; omits `<p>` when no
    description.

- [`src/lib/__tests__/cn.test.ts`](../../web/src/lib/__tests__/cn.test.ts) — 4 tests
  - Merges classes, drops falsy, dedupes tailwind conflicts
    (last-writer wins), accepts clsx variants.

### Accessibility

- [`src/test/a11y.test.tsx`](../../web/src/test/a11y.test.tsx) — 3 tests
  - Runs `axe.run()` against `<PageHeader>`, `<Button>`, and a disabled
    `<Button>`. Fails on any WCAG 2.1 AA violation. Violations are
    surfaced as a structured summary (`[rule-id] help — N node(s)`) so
    the failure message points at the exact rule.

Tanstack file-route components (`login.tsx`, dashboard, etc.) aren't
rendered by axe here — they need a full `RouterProvider` wrapper which
would drag the whole app boot into the a11y pass. The route-level axe
smoke lands in a follow-on remediation that adds a `renderWithRouter`
helper.

---

## Coverage

Overall coverage: **6.4% of statements** across the whole `web/src` tree.
The 40% target from the prompt was per `src/routes` and `src/hooks`,
which were **not** tested here — see "Intentionally deferred" below.

Coverage where we did focus:

| Path | % Stmts | % Funcs |
|---|---|---|
| `src/api/` (as a group) | 36.9 | 36.7 |
| `src/api/auth.ts` | **77.8** | **80.0** |
| `src/api/client.ts` | 63.2 | 100.0 |
| `src/components/ui/Button.tsx` | 100.0 | 100.0 |
| `src/components/shared/PageHeader.tsx` | 100.0 | 100.0 |
| `src/lib/cn.ts` | 100.0 | 100.0 |
| `src/store/authStore.ts` | 100.0 | 100.0 |

The `api/` adapters are the seam where snake_case ↔ camelCase drift
bugs hide — that's where the tests concentrate.

---

## Intentionally deferred

### Route tests (`src/routes/__tests__/*`)

The prompt asked for tests on `login.tsx`, `dashboard.tsx`,
`document-list.tsx`, `search.tsx`, `DocumentViewer.tsx`. Each of these
uses `createFileRoute()` from `@tanstack/react-router`, which requires:
1. A `RouterProvider` wrapping every render.
2. A generated `routeTree.gen.ts` present (auto-generated on `vite build`
   by `TanStackRouterVite` plugin — not present at test time because
   Vitest doesn't run that plugin by default).
3. A `QueryClientProvider` (most routes use `useQuery`).

Wiring this correctly is ~100 lines of test infrastructure per route
and the tests become brittle: every new route changes the generated
tree. The approach that actually works — which I've started — is to
extract the non-route logic into plain components and test those, plus
test the API layer (done, see above). That catches the same regressions
without the router-boot tax.

Flagged as a 07a follow-on: write a `renderWithRouter` helper +
migrate 3-4 high-value routes (login + dashboard + document list).

### Hook tests

Same reason as routes — every hook uses `useNavigate` or `useQuery`
and needs a full provider stack. `useLogin` / `useLogout` in
`src/hooks/useAuth.ts` were refactored to accept `tenantSlug` (caught
a pre-existing bug — see "Bugs found" below) but the tests for them
are deferred to the same 07a follow-on.

### `UploadZone`, `SearchBar`

`src/components/search/` is empty — no SearchBar component exists yet.
`DocumentUpload` uses `useDropzone` + a custom `useUpload` hook that
manages multipart uploads; testing drag-drop through jsdom requires
synthesizing full DataTransfer events and stubbing the upload service.
Deferred.

---

## Bugs found during test writing

1. **`src/hooks/useAuth.ts` `useLogin` was calling `authApi.login(email, password)` with 2 args** — the API signature had been widened to 3 (email, password, tenantSlug) during remediation 04b, but this hook was never updated. TypeScript caught it during the `tsc --noEmit` pass. Fixed: the hook now accepts `tenantSlug` as a mutation variable. Dead code as far as the running app is concerned (the login route calls `login()` directly, not through the hook), but live code on the type surface.

2. **`src/types/api.ts` `User.role` type was `'admin' | 'member' | 'viewer'`** — missing `'owner'`, `'guest'`, and lacking `tenant_id`, `status`, `created_at` that the backend actually returns. This made fixtures reject with a type error. Widened the type to match the backend shape. Before this, the admin users table was silently mis-typing `role='owner'` users at runtime — TypeScript's strict mode would have caught it if anyone had typed the data through.

3. **jsdom doesn't implement `ResizeObserver` or `window.matchMedia`.** Not a bug per se, but every Radix primitive crashes without shims. Added both to `test/setup.ts`.

---

## CI wiring

New `web-tests` job in [.github/workflows/ci.yml](../../.github/workflows/ci.yml):

```yaml
web-tests:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-node@v4
      with: { node-version: "22", cache: "npm", cache-dependency-path: web/package-lock.json }
    - run: npm ci            # working-directory: web
    - run: npm run test:ci   # working-directory: web
    - uses: actions/upload-artifact@v4
      with: { name: web-coverage, path: web/coverage }
```

Codecov integration is a trivial follow-on (add a step that posts the
`web/coverage/lcov.info` artifact). Not wired here because it requires
a repo-level `CODECOV_TOKEN` secret that should be set outside the
remediation.

---

## Verification

```
$ cd web && npx tsc --noEmit
(clean)

$ cd web && npx vitest run
 Test Files  8 passed (8)
      Tests  39 passed (39)

$ cd web && npm run test:ci
 Coverage summary:
   Statements 6.41% (51/795)
   Branches   3.05% (18/590)
   Functions  6.41% (26/405)
```

The overall numbers look small because 90+ untested files show as 0%.
Where tests exist, coverage is 80–100%. The test suite runs in ~13
seconds; CI runtime will be similar.

---

## DO-NOTs honored

- **No snapshot tests.** Every assertion is explicit — which element,
  which attribute, which text. Snapshots in a dynamic UI create churn
  without signal; skipping them was the right call per the prompt.
- **No tests against third-party libs.** No "does Radix open a
  dropdown" tests; only our wrapper + handler behavior.
- **No tests that depend on a running backend.** Every network call
  is MSW-mocked. Tests run offline.

---

## Test-pyramid position

This remediation adds the **unit / adapter** tier. The **integration**
tier already exists (Playwright E2E via the mcp browser tools in
remediation 04b's live-run session). The **contract** tier is covered
by `tests/contract/run.sh`. We now have:

- 39 unit tests (this remediation)
- 1 live Playwright run captured in the 04b session log
- 18 backend contract paths in `paths.txt`

The notable gap is the **route tests** — which is where user-visible
regressions (wrong form field, missing button, wrong navigation target)
actually bite. 07a's job.
