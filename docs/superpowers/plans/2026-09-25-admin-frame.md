# Admin Page Frame Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every admin page one shared frame — a single `h1`, tabs in the header, width chosen by page kind — and migrate all 69 real admin pages onto it, with a lint guard so nothing can bypass it.

**Architecture:** Three pure-presentation components in `web/src/components/admin/` (`AdminPage`, `AdminSection`, `DirectoryGroup`) compose the existing `PageHeader` and shadcn `Tabs`. Pages keep every line of state, routing and data code; only their root wrapper and header JSX change. A script in `web/scripts/` fails `npm run lint` if an admin route renders `PageHeader` or centres itself.

**Tech Stack:** React 18, TypeScript, Tailwind 3.4 (logical utilities only — ADR 0107), Radix Tabs via `@/components/ui/shadcn/tabs`, TanStack Router file routes, vitest + Testing Library, Playwright.

**Spec:** `docs/superpowers/specs/2026-09-25-admin-frame-design.md`

## Global Constraints

- **UI only.** No change to any `validateSearch`, `navigate(...)`, `redirect`, query, mutation, route path, `beforeLoad`, or `data-testid` value. A diff line outside JSX/className in a migrated route file is a review stop.
- **Heading names are invariants.** Three e2e specs assert `getByRole('heading', { name: /webhooks|event streaming|email ingestion/i })` on embedded children. The level may move from `h1` to `h2`; the accessible name must not change.
- **Logical Tailwind only** (`ms-`/`me-`/`ps-`/`pe-`/`start-`/`end-`): `npm run lint:rtl` enforces it.
- **`measure` = `max-w-4xl` (896px), start-aligned.** Never `mx-auto`. The shell (`app-layout.tsx`) already pads every page with `p-4 sm:p-6 lg:p-8`; pages add no padding.
- **All commands run from `web/`.** Gates for every task: `npx tsc --noEmit`, `npm run lint`, `npx vitest run`, and the Playwright specs named in the task. Playwright needs a build first (`npm run build`) and, on this machine, `LD_LIBRARY_PATH=$SCRATCH/pwlibs/extracted/usr/lib/x86_64-linux-gnu`.
- **Commit after every task**, message in the house style (what/why, measured before/after), trailer `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- **Do not touch** `web/src/routes/_authenticated/settings/security/index.tsx` — another session owns it.

## Review Focus

1. **A `tabs.value` that matches no item** (a stale deep link). `validateSearch` already normalises every container's search, so the page never passes one — but the frame must not crash if it does. Pinned in Task 1: renders all triggers, none selected.
2. **The hub filter with regex-special characters** (`(`, `[`, `*`). Must match by plain substring, never throw. Pinned in Task 3: `matchesQuery(entry, '(')`.
3. **Role-gated hub rows.** A compliance officer sees only the intelligence rows `adminPathAllowsComplianceOfficer` permits; non-platform admins never see the Platform group. The logic is moved verbatim (Task 12) and checked by hand in that task's verification step, since route components need a router context the unit tests do not provide.
4. **A section title long enough to collide with its actions.** `PageHeader` already gives the title `min-w-0` and the actions `shrink-0`; Task 2 pins that `AdminSection` keeps the actions slot rendered alongside a long title.
5. **An embedded child with no description.** `AdminSection` must render no empty paragraph. Pinned in Task 2.

---

## File Structure

**Create**
- `web/src/components/admin/AdminPage.tsx` — page frame: header, controlled tab rows, width column.
- `web/src/components/admin/AdminSection.tsx` — `h2`-scale section header for embedded children and internal sections.
- `web/src/components/admin/DirectoryGroup.tsx` — hub directory primitives + `matchesQuery`.
- `web/src/components/admin/__tests__/AdminPage.test.tsx`
- `web/src/components/admin/__tests__/AdminSection.test.tsx`
- `web/src/components/admin/__tests__/DirectoryGroup.test.tsx`
- `web/scripts/check-admin-frame.mjs` — the guard.

**Modify**
- `web/package.json` — `lint:admin` script, appended to `lint`.
- 13 containers, 37 embedded children, 18 standalone pages, 2 hubs under `web/src/routes/_authenticated/admin/` (listed per task).
- `web/e2e/72-rtl-geometry.spec.ts:128` — `ROUTES`.
- `web/e2e/70-a11y.spec.ts` — two new scans.

**Untouched**
- `routing.tsx`, `pii.tsx`, `billing.tsx`, `integrations-hub.tsx`, `intelligence/index.tsx`, `intelligence/anomaly-reports.tsx`, `tenant/index.tsx`, `tenant/identity/index.tsx` (redirect shims), `integrations.tsx` (layout route), `components/shared/PageHeader.tsx`.

---

### Task 1: `AdminPage`

**Files:**
- Create: `web/src/components/admin/AdminPage.tsx`
- Test: `web/src/components/admin/__tests__/AdminPage.test.tsx`

**Interfaces:**
- Consumes: `PageHeader` from `@/components/shared/PageHeader` (props `title`, `description`, `actions`, `noMargin`, `variant`); `Tabs`, `TabsList`, `TabsTrigger` from `@/components/ui/shadcn/tabs`; `cn` from `@/lib/cn`.
- Produces:
  ```ts
  export type AdminWidth = 'measure' | 'full'
  export interface AdminTabItem { value: string; label: ReactNode; testId?: string }
  export interface AdminTabs { value: string; onValueChange: (value: string) => void; items: AdminTabItem[]; ariaLabel?: string }
  export interface AdminPageProps { title: ReactNode; description?: ReactNode; actions?: ReactNode; width?: AdminWidth; tabs?: AdminTabs; children: ReactNode }
  export function AdminPage(props: AdminPageProps): JSX.Element
  ```
  Root carries `data-testid="admin-page"`; the content column carries `data-admin-width="measure"|"full"`. Pages render their own `<TabsContent value="…">` as children — the frame's `Tabs` root wraps the column, so Radix's `aria-controls` resolve.

  **There is deliberately no `subTabs` prop.** Radix `TabsContent` binds to the *nearest* `Tabs` root, so a frame-owned second root around the column would capture the page's primary-level `TabsContent` and render nothing. A page with two levels (Identity) renders its inner `<Tabs>` inside each primary `TabsContent`, as it does today — see Task 5. (Deviation from spec §4.1/§8, recorded there.)

- [ ] **Step 1: Write the failing test**

```tsx
// web/src/components/admin/__tests__/AdminPage.test.tsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { AdminPage } from '../AdminPage'

const tabs = {
  value: 'a',
  onValueChange: vi.fn(),
  items: [
    { value: 'a', label: 'Alpha', testId: 'tab-a' },
    { value: 'b', label: 'Beta', testId: 'tab-b' },
  ],
}

describe('<AdminPage>', () => {
  it('renders exactly one h1 and the description', () => {
    render(<AdminPage title="Users" description="Manage members">body</AdminPage>)
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
    expect(screen.getByRole('heading', { level: 1, name: 'Users' })).toBeInTheDocument()
    expect(screen.getByText('Manage members')).toBeInTheDocument()
    expect(screen.getByTestId('admin-page')).toBeInTheDocument()
  })

  it('defaults to the measure width and exposes it as a data attribute', () => {
    render(<AdminPage title="X">body</AdminPage>)
    const col = screen.getByText('body').closest('[data-admin-width]')
    expect(col).toHaveAttribute('data-admin-width', 'measure')
    expect(col?.className).toContain('max-w-4xl')
    expect(col?.className).not.toContain('mx-auto')
  })

  it('full width drops the cap', () => {
    render(<AdminPage title="X" width="full">body</AdminPage>)
    const col = screen.getByText('body').closest('[data-admin-width]')
    expect(col).toHaveAttribute('data-admin-width', 'full')
    expect(col?.className).not.toContain('max-w-4xl')
  })

  it('tabs are controlled: a click reports the value and does not move the selection', async () => {
    const user = userEvent.setup()
    render(
      <AdminPage title="X" tabs={tabs}>
        <TabsContent value="a">A body</TabsContent>
        <TabsContent value="b">B body</TabsContent>
      </AdminPage>,
    )
    expect(screen.getByTestId('tab-a')).toHaveAttribute('aria-selected', 'true')
    await user.click(screen.getByTestId('tab-b'))
    expect(tabs.onValueChange).toHaveBeenCalledWith('b')
    // Still controlled by `value`, which the test did not change.
    expect(screen.getByTestId('tab-a')).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByText('A body')).toBeInTheDocument()
  })

  it('a nested Tabs inside a TabsContent keeps its own selection (the Identity shape)', async () => {
    const user = userEvent.setup()
    const inner = vi.fn()
    render(
      <AdminPage title="X" tabs={tabs}>
        <TabsContent value="a">
          <Tabs value="x" onValueChange={inner}>
            <TabsList>
              <TabsTrigger value="x" data-testid="sub-x">Ex</TabsTrigger>
              <TabsTrigger value="y" data-testid="sub-y">Why</TabsTrigger>
            </TabsList>
            <TabsContent value="x">X body</TabsContent>
          </Tabs>
        </TabsContent>
      </AdminPage>,
    )
    expect(screen.getAllByRole('tablist')).toHaveLength(2)
    expect(screen.getByText('X body')).toBeInTheDocument()
    await user.click(screen.getByTestId('sub-y'))
    expect(inner).toHaveBeenCalledWith('y')
    expect(tabs.onValueChange).not.toHaveBeenCalledWith('y')
  })

  it('tolerates a value that matches no item (stale deep link) without crashing', () => {
    render(<AdminPage title="X" tabs={{ ...tabs, value: 'zzz' }}>body</AdminPage>)
    expect(screen.getAllByRole('tab')).toHaveLength(2)
    expect(screen.queryByRole('tab', { selected: true })).toBeNull()
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/components/admin/__tests__/AdminPage.test.tsx`
Expected: FAIL — `Cannot find module '../AdminPage'`

- [ ] **Step 3: Write the component**

```tsx
// web/src/components/admin/AdminPage.tsx
import type { ReactNode } from 'react'

import { PageHeader } from '@/components/shared/PageHeader'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/shadcn/tabs'
import { cn } from '@/lib/cn'

// The one way to be an admin page. It owns the only h1, draws the tab
// rows in the header, and decides how wide the content column is. It
// holds no state: tabs are controlled by the page, which keeps its
// validateSearch + navigate exactly as before and passes value/onChange
// down. Pages render their own <TabsContent> as children — the Radix
// root wraps the column, so aria-controls still resolves.
//
// Width is a property of the page kind, set once here, never by the
// page: `measure` is 896px start-aligned (the shell supplies the 32px
// gutter; nothing here centres), `full` spans the column. The column is
// a flex column so a child that needs height (the metadata-schema
// builder's min-h-0 flex-1) keeps working.

export type AdminWidth = 'measure' | 'full'

export interface AdminTabItem {
  value: string
  label: ReactNode
  testId?: string
}

export interface AdminTabs {
  value: string
  onValueChange: (value: string) => void
  items: AdminTabItem[]
  ariaLabel?: string
}

export interface AdminPageProps {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  width?: AdminWidth
  tabs?: AdminTabs
  children: ReactNode
}

// No `subTabs`. Radix TabsContent binds to the NEAREST Tabs root, so a
// second frame-owned root around the column would capture the page's
// primary-level TabsContent and render nothing (and leave the selected
// primary trigger's aria-controls pointing at a missing id). A page with
// two levels renders its inner <Tabs> inside each primary TabsContent.
export function AdminPage({
  title,
  description,
  actions,
  width = 'measure',
  tabs,
  children,
}: AdminPageProps) {
  const header = <PageHeader title={title} description={description} actions={actions} noMargin />
  const column = (
    <div
      data-admin-width={width}
      className={cn('flex min-w-0 flex-col gap-6', width === 'measure' && 'w-full max-w-4xl')}
    >
      {children}
    </div>
  )

  if (!tabs) {
    return (
      <div data-testid="admin-page" className="flex min-w-0 flex-col gap-6">
        {header}
        {column}
      </div>
    )
  }

  return (
    <Tabs
      value={tabs.value}
      onValueChange={tabs.onValueChange}
      data-testid="admin-page"
      className="flex min-w-0 flex-col gap-6"
    >
      <div className="flex flex-col gap-4">
        {header}
        <TabRow tabs={tabs} />
      </div>
      {column}
    </Tabs>
  )
}

function TabRow({ tabs }: { tabs: AdminTabs }) {
  return (
    <TabsList aria-label={tabs.ariaLabel} className="w-fit">
      {tabs.items.map((t) => (
        <TabsTrigger key={t.value} value={t.value} data-testid={t.testId}>
          {t.label}
        </TabsTrigger>
      ))}
    </TabsList>
  )
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `npx vitest run src/components/admin/__tests__/AdminPage.test.tsx`
Expected: PASS (6 tests)

- [ ] **Step 5: Gates and commit**

Run: `npx tsc --noEmit && npm run lint`
Expected: both exit 0

```bash
git add src/components/admin/AdminPage.tsx src/components/admin/__tests__/AdminPage.test.tsx
git commit -m "feat(web): AdminPage — the one frame every admin page renders through"
```

---

### Task 2: `AdminSection`

**Files:**
- Create: `web/src/components/admin/AdminSection.tsx`
- Test: `web/src/components/admin/__tests__/AdminSection.test.tsx`

**Interfaces:**
- Consumes: `PageHeader` (`variant="section"` renders an `h2` at `text-lg`), `cn`.
- Produces:
  ```ts
  export interface AdminSectionProps { title: ReactNode; description?: ReactNode; actions?: ReactNode; className?: string; children: ReactNode }
  export function AdminSection(props: AdminSectionProps): JSX.Element
  ```
  `className` is merged with `cn`, so a caller passing `gap-6` overrides the default `gap-4` (twMerge).

- [ ] **Step 1: Write the failing test**

```tsx
// web/src/components/admin/__tests__/AdminSection.test.tsx
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { AdminSection } from '../AdminSection'

describe('<AdminSection>', () => {
  it('renders an h2, never an h1', () => {
    render(<AdminSection title="Users">body</AdminSection>)
    expect(screen.getByRole('heading', { level: 2, name: 'Users' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 1 })).toBeNull()
  })

  it('renders no empty paragraph when there is no description', () => {
    const { container } = render(<AdminSection title="X">body</AdminSection>)
    expect(container.querySelector('p')).toBeNull()
  })

  it('keeps the actions slot beside a long title', () => {
    render(
      <AdminSection
        title={'A '.repeat(80) + 'very long section title that must not push the actions away'}
        actions={<button>New</button>}
      >
        body
      </AdminSection>,
    )
    expect(screen.getByRole('button', { name: 'New' })).toBeInTheDocument()
  })

  it('lets a caller widen the gap', () => {
    const { container } = render(<AdminSection title="X" className="gap-6">body</AdminSection>)
    const section = container.querySelector('section')
    expect(section?.className).toContain('gap-6')
    expect(section?.className).not.toContain('gap-4')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/components/admin/__tests__/AdminSection.test.tsx`
Expected: FAIL — `Cannot find module '../AdminSection'`

- [ ] **Step 3: Write the component**

```tsx
// web/src/components/admin/AdminSection.tsx
import type { ReactNode } from 'react'

import { PageHeader } from '@/components/shared/PageHeader'
import { cn } from '@/lib/cn'

// h2-scale section for two uses: an embedded child of a merged page
// (UsersPage inside Identity), and an internal section of a standalone
// page (Encryption's "Register external KMS"). A child never renders
// AdminPage or PageHeader — that is what produced two headers on every
// merged page. Actions sit at the section's own end edge, next to the
// thing they act on, rather than 1300px away in a page header.

export interface AdminSectionProps {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  className?: string
  children: ReactNode
}

export function AdminSection({ title, description, actions, className, children }: AdminSectionProps) {
  return (
    <section className={cn('flex min-w-0 flex-col gap-4', className)}>
      <PageHeader variant="section" title={title} description={description} actions={actions} noMargin />
      {children}
    </section>
  )
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `npx vitest run src/components/admin/__tests__/AdminSection.test.tsx`
Expected: PASS (4 tests)

- [ ] **Step 5: Gates and commit**

Run: `npx tsc --noEmit && npm run lint`

```bash
git add src/components/admin/AdminSection.tsx src/components/admin/__tests__/AdminSection.test.tsx
git commit -m "feat(web): AdminSection — h2 header for embedded children and internal sections"
```

---

### Task 3: `DirectoryGroup` + `matchesQuery`

**Files:**
- Create: `web/src/components/admin/DirectoryGroup.tsx`
- Test: `web/src/components/admin/__tests__/DirectoryGroup.test.tsx`

**Interfaces:**
- Consumes: `Card` from `@/components/ui/card`, `Link` from `@tanstack/react-router`, `DirectionalIcon` from `@/components/shared/DirectionalIcon`, `LucideIcon` type.
- Produces:
  ```ts
  export interface DirectoryEntry { to: string; icon: LucideIcon; label: string; desc: string; testId?: string }
  export function matchesQuery(entry: Pick<DirectoryEntry, 'label' | 'desc'>, q: string): boolean
  export function DirectoryGroup(p: { id: string; label: string; description?: string; children: ReactNode }): JSX.Element
  export function DirectorySub(p: { label: string; children: ReactNode }): JSX.Element
  export function DirectoryList(p: { children: ReactNode }): JSX.Element
  export function DirectoryRow(p: DirectoryEntry): JSX.Element
  ```

- [ ] **Step 1: Write the failing test**

```tsx
// web/src/components/admin/__tests__/DirectoryGroup.test.tsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { Users } from 'lucide-react'

// TanStack's <Link> needs a router; the row only needs an anchor.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, ...rest }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...rest}>{children}</a>
  ),
}))

import { DirectoryGroup, DirectorySub, DirectoryList, DirectoryRow, matchesQuery } from '../DirectoryGroup'

describe('matchesQuery', () => {
  const entry = { label: 'Identity & Access', desc: 'Users, groups, roles, SSO' }
  it('matches label or description, case-insensitively', () => {
    expect(matchesQuery(entry, 'sso')).toBe(true)
    expect(matchesQuery(entry, 'IDENTITY')).toBe(true)
    expect(matchesQuery(entry, 'billing')).toBe(false)
  })
  it('an empty query matches everything', () => {
    expect(matchesQuery(entry, '')).toBe(true)
    expect(matchesQuery(entry, '   ')).toBe(true)
  })
  it('treats regex-special characters as plain text', () => {
    expect(() => matchesQuery(entry, '(')).not.toThrow()
    expect(matchesQuery({ label: 'A (beta)', desc: '' }, '(beta')).toBe(true)
    expect(matchesQuery(entry, '[')).toBe(false)
  })
})

describe('<DirectoryGroup>', () => {
  it('names the group with an h2 and a sub-group with an h3, and rows are links', () => {
    render(
      <DirectoryGroup id="g-tenant" label="Tenant administration" description="People, policy.">
        <DirectorySub label="People & access">
          <DirectoryRow to="/admin/identity" icon={Users} label="Identity & Access" desc="Users, groups" testId="dir-identity" />
        </DirectorySub>
      </DirectoryGroup>,
    )
    expect(screen.getByRole('heading', { level: 2, name: 'Tenant administration' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 3, name: 'People & access' })).toBeInTheDocument()
    const link = screen.getByTestId('dir-identity')
    expect(link).toHaveAttribute('href', '/admin/identity')
    expect(link).toHaveTextContent('Identity & Access')
    expect(link).toHaveTextContent('Users, groups')
  })

  it('a flat list needs no sub-heading', () => {
    render(
      <DirectoryGroup id="g-int" label="Integrations">
        <DirectoryList>
          <DirectoryRow to="/admin/integrations" icon={Users} label="Integrations" desc="eSignature" />
        </DirectoryList>
      </DirectoryGroup>,
    )
    expect(screen.queryByRole('heading', { level: 3 })).toBeNull()
    expect(screen.getByRole('link', { name: /Integrations/ })).toHaveAttribute('href', '/admin/integrations')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/components/admin/__tests__/DirectoryGroup.test.tsx`
Expected: FAIL — `Cannot find module '../DirectoryGroup'`

- [ ] **Step 3: Write the component**

```tsx
// web/src/components/admin/DirectoryGroup.tsx
import type { ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import type { LucideIcon } from 'lucide-react'

import { Card } from '@/components/ui/card'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

// The admin hub as a directory rather than a brochure: one Card per
// group, 44px rows inside, every entry visible at 1000px. Icons are
// monochrome — 21 identical primary tiles differentiated nothing.

export interface DirectoryEntry {
  to: string
  icon: LucideIcon
  label: string
  desc: string
  testId?: string
}

// Plain substring, never a RegExp: the query comes from a text box and
// "(" must not throw.
export function matchesQuery(entry: Pick<DirectoryEntry, 'label' | 'desc'>, q: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  return `${entry.label} ${entry.desc}`.toLowerCase().includes(needle)
}

export function DirectoryGroup({
  id,
  label,
  description,
  children,
}: {
  id: string
  label: string
  description?: string
  children: ReactNode
}) {
  return (
    <Card className="overflow-hidden p-0" aria-labelledby={id} role="region">
      <div className="border-b border-border px-4 py-3">
        <h2 id={id} className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          {label}
        </h2>
        {description && <p className="mt-0.5 text-sm text-muted-foreground">{description}</p>}
      </div>
      {children}
    </Card>
  )
}

export function DirectorySub({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <h3 className="bg-muted/30 px-4 py-1.5 text-xs font-medium text-foreground">{label}</h3>
      <ul className="divide-y divide-border">{children}</ul>
    </div>
  )
}

export function DirectoryList({ children }: { children: ReactNode }) {
  return <ul className="divide-y divide-border">{children}</ul>
}

export function DirectoryRow({ to, icon: Icon, label, desc, testId }: DirectoryEntry) {
  return (
    <li>
      <Link
        to={to}
        data-testid={testId}
        className={
          'group flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-muted/40 ' +
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring'
        }
      >
        <Icon className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
        <span dir="auto" className="min-w-0 flex-1">
          <span className="block truncate text-sm font-medium text-foreground">{label}</span>
          <span className="block truncate text-xs text-muted-foreground">{desc}</span>
        </span>
        <DirectionalIcon
          name="ChevronRight"
          className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 rtl:rotate-180"
          aria-hidden
        />
      </Link>
    </li>
  )
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `npx vitest run src/components/admin/__tests__/DirectoryGroup.test.tsx`
Expected: PASS (5 tests)

- [ ] **Step 5: Gates and commit**

Run: `npx tsc --noEmit && npm run lint`

```bash
git add src/components/admin/DirectoryGroup.tsx src/components/admin/__tests__/DirectoryGroup.test.tsx
git commit -m "feat(web): DirectoryGroup — hub rows instead of cards, plus a plain-text matcher"
```

---

### Task 4: The guard, in report mode

**Files:**
- Create: `web/scripts/check-admin-frame.mjs`
- Modify: `web/package.json:16-20` (`lint` chain and a new `lint:admin`)

**Interfaces:**
- Produces: `node scripts/check-admin-frame.mjs [--report]`. Exit 1 on violations unless `--report`, which prints them and exits 0. Marker to exempt a line: `// admin-frame: exempt — <reason>` on the line or the line above.

- [ ] **Step 1: Write the script**

```js
#!/usr/bin/env node
// Guard for the admin page frame (spec: docs/superpowers/specs/
// 2026-09-25-admin-frame-design.md §5). Run via `npm run lint:admin`.
//
// Every admin route renders through <AdminPage> (a page) or
// <AdminSection> without <AdminPage> (an embedded child). Nothing under
// admin/ renders <PageHeader> — the frame composes it — and nothing
// centres itself with mx-auto max-w-*: the frame owns width.
//
// `--report` prints violations and exits 0, so the guard can ship before
// the migration and flip to failing once every page is on the frame.
//
// Opt-out: `// admin-frame: exempt — <reason>` on the line or the line
// above. It exists so a reviewer stops and asks, not to silence the rule.

import { readFileSync } from 'node:fs'
import { readdir } from 'node:fs/promises'
import { join, dirname, resolve, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const SRC = resolve(__dirname, '..', 'src')
const ROOT = join(SRC, 'routes', '_authenticated', 'admin')
const REPORT = process.argv.includes('--report')
const MARKER = 'admin-frame: exempt'

const LAYOUT_ROUTES = new Set(['integrations.tsx'])

async function walk(dir) {
  const out = []
  for (const e of await readdir(dir, { withFileTypes: true })) {
    if (e.name === '__tests__') continue
    const p = join(dir, e.name)
    if (e.isDirectory()) out.push(...(await walk(p)))
    else if (e.isFile() && e.name.endsWith('.tsx')) out.push(p)
  }
  return out
}

const exempt = (lines, i) =>
  lines[i].includes(MARKER) || (i > 0 && lines[i - 1].includes(MARKER))

function check(file) {
  const src = readFileSync(file, 'utf8')
  const lines = src.split('\n')
  const rel = relative(ROOT, file)
  const out = []

  const redirects = /throw redirect\(|redirect\(\{/.test(src)
  const isShim = redirects && lines.length < 60
  if (isShim || LAYOUT_ROUTES.has(rel)) return out

  const hasPage = /<AdminPage\b/.test(src)
  const hasSection = /<AdminSection\b/.test(src)

  for (let i = 0; i < lines.length; i++) {
    const l = lines[i]
    if (exempt(lines, i)) continue
    if (/<PageHeader\b/.test(l)) out.push({ rel, line: i + 1, msg: 'renders <PageHeader>; use AdminPage or AdminSection' })
    if (/\bmx-auto\b/.test(l) && /\bmax-w-/.test(l)) out.push({ rel, line: i + 1, msg: 'centres itself with mx-auto max-w-*; the frame owns width' })
  }
  if (!hasPage && !hasSection) out.push({ rel, line: 1, msg: 'no <AdminPage> or <AdminSection>' })
  if (hasPage && redirects) out.push({ rel, line: 1, msg: 'embedded child (its route redirects) renders <AdminPage>; use AdminSection' })
  return out
}

async function main() {
  const violations = (await Promise.all((await walk(ROOT)).map(check))).flat()
  if (violations.length === 0) {
    console.log('check-admin-frame: ✓ every admin route is on the frame')
    return
  }
  const log = REPORT ? console.log : console.error
  log(`check-admin-frame: ${violations.length} violation(s)${REPORT ? ' (report mode)' : ''}`)
  for (const v of violations) log(`  admin/${v.rel}:${v.line}  ${v.msg}`)
  if (!REPORT) process.exit(1)
}

main()
```

- [ ] **Step 2: Wire it into package.json**

In `web/package.json`, change the `lint` line and add `lint:admin`:

```json
    "lint": "eslint . && npm run lint:rtl && npm run lint:utf8 && npm run lint:tenant && npm run lint:admin",
    "lint:admin": "node scripts/check-admin-frame.mjs --report",
```

- [ ] **Step 3: Run it and confirm it reports every page, exits 0**

Run: `npm run lint:admin; echo "exit $?"`
Expected: a list of ~70 violations (every page renders `<PageHeader>` or has no frame), then `exit 0`.

Run: `node scripts/check-admin-frame.mjs; echo "exit $?"`
Expected: the same list, then `exit 1` — proof the non-report mode fails.

- [ ] **Step 4: Full lint still passes, commit**

Run: `npm run lint; echo "exit $?"`
Expected: `exit 0` (report mode).

```bash
git add scripts/check-admin-frame.mjs package.json
git commit -m "chore(web): lint:admin guard for the admin frame, reporting until the migration lands"
```

---

### Task 5: Identity + its six children

The container gets the frame with two tab rows. Each child swaps its page header for a section.

**Files:**
- Modify: `web/src/routes/_authenticated/admin/identity.tsx:47-105`
- Modify: `users.tsx` (root `<div className="space-y-6">`, `<PageHeader` at the line found by `grep -n '<PageHeader' users.tsx`), `groups.tsx` (`space-y-6`), `permissions.tsx` (two returns: L60 and L73, both `<div>` with `<PageHeader` at L62 and L75), `sso.tsx` (`<div>`), `tenant/identity/ldap.tsx` (`space-y-6`), `scim.tsx` (`mx-auto max-w-4xl p-6`)
- Test: existing `e2e/44-ldap-admin.spec.ts`; `npx vitest run src/routes/_authenticated/admin/__tests__/scim.test.tsx`

**Interfaces:**
- Consumes: `AdminPage`, `AdminTabs` (Task 1), `AdminSection` (Task 2). Children keep their exported names: `UsersPage`, `GroupsPage`, `PermissionsPage`, `SsoPage`, `LDAPAdminPage`, `ScimPage`.

- [ ] **Step 1: Record the invariants before touching anything**

```bash
A=src/routes/_authenticated/admin
for f in identity users groups permissions sso tenant/identity/ldap scim; do
  grep -o 'data-testid=[^ >]*' $A/$f.tsx | sort > /tmp/testids-$(basename $f)-before.txt
done
```

- [ ] **Step 2: Rewrite the container's render**

Only the *outer* `Tabs` moves into the frame. The two inner `<Tabs value={activeSub} …>` blocks stay exactly where they are — inside their primary `TabsContent` — because Radix binds `TabsContent` to the nearest root (see Task 1). Replace `identity.tsx` lines 47–105 (from `return (` through the closing `)` of the return) with:

```tsx
  // Width follows the active sub-tab (spec §4.2): tables full, forms measured.
  const width = activeSub === 'users' || activeSub === 'permissions' || activeSub === 'scim' ? 'full' : 'measure'

  return (
    <AdminPage
      title="Identity & Access"
      description="Users, groups, roles, SSO, LDAP/AD, SCIM provisioning."
      tabs={{
        value: activeGroup,
        onValueChange: (v) =>
          navigate({
            to: '/admin/identity',
            search: { group: v as Group },
          }),
        items: [
          { value: 'people', label: <>People &amp; Roles</> },
          { value: 'auth', label: 'Authentication' },
        ],
      }}
      width={width}
    >
      <TabsContent value="people">
        <Tabs
          value={activeSub}
          onValueChange={(v) =>
            navigate({
              to: '/admin/identity',
              search: { group: 'people', sub: v as Sub },
            })
          }
        >
          <TabsList className="w-fit">
            <TabsTrigger value="users">Users</TabsTrigger>
            <TabsTrigger value="groups">Groups</TabsTrigger>
            <TabsTrigger value="permissions">Permission matrix</TabsTrigger>
          </TabsList>
          <TabsContent value="users" className="mt-4"><UsersPage /></TabsContent>
          <TabsContent value="groups" className="mt-4"><GroupsPage /></TabsContent>
          <TabsContent value="permissions" className="mt-4"><PermissionsPage /></TabsContent>
        </Tabs>
      </TabsContent>

      <TabsContent value="auth">
        <Tabs
          value={activeSub}
          onValueChange={(v) =>
            navigate({
              to: '/admin/identity',
              search: { group: 'auth', sub: v as Sub },
            })
          }
        >
          <TabsList className="w-fit">
            <TabsTrigger value="sso">SSO (SAML / OIDC)</TabsTrigger>
            <TabsTrigger value="ldap">LDAP / AD</TabsTrigger>
            <TabsTrigger value="scim">SCIM provisioning</TabsTrigger>
          </TabsList>
          <TabsContent value="sso" className="mt-4"><SsoPage /></TabsContent>
          <TabsContent value="ldap" className="mt-4"><LDAPAdminPage /></TabsContent>
          <TabsContent value="scim" className="mt-4"><ScimPage /></TabsContent>
        </Tabs>
      </TabsContent>
    </AdminPage>
  )
```

The `import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'` line stays as it is (the inner Tabs still use all four); add `import { AdminPage } from '@/components/admin/AdminPage'`.

The three `navigate(...)` calls are the existing ones, moved verbatim; `validateSearch`, `PEOPLE_SUBS`, `AUTH_SUBS`, `activeGroup`, `activeSub` are untouched. What changed on screen: the page now has an `h1` above the group tabs, and the sub-tab row sits at the top of the content column instead of a second bare row above a repeated title.

- [ ] **Step 3: Convert each child**

The transformation, applied to every child in this task:

1. Delete the `<PageHeader … />` element (multi-line: from `<PageHeader` to its `/>`), keeping the values of its `title`, `description` and `actions` props.
2. Replace the component's root opening tag with `<AdminSection title={…} description={…} actions={…}>` using exactly those values, and its matching closing `</div>` with `</AdminSection>`. If the old root was `<div className="space-y-6">`, pass `className="gap-6"` so the child's own rhythm is unchanged. If the old root was `<div className="mx-auto max-w-4xl p-6">` or a bare `<div>`, pass no `className`.
3. Replace `import { PageHeader } from '@/components/shared/PageHeader'` with `import { AdminSection } from '@/components/admin/AdminSection'`.

Worked example — `users.tsx` (root `<div className="space-y-6">`, header has `actions`):

```tsx
// before
  return (
    <div className="space-y-6">
      <PageHeader
        title="Users"
        description="Manage team members, roles, and access. New users can be invited by email or created directly with an initial password."
        actions={ /* …the existing <> Bulk invite / Add user buttons </> … */ }
      />
      …rest…
    </div>
  )

// after
  return (
    <AdminSection
      title="Users"
      description="Manage team members, roles, and access. New users can be invited by email or created directly with an initial password."
      actions={ /* the same <>…</> block, moved verbatim */ }
      className="gap-6"
    >
      …rest, unchanged…
    </AdminSection>
  )
```

Per file:

| File | Old root | `className` | Notes |
|---|---|---|---|
| `users.tsx` | `space-y-6` | `gap-6` | actions moved verbatim |
| `groups.tsx` | `space-y-6` | `gap-6` | actions moved verbatim |
| `permissions.tsx` | `<div>` ×2 (L60, L73) | — | **both** returns: each has its own `<PageHeader title="Permission Matrix" …/>`; convert each |
| `sso.tsx` | `<div>` | — | actions moved verbatim |
| `tenant/identity/ldap.tsx` | `space-y-6` | `gap-6` | |
| `scim.tsx` | `mx-auto max-w-4xl p-6` | — | this was one of the centred pages |

- [ ] **Step 4: Verify the invariants**

```bash
A=src/routes/_authenticated/admin
for f in identity users groups permissions sso tenant/identity/ldap scim; do
  grep -o 'data-testid=[^ >]*' $A/$f.tsx | sort > /tmp/testids-$(basename $f)-after.txt
  diff /tmp/testids-$(basename $f)-before.txt /tmp/testids-$(basename $f)-after.txt && echo "$f: testids unchanged"
done
# Any changed line that is not markup is a review stop:
git diff -U0 -- $A/identity.tsx $A/users.tsx $A/groups.tsx $A/permissions.tsx $A/sso.tsx $A/tenant/identity/ldap.tsx $A/scim.tsx \
  | grep -E '^[-+]' | grep -vE '^(\+\+\+|---)' \
  | grep -vE 'className|AdminPage|AdminSection|PageHeader|Tabs|import|^[-+]\s*(//|\{/\*|\*)|^[-+]\s*[<>{}()/,]*\s*$|title=|description=|actions=|label:|value:|items:|onValueChange|search:|to:|navigate|width|return \(|^\+\s*\}$|^\+\s*\]$|^\+\s*\)$|^\+\s*\},$|^\+\s*const (groupTabs|subTabs|width)'
```
Expected: `testids unchanged` ×7 and an empty grep (nothing but markup changed).

- [ ] **Step 5: Gates**

Run: `npx tsc --noEmit && npm run lint && npx vitest run src/routes/_authenticated/admin/__tests__/scim.test.tsx`
Expected: all pass; `lint:admin` report shrinks by 7 files.

Run: `npm run build && npx playwright test 44-ldap-admin 70-a11y --reporter=list`
Expected: all pass (`admin users table` light + dark scans included).

- [ ] **Step 6: Look at it**

Open `/admin/identity`, `/admin/identity?group=auth&sub=scim`, and `/admin/users` (must redirect into the container). Expect: one `h1` "Identity & Access", two tab rows in the header, "Users" as an `h2` with its buttons beside it, the Users table full-width, SCIM at 896px start-aligned — no centring gutter.

- [ ] **Step 7: Commit**

```bash
git add src/routes/_authenticated/admin/{identity,users,groups,permissions,sso,scim}.tsx src/routes/_authenticated/admin/tenant/identity/ldap.tsx
git commit -m "feat(web): Identity & Access on the admin frame — one h1, tabs in the header, SCIM uncentred"
```

---

### Task 6: Protection, Records & retention, Legal, Audit, Subscription, Tenant settings, Data governance + their children

Seven single-row containers that today have **no** page header of their own, plus their fourteen children.

**Files:**
- Modify containers: `protection.tsx:20-27`, `records-retention.tsx:17-23`, `legal.tsx:17-23`, `audit.tsx:16-22`, `subscription.tsx:17-23`, `tenant-settings.tsx:20-28`, `data-governance.tsx:29-41`
- Modify children: `tenant/classification.tsx`, `tenant/watermark.tsx`, `tenant/irm.tsx`, `records.tsx`, `retention.tsx` (main return at L134), `legal-holds.tsx`, `ediscovery.tsx`, `audit-log.tsx`, `siem.tsx`, `tenant/license.tsx`, `settings.tsx`, `tenant/upload-policy.tsx`, `compliance.tsx`, `residency.tsx`
- Test: `e2e/70-a11y`, `e2e/72-rtl-geometry`; `npx vitest run src/routes/_authenticated/admin`

**Interfaces:** Consumes `AdminPage`, `AdminSection`. Containers pass `width` from `active`.

- [ ] **Step 1: Record invariants** — same loop as Task 5 Step 1 over the 21 files.

- [ ] **Step 2: Rewrite each container's render**

The pattern (same for all seven; `active`, `navigate`, `Tab` and `validateSearch` are the file's existing symbols, untouched). Delete the root `<div className="space-y-4">`, the `<Tabs …>` opening tag, the whole `<TabsList>…</TabsList>`, and the matching `</Tabs>` / `</div>`. Keep every `<TabsContent value="…">` child but drop its `className="mt-4"`. Replace the `Tabs, TabsList, TabsTrigger` import with `TabsContent` only and import `AdminPage`.

`protection.tsx`:
```tsx
  return (
    <AdminPage
      title="Information protection"
      description="Classification & access, watermark, and protected exports (IRM)."
      tabs={{
        value: active,
        onValueChange: (v) => navigate({ to: '/admin/protection', search: { tab: v as Tab } }),
        items: [
          { value: 'classification', label: <>Classification &amp; access</> },
          { value: 'watermark', label: 'Watermark' },
          { value: 'exports', label: 'Protected exports' },
        ],
      }}
      width={active === 'watermark' ? 'measure' : 'full'}
    >
      …existing <TabsContent> children…
    </AdminPage>
  )
```

`records-retention.tsx` — title `Records & retention`, description `File plan, retention policies & schedules, disposition.`, items `records: 'Records file plan'`, `retention: 'Retention policies'`, `width="full"`, navigate target `/admin/records-retention`.

`legal.tsx` — title `Legal holds & e-discovery`, description `Active holds and hold-scoped export of documents, metadata and audit.`, items `holds: 'Legal holds'`, `ediscovery: 'E-discovery export'`, `width="measure"`, target `/admin/legal`.

`audit.tsx` — title `Audit & SIEM`, description `Activity history and forwarding to syslog, Splunk or Sentinel.`, items `log: 'Activity history'`, `forwarding: 'SIEM forwarding'`, `width={active === 'log' ? 'full' : 'measure'}`, target `/admin/audit`.

`subscription.tsx` — title `Subscription & licensing`, description `Plan & usage, and the license: JWT claims, seats, entitlements, expiry.`, items `plan: <>Plan &amp; usage</>`, `license: 'License'`, `width="measure"`, target `/admin/subscription`.

`tenant-settings.tsx` — its root **is** the `<Tabs>` (no wrapper div). Title `Tenant settings`, description `Feature flags and upload policy.`, items `flags: 'Feature flags'`, `upload: 'Upload policy'`, `width="measure"`, target `/admin/tenant-settings`.

`data-governance.tsx` — title `Data governance`, description `Compliance overview and residency migrations.`, items `compliance: 'Compliance'`, `residency: 'Residency'`, `certification: 'Records certification'`, `width="full"`, target `/admin/data-governance`. The `CertificationDashboard` import stays.

- [ ] **Step 3: Convert the fourteen children** with the Task 5 Step 3 transformation:

| File | Old root | `className` | Notes |
|---|---|---|---|
| `tenant/classification.tsx` | `mx-auto max-w-4xl p-6` | — | centred today |
| `tenant/watermark.tsx` | `mx-auto max-w-4xl p-6` | — | centred today |
| `tenant/irm.tsx` | `mx-auto max-w-4xl p-6` | — | centred today |
| `records.tsx` | `mx-auto max-w-5xl p-6` | — | |
| `retention.tsx` | `space-y-6` (L134) | `gap-6` | actions moved verbatim; the earlier `return (` at L75 is inside a hook, not JSX |
| `legal-holds.tsx` | `space-y-6` | `gap-6` | |
| `ediscovery.tsx` | `mx-auto max-w-4xl p-6` | — | centred today |
| `audit-log.tsx` | `space-y-6` | `gap-6` | actions (`audit-export`) moved verbatim |
| `siem.tsx` | `mx-auto max-w-4xl p-6` | — | centred today |
| `tenant/license.tsx` | `mx-auto max-w-5xl p-6` | — | centred today |
| `settings.tsx` | `space-y-6` | `gap-6` | actions (`save-tenant-settings`) moved verbatim |
| `tenant/upload-policy.tsx` | `space-y-6` | `gap-6` | actions moved verbatim |
| `compliance.tsx` | `<div>` (L20; header L23 `title="Compliance" description="Data residency, encryption, and retention overview"`, no actions) | — | exports `CompliancePage`; the spinner/error branches below the header are untouched |
| `residency.tsx` | `<div>` | — | |

- [ ] **Step 4: Verify invariants** — Task 5 Step 4 loop over the 21 files. Expected: all `testids unchanged`, markup-only diff.

- [ ] **Step 5: Gates**

Run: `npx tsc --noEmit && npm run lint && npx vitest run src/routes/_authenticated/admin`
Run: `npm run build && npx playwright test 70-a11y 72-rtl-geometry --reporter=list`
Expected: all pass.

- [ ] **Step 6: Look at it**

`/admin/protection` (each tab), `/admin/audit?tab=forwarding`, `/admin/subscription?tab=license`, `/admin/tenant-settings`. Expect: one `h1` per page, tabs under it, the previously centred pages now start-aligned at 896px, the feature-flag rows with their checkbox ~840px from the label instead of 1400.

- [ ] **Step 7: Commit**

```bash
git add src/routes/_authenticated/admin
git commit -m "feat(web): seven merged admin pages on the frame; six centred children uncentred"
```

---

### Task 7: AI, Tagging, OCR, PII scanning + their nine children

These four containers already render a `PageHeader` above their tabs; its values move into `AdminPage`. Their children already use `PageHeader variant="section"` and bare `max-w-*` roots.

**Files:**
- Modify containers: `ai.tsx:22-34`, `tagging.tsx:16-27`, `ocr.tsx:17-27`, `pii-scanning.tsx:16-26`
- Modify children: `tenant/ai.tsx` (returns at L114 loading, L148 main; header L172), `intelligence/ner-config.tsx` (returns L73 loading, L90 main; headers L75, L92), `intelligence/models.tsx`, `intelligence/usage.tsx`, `tags.tsx`, `intelligence/auto-tag.tsx`, `intelligence/tag-review.tsx`, `intelligence/ocr-config.tsx` (returns L78, L98, L115, L170; headers L80, L100, L117, L172), `intelligence/ocr-review.tsx`, `intelligence/compliance-config.tsx`, `intelligence/compliance.tsx`
- Test: `e2e/38-llm-admin.spec.ts`

- [ ] **Step 1: Record invariants** — Task 5 Step 1 loop over the 15 files.

- [ ] **Step 2: Rewrite each container's render** — as Task 6 Step 2, with the container's existing header values:

`ai.tsx` — title `AI & Models`, description `Provider keys, NER tier, model registry, and usage & cost.`, items `provider: <>Provider &amp; keys</>`, `ner: 'NER tier'`, `models: 'Model registry'`, `usage: <>Usage &amp; cost</>`, `width={active === 'models' || active === 'usage' ? 'full' : 'measure'}`, target `/admin/ai`. Delete the now-redundant `<PageHeader …/>` line and its import.

`tagging.tsx` — title `Tagging`, description `Tag catalog, auto-tag thresholds, and the suggestion review queue.`, items `catalog: 'Catalog'`, `thresholds: 'Thresholds'`, `review: 'Review queue'`, `width={active === 'catalog' ? 'full' : 'measure'}`, target `/admin/tagging`.

`ocr.tsx` — title `OCR`, description `Quality thresholds and the review queue for low-confidence scans.`, items `config: 'Quality config'`, `review: 'Review queue'`, `width={active === 'review' ? 'full' : 'measure'}`, target `/admin/ocr`.

`pii-scanning.tsx` — title `PII / PHI scanning`, description `Findings across the tenant and the detection rules that produce them.`, items `findings: 'Findings'`, `config: 'Detection rules'`, `width={active === 'findings' ? 'full' : 'measure'}`, target `/admin/pii-scanning`.

- [ ] **Step 3: Convert the children.** Same transformation. These already use `variant="section"`, so the accessible heading level is unchanged. **Every** `return (` that renders a `<PageHeader` is converted — loading and error branches included:

| File | Roots to convert | `className` |
|---|---|---|
| `tenant/ai.tsx` | L148 `mx-auto max-w-3xl` (main; header L172). L114 is a spinner with no header — leave it | — |
| `intelligence/ner-config.tsx` | L73 and L90, both `max-w-3xl` | — |
| `intelligence/models.tsx` | `max-w-6xl` | — |
| `intelligence/usage.tsx` | `space-y-6` | `gap-6` |
| `tags.tsx` | `space-y-6` | `gap-6` |
| `intelligence/auto-tag.tsx` | `max-w-3xl` | — |
| `intelligence/tag-review.tsx` | `max-w-5xl`; `description` is an expression — move it verbatim | — |
| `intelligence/ocr-config.tsx` | L78, L98, L115, L170 — all `max-w-3xl` | — |
| `intelligence/ocr-review.tsx` | `max-w-6xl` | — |
| `intelligence/compliance-config.tsx` | `max-w-3xl` | — |
| `intelligence/compliance.tsx` | `max-w-6xl` | — |

- [ ] **Step 4: Verify invariants** — loop over the 15 files; markup-only diff.

- [ ] **Step 5: Gates**

Run: `npx tsc --noEmit && npm run lint && npx vitest run src/routes/_authenticated/admin`
Run: `npm run build && npx playwright test 38-llm-admin 70-a11y --reporter=list`

- [ ] **Step 6: Look at it** — `/admin/ai` (each tab). The provider form, 431px wide today with 1197px of canvas beside it, now sits in the 896px measure.

- [ ] **Step 7: Commit**

```bash
git add src/routes/_authenticated/admin
git commit -m "feat(web): intelligence admin pages on the frame"
```

---

### Task 8: Integrations + its six children, and the hand-rolled tabs

**Files:**
- Modify: `integrations/index.tsx:80-98` (frame), `integrations/index.tsx:156` and the block starting 8 lines above `tab === 'connections'` (underline tabs)
- Modify children: `connectors.tsx`, `webhooks.tsx`, `integrations/email.tsx`, `integrations/events.tsx`, `integrations/mcp.tsx`, `integrations/ipaas.tsx` — all `space-y-6` roots → `className="gap-6"`, actions moved verbatim where present
- Test: `e2e/53-esign-connectors.spec.ts`, `e2e/56-customer-webhooks.spec.ts`, `e2e/57-event-streaming.spec.ts`, `e2e/58-email-ingestion.spec.ts`

**Interfaces:** the top-tab `data-testid`s (`top-tab-esign` … `top-tab-ipaas`) and the sub-tab ones (`tab-connections`, `tab-envelopes`, `tab-notifications`, `tab-connectors`) are invariants the four specs click.

- [ ] **Step 1: Record invariants** over the 7 files.

- [ ] **Step 2: Frame the container**

Replace lines 80–98 (root `<div className="mx-auto w-full max-w-5xl p-6">`, `<PageHeader …/>`, `<Tabs …>`, `<TabsList>…</TabsList>`) with:

```tsx
  return (
    <AdminPage
      title="Integrations"
      description="Third-party providers, outbound webhooks, inbound email + event streams, and MCP keys for this tenant."
      tabs={{
        value: active,
        onValueChange: (v) => navigate({ to: '/admin/integrations', search: { tab: v as TopTab } }),
        items: [
          { value: 'esign', label: 'eSignature', testId: 'top-tab-esign' },
          { value: 'connectors', label: 'Connectors', testId: 'top-tab-connectors' },
          { value: 'webhooks', label: 'Webhooks', testId: 'top-tab-webhooks' },
          { value: 'email', label: 'Email ingestion', testId: 'top-tab-email' },
          { value: 'events', label: 'Event streaming', testId: 'top-tab-events' },
          { value: 'mcp', label: 'MCP', testId: 'top-tab-mcp' },
          { value: 'ipaas', label: 'iPaaS', testId: 'top-tab-ipaas' },
        ],
      }}
      width={active === 'esign' || active === 'connectors' || active === 'webhooks' ? 'full' : 'measure'}
    >
```
Keep the existing `<TabsContent>` children (drop `className="mt-4"` if present); close with `</AdminPage>`. The `esign_error` handling and the `replace: true` navigate at L70–77 are untouched.

- [ ] **Step 3: Replace the underline tabs with the canonical primitive**

The eSignature panel keeps its local `const [tab, setTab] = useState<'connections' | 'envelopes' | 'notifications' | 'connectors'>('connections')` (L156). Replace the `<div className="mb-4 flex gap-2 border-b border-border">…four <button>s…</div>` block with:

```tsx
      <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)}>
        <TabsList className="mb-4 w-fit">
          <TabsTrigger value="connections" data-testid="tab-connections">Connections</TabsTrigger>
          <TabsTrigger value="envelopes" data-testid="tab-envelopes">In-progress envelopes</TabsTrigger>
          <TabsTrigger value="notifications" data-testid="tab-notifications">Notifications</TabsTrigger>
          <TabsTrigger value="connectors" data-testid="tab-connectors">Workspace connectors</TabsTrigger>
        </TabsList>
      </Tabs>
```
The `{tab === 'connections' && (<section …>)}` blocks that follow stay exactly as they are — the state and the conditionals are unchanged; only the buttons were replaced. Keep `Tabs, TabsList, TabsTrigger, TabsContent` in the import (this file now needs all four).

- [ ] **Step 4: Convert the six children** — `space-y-6` → `AdminSection … className="gap-6"`, headers' `title`/`description`/`actions` moved verbatim. Titles are `Connectors`, `Webhooks`, `Email ingestion`, `Event streaming`, `MCP server (LLM agents)`, `iPaaS integrations` — three of these are asserted by name in e2e; they must not change.

- [ ] **Step 5: Verify invariants** — loop over the 7 files; markup-only diff; explicitly:

```bash
grep -c 'data-testid="top-tab-' src/routes/_authenticated/admin/integrations/index.tsx   # expect 7
grep -c 'data-testid="tab-' src/routes/_authenticated/admin/integrations/index.tsx       # expect 4
```

- [ ] **Step 6: Gates**

Run: `npx tsc --noEmit && npm run lint`
Run: `npm run build && npx playwright test 53-esign-connectors 56-customer-webhooks 57-event-streaming 58-email-ingestion 70-a11y --reporter=list`
Expected: all pass — the three heading-name assertions still find their `h2`s.

- [ ] **Step 7: Commit**

```bash
git add src/routes/_authenticated/admin
git commit -m "feat(web): Integrations on the frame; hand-rolled underline tabs replaced by the primitive"
```

---

### Task 9: Eleven standalone full-width pages

**Files:**
- Modify: `api-keys.tsx`, `share-links.tsx` (main return L107), `workflows.tsx`, `metadata-schema.tsx`, `ingestion.tsx`, `permission-lag.tsx` (returns L27 spinner — leave; L38 main), `tenant/sync.tsx`, `platform/db-info.tsx`, `intelligence/anomalies.tsx`, `intelligence/routing-rules.tsx`, `intelligence/filing-analytics.tsx` (returns L45, L58, L70 — all three render a header; convert all three)

**Interfaces:** Consumes `AdminPage` with `width="full"`.

- [ ] **Step 1: Record invariants** over the 11 files.

- [ ] **Step 2: Wrap each page**

The transformation for a standalone page:

1. Delete the `<PageHeader … />` element, keeping its `title`, `description`, `actions` values.
2. Replace the component's root opening tag with `<AdminPage title={…} description={…} actions={…} width="full">` and its closing tag with `</AdminPage>`. The frame's content column already spaces siblings by `gap-6`, so a `space-y-6` root needs no replacement; `mx-auto max-w-* p-6` and bare `max-w-*` roots are simply dropped.
3. Swap the `PageHeader` import for `import { AdminPage } from '@/components/admin/AdminPage'`.

| File | Old root | Notes |
|---|---|---|
| `api-keys.tsx` | `space-y-6` | actions (`new-api-key`) verbatim |
| `share-links.tsx` | `space-y-6` (L107) | |
| `workflows.tsx` | `space-y-6` | `title={t('admin.title')}` `description={t('admin.description')}` — pass the expressions verbatim |
| `metadata-schema.tsx` | `flex min-h-0 flex-1 flex-col gap-6` | **Keep** this div as the sole child of `AdminPage`; it needs `flex-1` for the builder's height |
| `ingestion.tsx` | `mx-auto max-w-6xl p-6` | centred today |
| `permission-lag.tsx` | `space-y-6` (L38) | |
| `tenant/sync.tsx` | `mx-auto max-w-4xl p-6` | centred today |
| `platform/db-info.tsx` | `mx-auto max-w-5xl p-6` | centred today |
| `intelligence/anomalies.tsx` | `max-w-6xl` | actions verbatim |
| `intelligence/routing-rules.tsx` | `max-w-5xl` | actions verbatim |
| `intelligence/filing-analytics.tsx` | `space-y-6` ×3 (L45, L58, L70) | convert all three returns |

- [ ] **Step 3: Verify invariants** — loop; markup-only diff.

- [ ] **Step 4: Gates**

Run: `npx tsc --noEmit && npm run lint && npx vitest run src/routes/_authenticated/admin`
Run: `npm run build && npx playwright test 70-a11y 72-rtl-geometry --reporter=list`

- [ ] **Step 5: Look at it** — `/admin/metadata-schema` (builder still fills the height), `/admin/tenant/sync` and `/admin/platform/db-info` (no longer centred).

- [ ] **Step 6: Commit**

```bash
git add src/routes/_authenticated/admin
git commit -m "feat(web): eleven standalone admin list pages on the frame at full width"
```

---

### Task 10: Seven standalone measured pages

**Files:**
- Modify: `bulk.tsx`, `capture.tsx` (L120; no `PageHeader` — its `<header><h1>Scan capture</h1><p>…</p></header>` at L122–127 becomes the `AdminPage` props), `privacy.tsx`, `mfa-policy.tsx` (returns L64 and L89, headers L66 and L91 — convert both), `tenant/encryption.tsx`, `platform/load-tests.tsx`, `platform/support-search.tsx`
- Test: `e2e/42-federated-search.spec.ts` (support search)

- [ ] **Step 1: Record invariants** over the 7 files.

- [ ] **Step 2: Wrap each page** with the Task 9 Step 2 transformation and `width="measure"`:

| File | Old root | Notes |
|---|---|---|
| `bulk.tsx` | `space-y-6` | |
| `capture.tsx` | `mx-auto max-w-5xl space-y-6 p-6` | delete the `<header>…</header>` block; `title="Scan capture"`, `description="Upload a multi-page scan (PDF/TIFF). Pages are split into separate documents at barcode / patch-code boundaries — review and correct before committing."` |
| `privacy.tsx` | `<div>` | |
| `mfa-policy.tsx` | `<div>` (L64) and `space-y-4` (L89) | both returns |
| `tenant/encryption.tsx` | `mx-auto max-w-4xl p-6` | centred today; its four internal `<section>` cards stay as they are |
| `platform/load-tests.tsx` | `mx-auto max-w-7xl p-6` | centred today |
| `platform/support-search.tsx` | `mx-auto max-w-4xl` | centred today |

- [ ] **Step 3: Verify invariants** — loop; markup-only diff.

- [ ] **Step 4: Gates**

Run: `npx tsc --noEmit && npm run lint && npx vitest run src/routes/_authenticated/admin`
Run: `npm run build && npx playwright test 42-federated-search 70-a11y 72-rtl-geometry --reporter=list`

- [ ] **Step 5: Look at it** — `/admin/tenant/encryption` at 1920: 896px, starting at the 32px gutter, not centred.

- [ ] **Step 6: Commit**

```bash
git add src/routes/_authenticated/admin
git commit -m "feat(web): seven standalone admin form pages on the frame in the measure"
```

---

### Task 11: The hub and the Platform hub

**Files:**
- Modify: `web/src/routes/_authenticated/admin/index.tsx` (full render rewrite; the four `*_GROUP` constants, `slugify`, role gating stay)
- Modify: `web/src/routes/_authenticated/admin/platform/index.tsx`
- Test: `e2e/70-a11y.spec.ts` (`admin landing`), `e2e/72-rtl-geometry.spec.ts` (`/admin` is already in `ROUTES`)

**Interfaces:** Consumes `AdminPage`, `DirectoryGroup`, `DirectorySub`, `DirectoryList`, `DirectoryRow`, `matchesQuery` (Task 3).

- [ ] **Step 1: Record invariants** over both files, and the role-gating lines:

```bash
grep -n "adminPathAllowsComplianceOfficer\|is_platform_admin\|isComplianceOfficer" src/routes/_authenticated/admin/index.tsx > /tmp/hub-gating-before.txt
```

- [ ] **Step 2: Rewrite `index.tsx` from `function AdminPage()` to the end** (the data constants above it are untouched; rename nothing in them):

```tsx
function AdminHubPage() {
  const user = useAuthStore((s) => s.user)
  const isPlatformAdmin = user?.is_platform_admin === true
  const isComplianceOfficer = user?.role === 'compliance_officer' && !isPlatformAdmin
  const [q, setQ] = useState('')

  let groups = [TENANT_GROUP, INTEGRATIONS_GROUP, INTEL_GROUP, ...(isPlatformAdmin ? [PLATFORM_GROUP] : [])]

  // A compliance officer only reaches the hub for the intelligence
  // pages the backend authorizes them to read (see the route guard's
  // COMPLIANCE_OFFICER_ADMIN_PATHS) — show exactly those rows so the
  // hub never advertises a destination that would bounce them.
  if (isComplianceOfficer) {
    groups = [
      {
        ...INTEL_GROUP,
        sections: (INTEL_GROUP.sections ?? []).filter((sec) =>
          adminPathAllowsComplianceOfficer(sec.to),
        ),
      },
    ]
  }

  // Filter is a plain substring over label + description (matchesQuery);
  // a group with nothing left collapses. Presentation only — no fetch.
  const visible = groups
    .map((g) => ({
      ...g,
      sections: g.sections?.filter((s) => matchesQuery(s, q)),
      subgroups: g.subgroups
        ?.map((sg) => ({ ...sg, sections: sg.sections.filter((s) => matchesQuery(s, q)) }))
        .filter((sg) => sg.sections.length > 0),
    }))
    .filter((g) => (g.sections?.length ?? 0) > 0 || (g.subgroups?.length ?? 0) > 0)

  const [tenant, ...rest] = visible

  return (
    <AdminPage
      title="Administration"
      description={
        isComplianceOfficer
          ? 'Intelligence oversight — read-only access to scanning, tagging, and anomaly surfaces.'
          : 'Manage tenant-wide policy, people, and intelligence settings.'
      }
      actions={
        <input
          type="search"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Find a setting…"
          aria-label="Find a setting"
          data-testid="admin-hub-filter"
          className="h-9 w-64 rounded-md border border-border bg-background px-3 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        />
      }
      width="full"
    >
      {visible.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="admin-hub-empty">
          No settings match “{q}”.
        </p>
      ) : (
        <div className="grid gap-6 lg:grid-cols-2 lg:items-start">
          {tenant && <GroupBlock group={tenant} />}
          <div className="flex flex-col gap-6">
            {rest.map((g) => (
              <GroupBlock key={g.label} group={g} />
            ))}
          </div>
        </div>
      )}
    </AdminPage>
  )
}

function GroupBlock({ group }: { group: SectionGroup }) {
  const id = `group-${slugify(group.label)}`
  return (
    <DirectoryGroup id={id} label={group.label} description={group.description}>
      {group.subgroups ? (
        group.subgroups.map((sg) => (
          <DirectorySub key={sg.label} label={sg.label}>
            {sg.sections.map((s) => (
              <DirectoryRow key={s.to} to={s.to} icon={s.icon} label={s.label} desc={s.desc} />
            ))}
          </DirectorySub>
        ))
      ) : (
        <DirectoryList>
          {(group.sections ?? []).map((s) => (
            <DirectoryRow key={s.to} to={s.to} icon={s.icon} label={s.label} desc={s.desc} />
          ))}
        </DirectoryList>
      )}
    </DirectoryGroup>
  )
}

function slugify(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

export const Route = createFileRoute('/_authenticated/admin/')({ component: AdminHubPage })
```

Imports at the top of the file become:

```tsx
import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { Activity, AlertTriangle, Brain, CreditCard, Database, FileJson, FileSearch, FolderSync, FolderTree, KeyRound, LayoutTemplate, Link2, Lock, MapPinned, Plug, Scale, ScrollText, Settings, Shield, ShieldAlert, ShieldCheck, Tags as TagsIcon, Upload, UserCog, Users, Workflow, type LucideIcon } from 'lucide-react'
import { AdminPage } from '@/components/admin/AdminPage'
import { DirectoryGroup, DirectorySub, DirectoryList, DirectoryRow, matchesQuery } from '@/components/admin/DirectoryGroup'
import { useAuthStore } from '@/store/authStore'
import { adminPathAllowsComplianceOfficer } from '@/routes/_authenticated'
```
(`Link`, `PageHeader`, `Card`, `cn`, `DirectionalIcon` are no longer used here; `SectionCard` and `SectionGrid` are deleted.) The component is renamed from `AdminPage` to `AdminHubPage` because `AdminPage` is now the frame's name; the route registration is the only reference.

**Optional filter:** if the filter box is judged to be more than design, delete `useState`, `q`, `setQ`, the `actions` prop, the `visible` computation (use `groups` directly) and the empty-state branch. Nothing else depends on it.

- [ ] **Step 3: Rewrite `platform/index.tsx`** — from `function PlatformHubPage()` to the end:

```tsx
function PlatformHubPage() {
  return (
    <AdminPage
      title="Platform administration"
      description="Cross-tenant tools — every action is audited."
      width="measure"
    >
      <DirectoryGroup id="group-platform" label="Platform administration">
        <DirectoryList>
          {SECTIONS.map((s) => (
            <DirectoryRow key={s.to} to={s.to} icon={s.icon} label={s.label} desc={s.desc} />
          ))}
        </DirectoryList>
      </DirectoryGroup>
    </AdminPage>
  )
}

export const Route = createFileRoute('/_authenticated/admin/platform/')({
  beforeLoad: requirePlatformBuild,
  component: PlatformHubPage,
})
```
Imports: drop `Link`, `PageHeader`, `Card`, `cn`, `DirectionalIcon`; add `AdminPage` and `DirectoryGroup, DirectoryList, DirectoryRow`. `SECTIONS` and `requirePlatformBuild` stay.

- [ ] **Step 4: Verify the gating survived**

```bash
grep -n "adminPathAllowsComplianceOfficer\|is_platform_admin\|isComplianceOfficer" src/routes/_authenticated/admin/index.tsx > /tmp/hub-gating-after.txt
diff <(sed 's/^[0-9]*://' /tmp/hub-gating-before.txt) <(sed 's/^[0-9]*://' /tmp/hub-gating-after.txt) && echo "gating lines unchanged"
grep -c "to: '/" src/routes/_authenticated/admin/index.tsx   # expect 30 — every destination kept
```
Then sign in as the seeded admin and confirm the Platform group appears only when `is_platform_admin` is true (check `/api/v1/auth/me`), and that typing `siem` in the filter leaves exactly one row (Audit & SIEM) and typing `zzz` shows the empty message.

- [ ] **Step 5: Gates**

Run: `npx tsc --noEmit && npm run lint && npx vitest run`
Run: `npm run build && npx playwright test 70-a11y 72-rtl-geometry --reporter=list`
Expected: all pass; `admin landing` scan clean.

- [ ] **Step 6: Look at it** — `/admin` at 1920×1000: every one of the 30 rows visible without scrolling; two columns; no blue tiles.

- [ ] **Step 7: Commit**

```bash
git add src/routes/_authenticated/admin/index.tsx src/routes/_authenticated/admin/platform/index.tsx
git commit -m "feat(web): admin hub as a directory — all 30 entries above the fold, filterable"
```

---

### Task 12: Flip the guard to failing, and prove it can fail

**Files:**
- Modify: `web/package.json` (`lint:admin`)
- Test: the guard itself

- [ ] **Step 1: Confirm zero violations remain**

Run: `node scripts/check-admin-frame.mjs; echo "exit $?"`
Expected: `check-admin-frame: ✓ every admin route is on the frame` and `exit 0`. If anything is listed, it is a page missed in Tasks 5–11 — migrate it with that task's transformation before continuing.

- [ ] **Step 2: Remove `--report`**

```json
    "lint:admin": "node scripts/check-admin-frame.mjs",
```

- [ ] **Step 3: Prove the guard fails**

```bash
# Reintroduce the bug class in one migrated page.
sed -i 's|<AdminSection title="Users"|<PageHeader title="Users" /><AdminSection title="Users"|' src/routes/_authenticated/admin/users.tsx
npm run lint:admin; echo "exit $?"
```
Expected: `admin/users.tsx:<n>  renders <PageHeader>; use AdminPage or AdminSection` and `exit 1`.

```bash
git checkout -- src/routes/_authenticated/admin/users.tsx
npm run lint; echo "exit $?"
```
Expected: `exit 0`.

- [ ] **Step 4: Commit**

```bash
git add package.json
git commit -m "chore(web): lint:admin now fails the build — every admin route is on the frame"
```

---

### Task 13: Extend the gates

**Files:**
- Modify: `web/e2e/72-rtl-geometry.spec.ts:128`
- Modify: `web/e2e/70-a11y.spec.ts` (two tests, after `admin users table` at L356 and `dark: admin users table` at L460)

- [ ] **Step 1: RTL geometry routes**

Replace line 128 with:

```ts
const ROUTES = ['/', '/search', `/workspaces/${WS}`, '/trash', '/tasks', '/admin', '/notifications', '/settings', '/admin/identity', '/admin/tenant-settings', '/admin/tenant/encryption', '/admin/integrations', '/admin/users']
```

- [ ] **Step 2: a11y scans for the frame with two tab rows**

After the `admin users table` test (light block):

```ts
  test('admin identity (two tab rows, users table)', async ({ page }) => {
    await mockCore(page)
    await mockUsers(page)
    await page.goto('/admin/identity')
    await expect(page.getByText('alice@example.com')).toBeVisible()
    await expect(page.getByRole('heading', { level: 1, name: 'Identity & Access' })).toBeVisible()
    await expect(page.getByRole('tablist')).toHaveCount(2)
    await scan(page, '/admin/identity')
  })
```

After `dark: admin users table` (dark block):

```ts
  test('dark: admin identity', async ({ page }) => {
    await mockCore(page)
    await mockUsers(page)
    await page.goto('/admin/identity')
    await expect(page.getByText('alice@example.com')).toBeVisible()
    await forceDark(page)
    await scan(page, 'dark /admin/identity')
  })
```

- [ ] **Step 3: Run both gates**

Run: `npm run build && npx playwright test 70-a11y 72-rtl-geometry --reporter=list`
Expected: 33 → 35 a11y/RTL tests passing; zero RTL-only findings on the five new routes.

- [ ] **Step 4: Full regression net**

Run: `npx playwright test 38-llm-admin 42-federated-search 44-ldap-admin 53-esign-connectors 54-pades-validity-badge 56-customer-webhooks 57-event-streaming 58-email-ingestion 70-a11y 72-rtl-geometry --reporter=list`
Expected: all pass. (The four pre-existing `i18n-rtl-*` failures are unrelated and already red on this branch — see spec §9.)

- [ ] **Step 5: Look at everything once, in Arabic too**

Switch the UI to Arabic via the topbar selector and open `/admin`, `/admin/identity?group=auth&sub=scim`, `/admin/tenant-settings`, `/admin/tenant/encryption`, `/admin/integrations`. Expect: tab rows and chevrons mirrored, English descriptions keeping their full stop at the end (the `dir="auto"` on `DirectoryRow` and on `PageHeader`), no content off the start edge. Switch back to English.

- [ ] **Step 6: Commit**

```bash
git add e2e/72-rtl-geometry.spec.ts e2e/70-a11y.spec.ts
git commit -m "test(web): admin frame pages join the RTL geometry and a11y gates"
```

---

## Done when

- `npm run lint` is green with `lint:admin` in failing mode.
- `node scripts/check-admin-frame.mjs` reports ✓.
- The nine admin e2e specs, `70-a11y` and `72-rtl-geometry` pass on the built bundle.
- `/admin` shows all 30 entries at 1920×1000 without scrolling.
- No admin page is centred: the ink extent of every page's cards starts at the shell's 32px gutter.
