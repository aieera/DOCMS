# UX Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the P1/P2 UX defects found by the seven-lens audit — dead ends, lying empty states, broken interaction conventions, mobile gaps, and form friction — without touching the backend.

**Architecture:** Client-side only. Shared plumbing first (a missing shadow tier, mounting an already-built upload tray), then one mechanical pattern applied across call sites (`isError` → `ErrorState` + retry), then per-theme batches (dead ends, mobile/deep-links, browse interaction, forms, hierarchy). Each task ends green on tsc + lint + vitest.

**Tech Stack:** React 18, TypeScript, Vite 5, Tailwind 3.4 (+ tailwindcss-rtl), TanStack Router + Query v5, Zustand, Radix/shadcn, lucide-react, sonner (toasts), react-i18next, vitest + Testing Library, Playwright.

**Spec:** `docs/superpowers/specs/2026-09-17-ux-improvements-design.md`

## Global Constraints

- Branch `neu-ui` in `/home/sedoc/DOCMS`. `git status --short` before each commit; commit only the files the task names. Trailer on every commit body: `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.
- **Scope (spec §0, ruled):** client-side interaction wiring IS allowed — `onClick`/`onKeyDown` handlers, `navigate()`, mounting existing components, destructuring `isError`/`refetch`, toasts, ARIA/labels, URL search-param state. **Forbidden:** any file outside `web/`; new endpoints, queries, mutations, or query keys; changed business rules; new dependencies.
- Render new branches only from data the component **already fetches**.
- Neumorphic language is settled — use the existing `shadow-neu / -sm / -inset / -pressed` utilities (plus `shadow-neu-lg` added in Task 1). Don't hand-roll `shadow-[...]`.
- Logical CSS only for horizontal position/spacing (`ms-/me-/ps-/pe-/start-/end-`) — `npm run lint` chains `lint:rtl` and fails on physical utilities.
- All new user-visible strings via `t('key', 'English default')` with keys added to BOTH `web/public/locales/{en,ar}/common.json`.
- Every interactive control keeps a visible `:focus-visible` accent ring and a ≥44px touch target.
- Local gates: `npx tsc --noEmit`, `npm run lint`, `npm test -- --run`, `npm run build`. Do NOT start a dev server — the systemd `sedoc-web` unit serves :3000 with HMR; verify visually at `http://192.168.70.22:3000`.

---

## File structure

| Path | Responsibility |
|---|---|
| `web/src/styles/globals.css` | Modify: add `--nm-shadow-lg` to `:root` and `.dark` (hover-raise tier). |
| `web/tailwind.config.js` | Modify: register `boxShadow['neu-lg']`. |
| `web/src/routes/_authenticated.tsx` | Modify: mount `<UploadProgress/>`. |
| `web/src/components/documents/UploadProgress.tsx` | Modify: show failure reason + per-row Retry. |
| `web/src/routes/_authenticated/{tasks,search,notifications,workspaces/index}.tsx`, admin lists | Modify: honest error branches. |
| `web/src/components/notifications/NotificationsPanel.tsx`, `web/src/components/layout/app-topbar.tsx`, `web/src/routes/_authenticated/notifications.tsx` | Modify: notification rows activate. |
| `web/src/components/documents/DashboardUploadDialog.tsx` | Modify: success → toast with action. |
| `web/src/components/layout/breadcrumbs.tsx` | Modify: structural segments are not links. |
| `web/src/routes/_authenticated.tsx`, `web/src/routes/login.tsx` | Modify: carry `redirect` through the auth gate. |
| `web/src/components/layout/app-layout.tsx`, `app-topbar.tsx` | Modify: drawer closes on route change; mobile search entry. |
| `web/src/components/folders/BrowserTiles.tsx` | Modify: single-click opens, Enter/Space, press state. |
| `web/src/components/ui/shadcn/{badge,input}.tsx`, dialogs | Modify: hierarchy + required-field signalling. |
| `web/src/**/__tests__/ux-*.test.tsx` | Create: focused tests per task. |

---

### Task 1: Foundation — hover-raise tier, mount the upload tray, surface upload failures

**Files:**
- Modify: `web/src/styles/globals.css` (`:root` + `.dark`), `web/tailwind.config.js`
- Modify: `web/src/routes/_authenticated.tsx`, `web/src/components/documents/UploadProgress.tsx`
- Test: `web/src/components/documents/__tests__/ux-upload-progress.test.tsx` (create)

**Interfaces:**
- Consumes: the existing `useUploadStore` (already feeds `UploadProgress`), `--nm-dist`/`--nm-blur`/`--nm-light`/`--nm-dark`.
- Produces: Tailwind utility `shadow-neu-lg` (used by Tasks 5 and 7); `<UploadProgress/>` mounted globally.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/documents/__tests__/ux-upload-progress.test.tsx`. Read `UploadProgress.tsx` and its store first to match the real item shape; the store is `useUploadStore` in `web/src/store/uploadStore.ts` (read it — use its real `addUpload`/`setStatus` API in the test rather than inventing one):

```tsx
import { describe, it, expect, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { UploadProgress } from '../UploadProgress'
import { useUploadStore } from '@/store/uploadStore'

describe('UploadProgress — failure visibility', () => {
  beforeEach(() => { useUploadStore.setState(useUploadStore.getInitialState?.() ?? {} as never, true) })

  it('shows the stored failure reason and a retry control for a failed upload', () => {
    // Seed one failed item directly into the store, matching its real shape.
    const s = useUploadStore.getState() as Record<string, unknown>
    // (Use the store's documented add/setStatus API — see uploadStore.ts.)
    ;(s.addUpload as (f: { id: string; name: string }) => void)?.({ id: 'u1', name: 'contract.pdf' })
    ;(s.setStatus as (id: string, st: string, detail?: string) => void)?.('u1', 'failed', 'Virus scan rejected the file')
    render(<UploadProgress />)
    expect(screen.getByText(/Virus scan rejected the file/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })
})
```

If the store's real API differs, adapt the seeding lines to it — the two assertions (reason visible, retry present) are the contract and must not change.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npm test -- --run src/components/documents/__tests__/ux-upload-progress.test.tsx`
Expected: FAIL — the failure reason is stored but never rendered, and there is no retry button.

- [ ] **Step 3: Add the hover-raise shadow tier**

In `web/src/styles/globals.css`, add to BOTH the `:root` and `.dark` blocks, immediately after the existing `--nm-shadow-pressed` line:

```css
    --nm-shadow-lg: calc(var(--nm-dist) * 1.5) calc(var(--nm-dist) * 1.5) calc(var(--nm-blur) * 1.4) hsl(var(--nm-dark)), calc(var(--nm-dist) * -1.5) calc(var(--nm-dist) * -1.5) calc(var(--nm-blur) * 1.4) hsl(var(--nm-light));
```

In `web/tailwind.config.js`, add to `theme.extend.boxShadow` (next to the existing neu entries):

```js
        'neu-lg': 'var(--nm-shadow-lg)',
```

- [ ] **Step 4: Mount the upload tray**

In `web/src/routes/_authenticated.tsx`, import and mount it beside the existing duplicate dialog — the store already drives it, so this is purely a mount:

```tsx
import { UploadProgress } from '@/components/documents/UploadProgress'
```

and inside `AuthenticatedLayout`'s `<AppLayout>`, directly after `<DuplicateUploadDialog />`:

```tsx
      <UploadProgress />
```

- [ ] **Step 5: Surface the failure reason + retry in UploadProgress**

Read `web/src/components/documents/UploadProgress.tsx`. In the row renderer, where a failed item currently shows only an icon, render the stored reason and a retry control. Keep the existing markup/props; add:

```tsx
{item.status === 'failed' && item.error && (
  <p className="truncate text-xs text-destructive" title={item.error}>{item.error}</p>
)}
```

and, in that row's action slot, a retry button that re-runs the same upload the store already knows about (use the retry/`retryUpload` action if the store exposes one; if it does not, call the same `useUpload` entry point the row was created from — do NOT add a new store action or query):

```tsx
{item.status === 'failed' && (
  <Button variant="ghost" size="sm" onClick={() => retry(item)} aria-label={`Retry ${item.name}`}>
    {t('upload.retry', 'Retry')}
  </Button>
)}
```

Add `upload.retry` to both locale files. If no retry entry point exists client-side without a new mutation, render the reason only and report that in your report — the reason is the required half.

- [ ] **Step 6: Verify**

Run: `cd web && npm test -- --run src/components/documents/__tests__/ux-upload-progress.test.tsx && npx tsc --noEmit && npm run lint && npm test -- --run`
Expected: the new test passes; full suite green; tsc/lint clean.
Then confirm the tier is real: `grep -n 'nm-shadow-lg' web/src/styles/globals.css web/tailwind.config.js` — expect hits in `:root`, `.dark`, and the config.

- [ ] **Step 7: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/styles/globals.css web/tailwind.config.js web/src/routes/_authenticated.tsx web/src/components/documents/UploadProgress.tsx web/src/components/documents/__tests__/ux-upload-progress.test.tsx web/public/locales/en/common.json web/public/locales/ar/common.json
git commit -m "feat(web): hover-raise tier, mount upload tray, surface upload failures"
```

---

### Task 2: Honest error states — stop showing "all caught up" when the query failed

**Files:**
- Modify: `web/src/routes/_authenticated/tasks.tsx` (both `MyTasksSection` and `ApprovalsSection`), `search.tsx`, `notifications.tsx`, `workspaces/index.tsx`, and the admin list pages that render an empty state on failure (`admin/users.tsx` / `admin/identity` users table — grep below finds them)
- Test: `web/src/routes/__tests__/ux-error-states.test.tsx` (create)

**Interfaces:**
- Consumes: `ErrorState` from `@/components/ui/ErrorState` — signature `ErrorState({ message?: string; onRetry?: () => void })`, already neumorphic.
- Produces: the pattern later tasks reuse — `const { data, isLoading, isError, refetch } = useQuery(...)` and an `isError` branch **before** the empty branch.

- [ ] **Step 1: Enumerate the call sites**

Run: `cd web && grep -rn "const { data, isLoading } = useQuery\|const { data, isLoading, " src/routes --include='*.tsx' | head -30`
Every hit that renders an EmptyState/"caught up"/blank on failure is in scope. The audit confirmed these in code: `tasks.tsx:135` (MyTasksSection) and `tasks.tsx:531` (ApprovalsSection), `search.tsx:199`, plus notifications, workspaces and the admin users list.

- [ ] **Step 2: Write the failing test**

Create `web/src/routes/__tests__/ux-error-states.test.tsx`. Test the pattern on the highest-value screen (tasks) by rendering its section with a failing query — mock the API module so the query rejects:

```tsx
import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { renderWithProviders } from '@/test/renderWithProviders'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, ...r }: { children: React.ReactNode } & Record<string, unknown>) => <a {...r}>{children}</a>,
  useNavigate: () => vi.fn(),
  createFileRoute: () => (o: Record<string, unknown>) => o,
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) => select({ location: { pathname: '/tasks' } }),
}))
vi.mock('@/api/tasks', () => ({
  taskKeys: { list: (p: unknown) => ['tasks', 'list', p], mine: () => ['tasks', 'mine'] },
  listTasks: vi.fn().mockRejectedValue(new Error('boom')),
  listMyTasks: vi.fn().mockRejectedValue(new Error('boom')),
  invalidateTasks: vi.fn(),
}))

import { Route } from '../_authenticated/tasks'
const Page = (Route as unknown as { component: () => React.ReactNode }).component

describe('tasks — query failure', () => {
  it('shows an error with retry, never the "caught up" empty state', async () => {
    renderWithProviders(<Page />)
    await waitFor(() => expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument())
    expect(screen.queryByText(/caught up/i)).toBeNull()
  })
})
```

Adapt the mocked module surface to what `tasks.tsx` actually imports (read its imports first); the two assertions are the contract.

- [ ] **Step 3: Run it to verify it fails**

Run: `cd web && npm test -- --run src/routes/__tests__/ux-error-states.test.tsx`
Expected: FAIL — no retry button; "You're all caught up." is rendered on failure.

- [ ] **Step 4: Apply the pattern at every site**

For each call site, destructure the error state and add a branch BEFORE the empty branch. Example for `tasks.tsx` `MyTasksSection` (line ~135):

```tsx
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: taskKeys.list(params),
    queryFn: () => listTasks(params),
  })
```

then, immediately before the `{!isLoading && tasks.length === 0 && (` block:

```tsx
      {isError && (
        <ErrorState
          message={t('errors.tasks_load', "Couldn't load tasks.")}
          onRetry={() => void refetch()}
        />
      )}
```

and gate the empty branch so it cannot render on failure: change `{!isLoading && tasks.length === 0 && (` to `{!isLoading && !isError && tasks.length === 0 && (`.

Apply the identical shape to `ApprovalsSection`, `search.tsx` (its results area — add the branch between the skeleton and the no-results branch), `notifications.tsx`, `workspaces/index.tsx` (which today says "Try again in a moment" with no retry — give it the retry), and the admin users list. Import `ErrorState` where missing. Add the message keys to both locale files.

- [ ] **Step 5: Verify**

Run: `cd web && npm test -- --run src/routes/__tests__/ux-error-states.test.tsx && npx tsc --noEmit && npm run lint && npm test -- --run`
Expected: new test passes; full suite green.
Then prove no site still shows an empty state on failure: `grep -rn "isError" src/routes/_authenticated/tasks.tsx src/routes/_authenticated/search.tsx src/routes/_authenticated/notifications.tsx src/routes/_authenticated/workspaces/index.tsx` — each must have both the destructure and a branch.

- [ ] **Step 6: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/routes web/src/components web/public/locales/en/common.json web/public/locales/ar/common.json
git commit -m "fix(web): honest error states with retry (no more false all-clear)"
```

---

### Task 3: Dead ends — notifications open their artifact, upload lands somewhere, breadcrumbs don't 404

**Files:**
- Modify: `web/src/routes/_authenticated/notifications.tsx`, `web/src/components/notifications/NotificationsPanel.tsx`, `web/src/components/layout/app-topbar.tsx` (bell dropdown rows)
- Modify: `web/src/components/documents/DashboardUploadDialog.tsx`
- Modify: `web/src/components/layout/breadcrumbs.tsx`
- Test: `web/src/components/layout/__tests__/ux-breadcrumbs.test.tsx` (create)

**Interfaces:**
- Consumes: `Notification` type from `@/types/api` — fields `{ id, type, title, body, read, created_at, resource_type?, resource_id? }` (already fetched; do NOT add a query).
- Produces: nothing later tasks depend on.

- [ ] **Step 1: Write the failing breadcrumb test**

Create `web/src/components/layout/__tests__/ux-breadcrumbs.test.tsx`:

```tsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ to, children, ...r }: { to: string; children: React.ReactNode } & Record<string, unknown>) => <a href={to} {...r}>{children}</a>,
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) =>
    select({ location: { pathname: '/workspaces/ws-1/documents/doc-1' } }),
}))

import { Breadcrumbs } from '../breadcrumbs'

describe('Breadcrumbs — structural segments', () => {
  it('does not render a link to the routeless /documents segment', () => {
    render(<Breadcrumbs />)
    const dead = screen.queryAllByRole('link').filter((a) => (a as HTMLAnchorElement).getAttribute('href')?.endsWith('/documents'))
    expect(dead).toHaveLength(0)
  })
})
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npm test -- --run src/components/layout/__tests__/ux-breadcrumbs.test.tsx`
Expected: FAIL — a link whose href ends in `/documents` is rendered (it 404s when clicked).

- [ ] **Step 3: Fix the breadcrumb**

In `web/src/components/layout/breadcrumbs.tsx`, treat path segments that are structural (no route of their own) as plain text. Add a constant and use it when deciding `<Link>` vs `<span>`:

```tsx
// Segments that exist only as URL structure — there is no route at that
// prefix, so linking them ejects the user onto a shell-less 404.
const STRUCTURAL_SEGMENTS = new Set(['documents', 'instances'])
```

and where each crumb is rendered, render a `<span className="…">` (same classes as the link minus hover/underline) instead of a `<Link>` when `STRUCTURAL_SEGMENTS.has(segment)`.

- [ ] **Step 4: Make notification rows open their artifact**

In each of the three surfaces, make the row body activate. Use the data already on the notification — no new query:

```tsx
const navigate = useNavigate()
const open = (n: Notification) => {
  if (n.resource_type === 'document' && n.resource_id) {
    navigate({ to: '/search', search: { q: n.resource_id } as never })
    return
  }
  if (n.resource_type === 'task' && n.resource_id) { navigate({ to: '/tasks' }); return }
  navigate({ to: '/notifications' })
}
```

**Important:** a document detail route needs BOTH the workspace id and the document id. If the notification payload carries only `resource_id`, do NOT invent a lookup query (out of scope) — route to the search page pre-filled with that id as above, which reaches the document in one click. If the payload does carry the workspace, route directly to `/workspaces/$workspaceId/documents/$documentId`. Read the notification payload shape first and pick the branch that the real data supports; state which in your report.

Make the row keyboard-operable: render the row body as a `<button type="button" className="… text-start">` (or add `role="button" tabIndex={0}` + `onKeyDown` for Enter/Space), keeping the existing mark-read/snooze controls as separate siblings so they don't nest inside the activator.

- [ ] **Step 5: Give the dashboard upload a landing**

In `web/src/components/documents/DashboardUploadDialog.tsx`, in `submit()` after a successful `uploadFiles` + invalidation and before/with `close(false)`, replace the silent close with a toast carrying an action to the destination the user already chose:

```tsx
toast.success(t('upload.done', 'Upload complete'), {
  action: {
    label: t('upload.view', 'View'),
    onClick: () => navigate({ to: '/workspaces/$workspaceId', params: { workspaceId }, search: { folder: folderId ?? undefined } as never }),
  },
})
```

Use the workspace/folder ids the dialog already holds from its own picker. Add the two keys to both locale files.

- [ ] **Step 6: Verify**

Run: `cd web && npm test -- --run src/components/layout/__tests__/ux-breadcrumbs.test.tsx && npx tsc --noEmit && npm run lint && npm test -- --run`
Expected: new test passes; full suite green. Then click through on :3000 — a notification row opens something; uploading from the dashboard offers "View".

- [ ] **Step 7: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/routes/_authenticated/notifications.tsx web/src/components/notifications web/src/components/layout/app-topbar.tsx web/src/components/documents/DashboardUploadDialog.tsx web/src/components/layout/breadcrumbs.tsx web/src/components/layout/__tests__/ux-breadcrumbs.test.tsx web/public/locales/en/common.json web/public/locales/ar/common.json
git commit -m "fix(web): notifications open their artifact, upload lands, breadcrumbs stop 404ing"
```

---

### Task 4: Deep links survive login; mobile navigation works

**Files:**
- Modify: `web/src/routes/_authenticated.tsx` (the `beforeLoad` redirect), `web/src/routes/login.tsx` (post-login navigation)
- Modify: `web/src/components/layout/app-layout.tsx` (drawer closes on route change), `web/src/components/layout/app-topbar.tsx` (mobile search entry)
- Test: `web/src/components/layout/__tests__/ux-mobile-nav.test.tsx` (create)

**Interfaces:**
- Consumes: `AppSidebar`/`MobileSidebar` (props `{ open, onOpenChange }`), `useRouterState`.
- Produces: nothing later tasks depend on.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/layout/__tests__/ux-mobile-nav.test.tsx`:

```tsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'

let pathname = '/workspaces'
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) => select({ location: { pathname } }),
}))
vi.mock('../app-sidebar', () => ({
  AppSidebar: () => <aside data-testid="rail" />,
  MobileSidebar: ({ open }: { open: boolean }) => (open ? <div data-testid="drawer" /> : null),
}))
vi.mock('../app-topbar', () => ({ AppTopbar: () => <header /> }))
vi.mock('../breadcrumbs', () => ({ Breadcrumbs: () => null }))
vi.mock('@/store/uiStore', () => ({ useUIStore: (sel: (s: { sidebarCollapsed: boolean }) => unknown) => sel({ sidebarCollapsed: false }) }))

import { AppLayout } from '../app-layout'

describe('AppLayout — mobile drawer', () => {
  it('closes the drawer when the route changes', () => {
    const { rerender } = render(<AppLayout><p>a</p></AppLayout>)
    // Drawer starts closed; simulating a route change must not leave it open.
    pathname = '/tasks'
    rerender(<AppLayout><p>b</p></AppLayout>)
    expect(screen.queryByTestId('drawer')).toBeNull()
  })
})
```

(The meaningful assertion is that an effect keyed on `pathname` drives `setMobileOpen(false)`; if your implementation exposes the drawer differently, keep the assertion "no drawer after a route change".)

- [ ] **Step 2: Run it to verify it fails / passes trivially**

Run: `cd web && npm test -- --run src/components/layout/__tests__/ux-mobile-nav.test.tsx`
Note the result. This test can pass trivially before the fix (the drawer starts closed) — that's expected; the real proof is Step 3's code plus the manual check in Step 6. Do not weaken the assertion to force a red.

- [ ] **Step 3: Close the drawer on navigation**

In `web/src/components/layout/app-layout.tsx`, add the effect (the component already has `pathname` from `useRouterState` for the breadcrumb gate):

```tsx
  // Mobile: navigating from the drawer must dismiss it — otherwise the
  // new page renders underneath and every navigation costs a second tap.
  useEffect(() => { setMobileOpen(false) }, [pathname])
```

Import `useEffect` from `react`.

- [ ] **Step 4: Add a mobile search entry point**

In `web/src/components/layout/app-topbar.tsx`, the search field is `hidden sm:block`. Add a phone-only trigger in the left cluster (next to the menu button), navigating to the search page which has its own input:

```tsx
      <Button
        variant="ghost"
        size="icon"
        asChild
        className="text-muted-foreground hover:bg-muted hover:text-foreground sm:hidden"
      >
        <Link to="/search" aria-label={t('sidebar.search', 'Search')}>
          <Search className="h-5 w-5" />
        </Link>
      </Button>
```

`Search` is already imported in this file; import `Link` from `@tanstack/react-router` if absent.

- [ ] **Step 5: Carry the deep link through login**

In `web/src/routes/_authenticated.tsx` `beforeLoad`, the two `throw redirect({ to: '/login' })` calls discard the destination. Change both to carry it:

```tsx
        throw redirect({ to: '/login', search: { redirect: location.href } as never })
```

In `web/src/routes/login.tsx`, where it currently does `navigate({ to: '/' })` after a successful sign-in, read the param and honour it only if it is an internal path:

```tsx
  const search = Route.useSearch() as { redirect?: string }
  // Only ever follow same-origin, path-only redirects — never an absolute
  // URL, which would turn the login form into an open-redirect gadget.
  const dest = typeof search.redirect === 'string' && search.redirect.startsWith('/') && !search.redirect.startsWith('//')
    ? search.redirect
    : '/'
  navigate({ to: dest })
```

If `login.tsx` has no `validateSearch`, add one that passes `redirect` through as an optional string (a route-level search schema, not a new query).

- [ ] **Step 6: Verify**

Run: `cd web && npm test -- --run src/components/layout && npx tsc --noEmit && npm run lint && npm test -- --run`
Expected: green. Manually on :3000 at a phone width: open the drawer, tap a destination — the drawer closes; a search icon is visible in the topbar. Log out, open a deep document URL, sign in — you land on that URL.

- [ ] **Step 7: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/routes/_authenticated.tsx web/src/routes/login.tsx web/src/components/layout/app-layout.tsx web/src/components/layout/app-topbar.tsx web/src/components/layout/__tests__/ux-mobile-nav.test.tsx
git commit -m "fix(web): deep links survive login; mobile drawer closes; mobile search entry"
```

---

### Task 5: Browse tiles — single click opens, keyboard works, press state flips

**Files:**
- Modify: `web/src/components/folders/BrowserTiles.tsx` (all four tile/row variants)
- Modify: `web/src/routes/_authenticated/workspaces/$workspaceId/index.tsx` (the handlers wired into the tiles)
- Test: `web/src/components/folders/__tests__/ux-browser-tiles.test.tsx` (create)

**Interfaces:**
- Consumes: `shadow-neu-lg` from Task 1.
- Produces: nothing later tasks depend on.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/folders/__tests__/ux-browser-tiles.test.tsx`. Read `BrowserTiles.tsx` for the real exported component names and props first:

```tsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { FolderTile } from '../BrowserTiles'

describe('BrowserTiles — open on single click', () => {
  it('opens on a single click and on Enter', () => {
    const onOpen = vi.fn(); const onSelect = vi.fn()
    render(<FolderTile folder={{ id: 'f1', name: 'Finance', document_count: 0 } as never} onOpen={onOpen} onSelect={onSelect} selected={false} />)
    const tile = screen.getByRole('button', { name: /Finance/i })
    fireEvent.click(tile)
    expect(onOpen).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(tile, { key: 'Enter' })
    expect(onOpen).toHaveBeenCalledTimes(2)
  })
})
```

Adapt the props to the real signature; the contract is "single click opens, Enter opens".

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npm test -- --run src/components/folders/__tests__/ux-browser-tiles.test.tsx`
Expected: FAIL — `onClick` currently calls `onSelect`; opening needs a double click.

- [ ] **Step 3: Swap the interaction**

In `BrowserTiles.tsx`, for all four variants (`FolderTile`, `FileTile`, `FolderRow`, `FileRow`): make `onClick` call `onOpen`, delete the `onDoubleClick={onOpen}` wiring, and move selection onto the modifier/checkbox path:

```tsx
  onClick={(e) => {
    // Ctrl/Cmd/Shift-click selects (range/multi); a plain click opens,
    // matching every other surface in the app.
    if (e.metaKey || e.ctrlKey || e.shiftKey) { onSelect?.(); return }
    onOpen?.()
  }}
  onKeyDown={(e) => {
    if (e.key === 'Enter') { e.preventDefault(); onOpen?.() }
    if (e.key === ' ') { e.preventDefault(); onSelect?.() }
  }}
```

The file/folder hover checkbox already calls `onSelect` — keep it and make sure its own `onClick` calls `e.stopPropagation()` so ticking it doesn't also open the item.

- [ ] **Step 4: Add the press state and hover tier**

In `tileBase`, append the press flip (the file already has `transition-all duration-150`, and Task 1 added the bigger tier):

```
'active:translate-y-0 active:shadow-neu-pressed'
```

and change the hover raise from `hover:shadow-neu` (a no-op, since the tile idles at `shadow-neu`) to `hover:shadow-neu-lg`.

- [ ] **Step 5: Verify**

Run: `cd web && npm test -- --run src/components/folders/__tests__/ux-browser-tiles.test.tsx && npx tsc --noEmit && npm run lint && npm test -- --run`
Expected: green — and check the workspace page's own tests still pass (they may assert the old double-click; if one does, update it to the new single-click contract rather than deleting it).
Manually on :3000: single click opens a folder; Ctrl-click selects; the tile visibly presses in.

- [ ] **Step 6: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/components/folders/BrowserTiles.tsx web/src/routes/_authenticated/workspaces/\$workspaceId/index.tsx web/src/components/folders/__tests__/ux-browser-tiles.test.tsx
git commit -m "fix(web): browse tiles open on single click, keyboard-operable, press state"
```

---

### Task 6: Forms — visible required-field signalling and associated labels

**Files:**
- Modify: `web/src/components/folders/NewFolderDialog.tsx` and the other dialogs whose CTA is disabled-gated (add-passkey, share, SMTP admin)
- Modify: the `<label>` call sites missing `htmlFor` (grep in Step 1)
- Test: `web/src/components/folders/__tests__/ux-new-folder-dialog.test.tsx` (create)

**Interfaces:**
- Consumes: `Input` from `@/components/ui/shadcn/input` — it already supports `label`, `error`, and `icon` props and wires `aria-invalid` + `aria-describedby`.
- Produces: nothing later tasks depend on.

- [ ] **Step 1: Write the failing test + enumerate label sites**

Run: `cd web && grep -rn "<label" src --include='*.tsx' | grep -v htmlFor | wc -l` (the audit counted ~130).

Create `web/src/components/folders/__tests__/ux-new-folder-dialog.test.tsx`:

```tsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { NewFolderDialog } from '../NewFolderDialog'

describe('NewFolderDialog — required field', () => {
  it('keeps the CTA enabled and explains why an empty submit fails', () => {
    render(<NewFolderDialog open onOpenChange={() => {}} workspaceId="ws-1" parentId={null} onCreated={vi.fn()} />)
    const cta = screen.getByRole('button', { name: /create/i })
    expect(cta).toBeEnabled()
    fireEvent.click(cta)
    expect(screen.getByText(/name is required/i)).toBeInTheDocument()
  })
})
```

Adapt props to the dialog's real signature.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npm test -- --run src/components/folders/__tests__/ux-new-folder-dialog.test.tsx`
Expected: FAIL — the CTA is disabled while the field is empty, so no message ever appears.

- [ ] **Step 3: Replace disabled-gating with click validation**

In each affected dialog: remove `disabled={!name.trim()}` from the primary button, hold an error in state, and validate on submit:

```tsx
  const [nameError, setNameError] = useState<string | null>(null)
  const submit = () => {
    if (!name.trim()) { setNameError(t('validation.folder_name_required', 'Name is required')); return }
    setNameError(null)
    // …existing create call, unchanged…
  }
```

and pass it to the field so the message renders inline and is announced (`Input` already links it via `aria-describedby`):

```tsx
  <Input label={t('folders.name', 'Folder name')} value={name} onChange={(e) => { setName(e.target.value); if (nameError) setNameError(null) }} error={nameError ?? undefined} />
```

Add the keys to both locale files. Do not change what the create call does.

- [ ] **Step 4: Associate the labels**

For the `<label>` sites from Step 1, give each an `htmlFor` matching its control's `id` (add an `id`, or switch the call site to `Input`'s built-in `label` prop, which generates and links the id for you — prefer that where the control is already an `Input`). Work through the highest-traffic files first (dialogs, settings, admin forms); this improves the a11y gate. If the list is very long, cover the dialogs + settings + admin forms in this task and note the remainder for a follow-up.

- [ ] **Step 5: Verify**

Run: `cd web && npm test -- --run src/components/folders/__tests__/ux-new-folder-dialog.test.tsx && npx tsc --noEmit && npm run lint && npm test -- --run`
Expected: green. Re-run the label grep and report how many remain.

- [ ] **Step 6: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src web/public/locales/en/common.json web/public/locales/ar/common.json
git commit -m "fix(web): required-field signalling and associated form labels"
```

---

### Task 7: Hierarchy — stop the badge/shadow noise

**Files:**
- Modify: `web/src/components/ui/shadcn/badge.tsx`
- Modify: the audit-log table's Action badge call site (`grep -rn "AuditLogTable" src` to locate), and the task-priority mapping in `web/src/components/tasks/` or `tasks.tsx`

**Interfaces:**
- Consumes: the `badgeVariants` CVA from Task-1-era code (variants incl. `default`, `outline`, lifecycle variants).
- Produces: nothing later tasks depend on.

- [ ] **Step 1: Remove the shadow from non-interactive pills**

In `badge.tsx`, `shadow-neu-sm` is applied to every variant, so each table row stacks shadows. Badges are labels, not controls — drop `shadow-neu-sm` from the variant strings, keeping the tinted background and text colour as the cue. (Leave any genuinely interactive badge — one with an `onClick` at its call site — with its shadow; grep for that before removing.)

- [ ] **Step 2: Calm the audit log**

At the audit-log Action cell, pass a flat variant instead of the solid primary default so ~17 rows stop rendering identical bright pills:

```tsx
<Badge variant="outline">{event.action}</Badge>
```

- [ ] **Step 3: Un-invert task priority**

Find the priority→variant map (grep `priority` in the tasks components). Today `normal` — the commonest value — maps to the loudest solid style. Invert it so prominence tracks urgency: `urgent`/`high` → the strong variant, `normal` → a muted/outline variant, `low` → the quietest.

- [ ] **Step 4: Verify**

Run: `cd web && npx tsc --noEmit && npm run lint && npm test -- --run`
Expected: green (update any test asserting the old badge classes to the new contract rather than deleting it). Check `/admin/audit` and `/tasks` on :3000 — rows should read as data, not a wall of buttons.

- [ ] **Step 5: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/components web/src/routes
git commit -m "fix(web): calmer list hierarchy — badges read as data, priority prominence corrected"
```

---

### Task 8: Gates, visual verification, spec finalize

**Files:**
- Modify: `web/e2e/71-keyboard.spec.ts` (add the single-click/Enter-to-open coverage for tiles)
- Modify: `docs/superpowers/specs/2026-09-17-ux-improvements-design.md` (§7 status)

- [ ] **Step 1: Extend the keyboard spec**

Add a case asserting a folder tile opens on `Enter` (the fix from Task 5), reusing the file's existing mock helpers and login flow.

- [ ] **Step 2: Run every gate against the built bundle**

Playwright's system libs need staging (no root). Stage them once, then:

```bash
export LD_LIBRARY_PATH=/tmp/claude-1000/-home-sedoc-DOCMS/0d145252-1392-4188-8ccf-5636f0a3a641/scratchpad/pwlibs/extracted/usr/lib/x86_64-linux-gnu
cd /home/sedoc/DOCMS/web && npm run build
(npx vite preview --port 4173 --host 127.0.0.1 >/tmp/ux-preview.log 2>&1 &) ; sleep 4
CI= npx playwright test e2e/70-a11y.spec.ts e2e/71-keyboard.spec.ts
pkill -f "vite preview --port 4173" || true
```

Expected: `70-a11y.spec.ts` stays 16/16 (the label-association work should help, never hurt). Fix any new axe violation in the owning component.

Known pre-existing and out of scope: `71-keyboard.spec.ts` and `i18n-rtl-interactions.spec.ts` carry 5 failures caused by test-infra (a missing `**/api/v1/**` abort catch-all letting a real 401 through, `/tasks/mine` vs `/tasks?filter=mine` endpoint drift, and a `getByLabel(/password/i)` locator matching the password toggle). Do not chase them; report their count unchanged.

- [ ] **Step 3: Visual check**

Screenshot the touched screens at 390px and 1440px, light and dark, using the session helper:

```bash
node <scratchpad>/shoot-one.mjs /tasks light desktop <out>.png
```

Confirm: no false empty states, calmer list rows, a visible upload tray during an upload, and a search entry point on mobile.

- [ ] **Step 4: Finalize + commit**

Update the spec's §7 to record what shipped and what deferred to P3. Run the full local gate set (`tsc`, `lint`, `npm test -- --run`, `npm run build`) and commit:

```bash
cd /home/sedoc/DOCMS
git add web/e2e/71-keyboard.spec.ts docs/superpowers/specs/2026-09-17-ux-improvements-design.md
git commit -m "test(web): keyboard coverage for tile open; UX phase finalized"
```
