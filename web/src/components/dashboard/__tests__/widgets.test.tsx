import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, renderHook, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider, onlineManager } from '@tanstack/react-query'
import { WidgetCard } from '../WidgetCard'
import { ChartDataTable } from '../ChartDataTable'
import { FolderOpen } from 'lucide-react'
import { KpiCard } from '../KpiCard'
import { DocumentsAddedChart } from '../DocumentsAddedChart'
import * as metricsHook from '../useDashboardMetrics'
import { KpiStrip } from '../KpiStrip'
import { BreakdownBars } from '../BreakdownBars'
import { LibraryBreakdown } from '../LibraryBreakdown'
import { AttentionList } from '../AttentionList'
import { renderWithProviders } from '@/test/renderWithProviders'
import { getWorkspaces } from '@/api/workspaces'
import { listMyTasks } from '@/api/tasks'
import { getNotifications, getUnreadCount } from '@/api/notifications'
import { search } from '@/api/search'
import type { SearchResult, Workspace } from '@/types/api'
import { useUIStore } from '@/store/uiStore'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, search, ...rest }:
    { children: React.ReactNode; to?: string; search?: Record<string, string> } & Record<string, unknown>) =>
    <a href={search ? `${to}?${new URLSearchParams(search).toString()}` : to} {...rest}>{children}</a>,
  useNavigate: () => vi.fn(),
}))

// fix round 1 — module-boundary mocks so KpiStrip's three queries (plus
// the metrics facet query, spied via metricsHook below) can be driven
// into pending/error/success independently, the way ux-error-states.test.tsx
// mocks '@/api/tasks'.
vi.mock('@/api/workspaces', () => ({ getWorkspaces: vi.fn() }))
vi.mock('@/api/tasks', () => ({
  taskKeys: { mine: () => ['tasks', 'mine'] },
  listMyTasks: vi.fn(),
}))
// getNotifications feeds AttentionList's unread-notifications row (R24). The
// default resolves an empty inbox so tests that don't care stay green;
// markAsRead is stubbed so the module still exports everything it imports.
vi.mock('@/api/notifications', () => ({
  getUnreadCount: vi.fn(),
  getNotifications: vi.fn(() => Promise.resolve({ items: [], total_count: 0 })),
  markAsRead: vi.fn(),
}))
// The facet query behind useDashboardMetrics. Only the offline test
// uses the real hook; every other test spies the hook itself.
vi.mock('@/api/search', () => ({ search: vi.fn() }))

describe('WidgetCard', () => {
  it('shows the failure state with Retry, and never the empty copy, when a query failed', async () => {
    const onRetry = vi.fn()
    render(
      <WidgetCard title="Lifecycle" isError isEmpty emptyLabel="Nothing here yet" onRetry={onRetry}>
        <p>rows</p>
      </WidgetCard>,
    )
    expect(screen.queryByText('Nothing here yet')).not.toBeInTheDocument()
    expect(screen.queryByText('rows')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('prefers the failure state over the loading state', () => {
    render(<WidgetCard title="Lifecycle" isLoading isError onRetry={vi.fn()}><p>rows</p></WidgetCard>)
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })

  it('renders children only when the query succeeded and is non-empty', () => {
    render(<WidgetCard title="Lifecycle"><p>rows</p></WidgetCard>)
    expect(screen.getByText('rows')).toBeInTheDocument()
  })

  it('labels its region with the title so the page has a navigable outline', () => {
    render(<WidgetCard title="File types"><p>rows</p></WidgetCard>)
    expect(screen.getByRole('region', { name: 'File types' })).toBeInTheDocument()
  })

  it('is the design-system card frame: rounded-xl, 1px border, flat sm shadow, no neumorphic or entrance classes', () => {
    render(<WidgetCard title="File types" delayIndex={3}><p>rows</p></WidgetCard>)
    const card = screen.getByRole('region', { name: 'File types' })
    for (const cls of ['rounded-xl', 'border', 'border-border', 'bg-card', 'shadow-sm']) {
      expect(card).toHaveClass(cls)
    }
    expect(card.className).not.toMatch(/shadow-neu/)
    expect(card).not.toHaveClass('dash-rise')
    // delayIndex is accepted but no longer drives an entrance animation.
    expect(card.style.getPropertyValue('--dash-delay')).toBe('')
  })

  it('sets the card title at 16/20 semibold with -0.01em tracking (spec §3)', () => {
    render(<WidgetCard title="File types"><p>rows</p></WidgetCard>)
    const title = screen.getByRole('heading', { level: 2, name: 'File types' })
    expect(title).toHaveClass('text-base', 'leading-5', 'font-semibold', 'tracking-[-0.01em]')
  })

  describe('icon slot', () => {
    const header = (name: string) => screen.getByRole('region', { name }).querySelector('header') as HTMLElement

    it('adds no wrapper at all when no icon is supplied', () => {
      render(<WidgetCard title="File types" action={<button>act</button>}><p>rows</p></WidgetCard>)
      const h = header('File types')
      // Exactly the original two children: the title block, then the action.
      expect(h.children).toHaveLength(2)
      expect(h.firstElementChild).toHaveClass('min-w-0')
      expect(h.firstElementChild?.firstElementChild?.tagName).toBe('H2')
      expect(h.lastElementChild).toHaveTextContent('act')
    })

    it('renders the icon at the start edge, before the title, and keeps the action last', () => {
      render(
        <WidgetCard title="Ask" icon={<span data-testid="tile" />} action={<button>act</button>}>
          <p>rows</p>
        </WidgetCard>,
      )
      const h = header('Ask')
      const title = screen.getByRole('heading', { level: 2, name: 'Ask' })
      expect(screen.getByTestId('tile').compareDocumentPosition(title) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      expect(h.contains(screen.getByTestId('tile'))).toBe(true)
      // Icon + title share one flex item so `justify-between` still parks the action at the end.
      expect(h.children).toHaveLength(2)
      expect(h.lastElementChild).toHaveTextContent('act')
    })

    it('keeps the icon when the card is loading, and still hides nothing but the subtitle text', () => {
      render(<WidgetCard title="Ask" subtitle="sub" icon={<span data-testid="tile" />} isLoading><p>rows</p></WidgetCard>)
      expect(screen.getByTestId('tile')).toBeInTheDocument()
      expect(screen.queryByText('sub')).not.toBeInTheDocument()
    })
  })

  it('renders the empty state flat, without an inset shadow', () => {
    render(<WidgetCard title="File types" isEmpty emptyLabel="Nothing here yet"><p>rows</p></WidgetCard>)
    const empty = screen.getByText('Nothing here yet').parentElement as HTMLElement
    expect(empty).toHaveClass('rounded-lg', 'bg-muted')
    expect(empty.className).not.toMatch(/shadow-neu/)
  })
})

describe('ChartDataTable', () => {
  it('exposes the chart numbers to assistive tech', () => {
    render(
      <ChartDataTable
        caption="Documents added per month"
        columns={['Month', 'Documents']}
        rows={[{ key: 'jan', label: 'Jan', value: 12 }]}
      />,
    )
    const table = screen.getByRole('table', { name: 'Documents added per month' })
    expect(table).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: 'Jan' })).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: '12' })).toBeInTheDocument()
  })
})

describe('KpiCard', () => {
  it('shows a skeleton instead of a zero while the value is still undefined', () => {
    const { container } = render(
      <KpiCard icon={FolderOpen} label="Documents" value={undefined} href="/workspaces" />,
    )
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(container.querySelector('[data-testid="kpi-skeleton"]')).not.toBeNull()
  })

  it('names the tile for assistive tech as label plus value', () => {
    render(<KpiCard icon={FolderOpen} label="Documents" value={42} href="/workspaces" />)
    expect(screen.getByRole('link', { name: /Documents: 42/ })).toBeInTheDocument()
  })
})

describe('WidgetCard — loading a11y (fix round 1)', () => {
  it('marks the region busy and announces loading via a status role while isLoading', () => {
    render(<WidgetCard title="Lifecycle" isLoading><p>rows</p></WidgetCard>)
    const region = screen.getByRole('region', { name: 'Lifecycle' })
    expect(region).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByRole('status')).toHaveTextContent(/loading lifecycle/i)
  })

  it('clears aria-busy once loaded', () => {
    render(<WidgetCard title="Lifecycle"><p>rows</p></WidgetCard>)
    const region = screen.getByRole('region', { name: 'Lifecycle' })
    expect(region).not.toHaveAttribute('aria-busy', 'true')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})

const emptyMetrics = {
  activity: [], lifecycle: [], fileTypes: [], fileTypesTotal: 0, contributors: [],
  isLoading: false, isError: false, isUnavailable: false, refetch: vi.fn(),
}

// ---------------------------------------------------------------------
// Fix round 1 (review): four defects found against commit 07397317.
//
// Critical A — KpiStrip built the "across N workspaces" hint from
// `workspaces.data` without checking `isLoading`, so a still-pending
// query rendered "across 0 workspaces" beneath the skeleton.
// Critical B — every tile gated its numeral on `isLoading` alone; in
// react-query v5, `isLoading` goes false the moment a fetch SETTLES,
// including into an error, so a failed query rendered a confident "0"
// and `aria-label` lied ("Documents: 0") to assistive tech.
// Important C — `aria-label` omitted the hint, so alert-tone info
// ("2 overdue") never reached assistive tech even on success.
// Important D — nothing mounted KpiStrip itself; every test drove
// KpiTile directly with hand-supplied props, which structurally cannot
// see Critical A (KpiStrip is what fabricates the hint).
// ---------------------------------------------------------------------

describe('KpiCard — fix round 1', () => {
  it('suppresses the hint while the value is still loading, even if the caller supplied one', () => {
    render(
      <KpiCard
        icon={FolderOpen}
        label="Documents"
        value={undefined}
        hint="across 0 workspaces"
        href="/workspaces"
      />,
    )
    expect(screen.queryByText('across 0 workspaces')).not.toBeInTheDocument()
  })

  it('renders a distinct failed state — not the loading skeleton, not a numeral — when isError is true', () => {
    const { container } = render(
      <KpiCard icon={FolderOpen} label="Documents" value={undefined} isError hint="unable to load" href="/workspaces" />,
    )
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(container.querySelector('[data-testid="kpi-skeleton"]')).toBeNull()
    expect(container.querySelector('[data-testid="kpi-failed"]')).not.toBeNull()
  })

  it('gives a failed tile an accessible name that says unavailable, never a number', () => {
    render(
      <KpiCard icon={FolderOpen} label="Documents" value={undefined} isError hint="unable to load" href="/workspaces" />,
    )
    expect(screen.getByRole('link', { name: 'Documents: unavailable' })).toBeInTheDocument()
  })

  it('forces the failed hint to the alert tone regardless of the hintTone the caller passed', () => {
    render(
      <KpiCard
        icon={FolderOpen}
        label="Documents"
        value={undefined}
        isError
        hint="unable to load"
        hintTone="muted"
        href="/workspaces"
      />,
    )
    expect(screen.getByText('unable to load')).toHaveClass('text-destructive')
  })

  it('includes the hint in the accessible name so alert-tone info reaches assistive tech', () => {
    render(
      <KpiCard icon={FolderOpen} label="Open tasks" value={2} hint="2 overdue" hintTone="alert" href="/tasks" />,
    )
    expect(screen.getByRole('link', { name: 'Open tasks: 2, 2 overdue' })).toBeInTheDocument()
  })
})

// The REAL wire type: `Workspace.document_count` is a proto3 int64, which
// protojson emits as a STRING ("4"), even though types/api.ts says
// `number`. A numeric fixture here masked a string-concatenated total
// ("0412" instead of 16) for the whole branch, so the fixtures carry
// strings and cast past the lying type.
function wireWorkspace(id: string, documentCount: string): Workspace {
  return {
    id, name: `Workspace ${id}`, document_count: documentCount as unknown as number,
    member_count: 1, created_at: '2026-01-01T00:00:00Z',
  }
}

const okWorkspace: Workspace = wireWorkspace('w1', '5')

describe('KpiStrip — fix round 1', () => {
  beforeEach(() => {
    vi.mocked(getWorkspaces).mockReset()
    vi.mocked(listMyTasks).mockReset()
    vi.mocked(getUnreadCount).mockReset()
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('while a query is pending, renders no fabricated hint text such as "across 0 workspaces" anywhere', () => {
    vi.mocked(getWorkspaces).mockReturnValue(new Promise<Workspace[]>(() => {}))
    vi.mocked(listMyTasks).mockReturnValue(new Promise(() => {}))
    vi.mocked(getUnreadCount).mockReturnValue(new Promise(() => {}))

    const { container } = renderWithProviders(<KpiStrip />)

    expect(screen.queryByText(/across \d+ workspaces/i)).not.toBeInTheDocument()
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(container.querySelectorAll('[data-testid="kpi-skeleton"]').length).toBeGreaterThan(0)
  })

  it('when one query errors (others succeed), that tile does not render 0 and its accessible name claims no number', async () => {
    vi.mocked(getWorkspaces).mockResolvedValue([okWorkspace])
    vi.mocked(listMyTasks).mockRejectedValue(new Error('boom'))
    vi.mocked(getUnreadCount).mockResolvedValue(3)

    renderWithProviders(<KpiStrip />)

    await waitFor(() => expect(screen.getByRole('link', { name: 'Open tasks: unavailable' })).toBeInTheDocument())
    expect(screen.getByRole('link', { name: 'Awaiting approval: unavailable' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Open tasks: 0/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Awaiting approval: 0/ })).not.toBeInTheDocument()
    // the queries that succeeded still show their real numbers
    expect(screen.getByRole('link', { name: /^Documents: 5/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^Unread: 3/ })).toBeInTheDocument()
  })

  it('when every query fails, shows one consolidated failure card with a working Retry instead of four tiles', async () => {
    vi.mocked(getWorkspaces).mockRejectedValue(new Error('boom'))
    vi.mocked(listMyTasks).mockRejectedValue(new Error('boom'))
    vi.mocked(getUnreadCount).mockRejectedValue(new Error('boom'))

    renderWithProviders(<KpiStrip />)

    const retry = await screen.findByRole('button', { name: /retry/i })
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Unread/ })).not.toBeInTheDocument()
    expect(screen.getByTestId('kpi-strip-error')).toHaveClass('rounded-xl', 'border', 'border-border', 'shadow-sm')
    expect(screen.getByTestId('kpi-strip-error').className).not.toMatch(/shadow-neu/)
    expect(screen.queryByText('0')).not.toBeInTheDocument()

    vi.mocked(getWorkspaces).mockResolvedValue([okWorkspace])
    vi.mocked(listMyTasks).mockResolvedValue([])
    vi.mocked(getUnreadCount).mockResolvedValue(0)
    await userEvent.click(retry)

    await waitFor(() => expect(screen.queryByTestId('kpi-strip-error')).not.toBeInTheDocument())
    expect(screen.getByRole('link', { name: /^Documents: 5/ })).toBeInTheDocument()
  })

  it('a genuine zero still renders as a real 0, distinguishable from the failed state', async () => {
    vi.mocked(getWorkspaces).mockResolvedValue([])
    vi.mocked(listMyTasks).mockResolvedValue([])
    vi.mocked(getUnreadCount).mockResolvedValue(0)

    const { container } = renderWithProviders(<KpiStrip />)

    await waitFor(() => expect(screen.getByRole('button', { name: /^Unread: 0/ })).toBeInTheDocument())
    expect(container.querySelector('[data-testid="kpi-failed"]')).toBeNull()
  })
})

// ---------------------------------------------------------------------
// Final fix wave, commit 1 — the KPI numbers are real.
// C1: document_count arrives as a string; summing it concatenated.
// M2: singular "workspace", and no "0d" for a task created today.
// ---------------------------------------------------------------------

describe('KpiStrip — real document total (C1, M2)', () => {
  beforeEach(() => {
    vi.mocked(getWorkspaces).mockReset()
    vi.mocked(listMyTasks).mockReset()
    vi.mocked(getUnreadCount).mockReset()
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics })
    vi.mocked(listMyTasks).mockResolvedValue([])
    vi.mocked(getUnreadCount).mockResolvedValue(0)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('sums string document counts numerically: "4" + "12" is 16, not "0412"', async () => {
    vi.mocked(getWorkspaces).mockResolvedValue([wireWorkspace('w1', '4'), wireWorkspace('w2', '12')])
    renderWithProviders(<KpiStrip />)
    expect(await screen.findByRole('link', { name: 'Documents: 16, across 2 workspaces' })).toBeInTheDocument()
    expect(screen.queryByText('0412')).not.toBeInTheDocument()
  })

  it('treats a non-numeric count as an unavailable total, never as 0', async () => {
    vi.mocked(getWorkspaces).mockResolvedValue([wireWorkspace('w1', '4'), wireWorkspace('w2', 'n/a')])
    const { container } = renderWithProviders(<KpiStrip />)
    expect(await screen.findByRole('link', { name: 'Documents: unavailable' })).toBeInTheDocument()
    expect(container.querySelector('[data-testid="kpi-failed"]')).not.toBeNull()
  })

  it('says "across 1 workspace", singular', async () => {
    vi.mocked(getWorkspaces).mockResolvedValue([wireWorkspace('w1', '4')])
    renderWithProviders(<KpiStrip />)
    expect(await screen.findByRole('link', { name: 'Documents: 4, across 1 workspace' })).toBeInTheDocument()
  })

  it('links Awaiting approval to the approvals tab of /tasks', async () => {
    vi.mocked(getWorkspaces).mockResolvedValue([])
    renderWithProviders(<KpiStrip />)
    expect(await screen.findByRole('link', { name: /^Awaiting approval: 0/ })).toHaveAttribute('href', '/tasks?tab=approvals')
    expect(screen.getByRole('link', { name: /^Open tasks: 0/ })).toHaveAttribute('href', '/tasks')
    expect(screen.getByRole('link', { name: /^Documents: 0/ })).toHaveAttribute('href', '/workspaces')
  })

  it('opens the notifications panel from the Unread card, which is a button', async () => {
    vi.mocked(getWorkspaces).mockResolvedValue([])
    vi.mocked(getUnreadCount).mockResolvedValue(2)
    useUIStore.setState({ notificationsOpen: false })
    renderWithProviders(<KpiStrip />)
    const unread = await screen.findByRole('button', { name: 'Unread: 2, notifications' })
    await userEvent.click(unread)
    expect(useUIStore.getState().notificationsOpen).toBe(true)
    useUIStore.setState({ notificationsOpen: false })
  })

  it('lays the cards out one, two, then four across', async () => {
    vi.mocked(getWorkspaces).mockResolvedValue([])
    renderWithProviders(<KpiStrip />)
    const documents = await screen.findByRole('link', { name: /^Documents: 0/ })
    expect(documents.parentElement).toHaveClass('grid', 'grid-cols-1', 'shell:grid-cols-2', 'wide:grid-cols-4', 'gap-4')
  })

  it('describes an approval created today as waiting since today, not "0d"', async () => {
    vi.mocked(getWorkspaces).mockResolvedValue([])
    vi.mocked(listMyTasks).mockResolvedValue([{
      id: 't-wf', title: 'Approve contract', description: '', status: 'open', priority: 'normal',
      source: 'workflow', due_at: null, created_by: 'u1', created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(), assignees: [], documents: [],
    }])
    renderWithProviders(<KpiStrip />)
    expect(await screen.findByRole('link', { name: 'Awaiting approval: 1, waiting since today' })).toBeInTheDocument()
    expect(screen.queryByText(/0d\b/)).not.toBeInTheDocument()
  })
})

describe('number formatting (M3)', () => {
  const big = 12345
  const formatted = big.toLocaleString()

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('KpiCard renders its numeral with locale grouping', async () => {
    render(<KpiCard icon={FolderOpen} label="Documents" value={big} href="/workspaces" />)
    expect(await screen.findByText(formatted, {}, { timeout: 2000 })).toBeInTheDocument()
  })

  it('BreakdownBars renders each figure with locale grouping', () => {
    render(
      <BreakdownBars title="File types" slices={[{ key: 'pdf', label: 'PDF', value: big, share: 1 }]}
        isLoading={false} isError={false} onRetry={vi.fn()} emptyLabel="No files yet" />,
    )
    expect(screen.getByText(formatted)).toBeInTheDocument()
  })

  it('LibraryBreakdown renders the lifecycle legend figures with locale grouping', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      lifecycle: [{ key: 'active', label: 'Active', value: big, share: 1 }],
    })
    render(<LibraryBreakdown />)
    expect(screen.getByText(formatted, { selector: 'li span' })).toBeInTheDocument()
  })
})

// C2: a query that starts while offline is `fetchStatus: 'paused'` —
// react-query v5 reports `isLoading: false, isError: false, data:
// undefined` for it. Gating on `isLoading` rendered confident zeros and
// "nothing here" copy across the whole dashboard. Every widget here runs
// its REAL query (no hook spies) so the paused state is the genuine one.
describe('offline first mount (C2)', () => {
  beforeEach(() => {
    vi.mocked(getWorkspaces).mockReset().mockResolvedValue([wireWorkspace('w1', '4')])
    vi.mocked(listMyTasks).mockReset().mockResolvedValue([])
    vi.mocked(getUnreadCount).mockReset().mockResolvedValue(0)
    vi.mocked(getNotifications).mockReset().mockResolvedValue({ items: [], total_count: 0 })
    onlineManager.setOnline(false)
  })

  afterEach(() => {
    onlineManager.setOnline(true)
    vi.restoreAllMocks()
  })

  it('renders skeletons — no "0" and no empty-state copy — while every query is paused', () => {
    const { container } = renderWithProviders(
      <>
        <KpiStrip />
        <DocumentsAddedChart />
        <LibraryBreakdown />
        <AttentionList />
      </>,
    )

    // Prove the queries really are paused, not merely in flight.
    expect(getWorkspaces).not.toHaveBeenCalled()
    expect(listMyTasks).not.toHaveBeenCalled()
    expect(getNotifications).not.toHaveBeenCalled()

    expect(container.querySelectorAll('[data-testid="kpi-skeleton"]')).toHaveLength(4)
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /: 0\b/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /: 0\b/ })).not.toBeInTheDocument()
    expect(screen.queryByText(/across \d+ workspace/i)).not.toBeInTheDocument()
    expect(
      screen.queryByText(/no documents added|nothing indexed|no files indexed|no contributors|nothing needs you/i),
    ).not.toBeInTheDocument()
    for (const name of ['Documents added', 'Library breakdown', 'Needs your attention']) {
      expect(screen.getByRole('region', { name })).toHaveAttribute('aria-busy', 'true')
    }
  })
})

// ---------------------------------------------------------------------
// Task 7 — breakdown bars.
// ---------------------------------------------------------------------

describe('BreakdownBars', () => {
  const slices = [
    { key: 'pdf', label: 'PDF', value: 30, share: 0.75 },
    { key: 'docx', label: 'Word', value: 10, share: 0.25 },
  ]

  it('prints the figure beside every bar so colour is never the only encoding', () => {
    render(
      <BreakdownBars title="File types" slices={slices} isLoading={false} isError={false}
        onRetry={vi.fn()} emptyLabel="No files yet" />,
    )
    expect(screen.getByText('30')).toBeInTheDocument()
    expect(screen.getByText('10')).toBeInTheDocument()
    expect(screen.getByText('PDF')).toBeInTheDocument()
  })

  it('shows Retry and hides the rows when the query failed', () => {
    render(
      <BreakdownBars title="File types" slices={[]} isLoading={false} isError
        onRetry={vi.fn()} emptyLabel="No files yet" />,
    )
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.queryByText('No files yet')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------
// Final fix wave, commit 2 — the facet widgets are honest.
// ---------------------------------------------------------------------

function metricsWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } } })
  return ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
}

describe('useDashboardMetrics — facet honesty (I1, I2)', () => {
  beforeEach(() => {
    vi.mocked(search).mockReset()
  })

  // I1: `author` is a size-20 terms aggregation. With 21 authors the
  // response carries only the top 20, so dividing by the returned sum
  // inflates every share. lifecycle_state (size 10, 7 states) is never
  // truncated, so its sum is the real document total.
  it('divides a truncated facet by the lifecycle total: 21 authors, the server returns 20', async () => {
    const authors = Array.from({ length: 21 }, (_, i) => ({ value: `Author ${i + 1}`, count: 10 }))
    vi.mocked(search).mockResolvedValue({
      results: [], total_count: 210, latency_ms: 1, search_mode: 'lexical',
      facets: {
        lifecycle_state: [{ value: 'active', count: 150 }, { value: 'draft', count: 60 }],
        author: authors.slice(0, 20),
        doc_type: [{ value: 'application/pdf', count: 210 }],
        created_at: [],
      },
    } satisfies SearchResult)

    const { result } = renderHook(() => metricsHook.useDashboardMetrics(), { wrapper: metricsWrapper() })
    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.contributors).toHaveLength(5)
    for (const s of result.current.contributors) expect(s.share).toBeCloseTo(10 / 210, 6)
    expect(result.current.fileTypes[0].share).toBeCloseTo(1, 6)
  })

  it('never lets a share pass 100%: the denominator is at least the facet\'s own sum', async () => {
    // Fewer lifecycle-bucketed documents than typed ones (e.g. documents
    // indexed without a lifecycle_state): fall back to the facet's own sum.
    vi.mocked(search).mockResolvedValue({
      results: [], total_count: 40, latency_ms: 1, search_mode: 'lexical',
      facets: {
        lifecycle_state: [{ value: 'active', count: 10 }],
        doc_type: [{ value: 'application/pdf', count: 30 }, { value: 'text/plain', count: 10 }],
        author: [], created_at: [],
      },
    } satisfies SearchResult)

    const { result } = renderHook(() => metricsHook.useDashboardMetrics(), { wrapper: metricsWrapper() })
    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.fileTypes.map((s) => s.share)).toEqual([0.75, 0.25])
  })

  // The file-type list is capped at its top five; the total it was divided by
  // is exposed so the card can show what the other types add up to.
  it('exposes the file-type total the shares were divided by, beyond the five shown', async () => {
    vi.mocked(search).mockResolvedValue({
      results: [], total_count: 100, latency_ms: 1, search_mode: 'lexical',
      facets: {
        lifecycle_state: [{ value: 'active', count: 100 }],
        doc_type: [30, 20, 15, 10, 8, 7, 6, 4].map((count, i) => ({ value: `type/${i}`, count })),
        author: [], created_at: [],
      },
    } satisfies SearchResult)

    const { result } = renderHook(() => metricsHook.useDashboardMetrics(), { wrapper: metricsWrapper() })
    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.fileTypes).toHaveLength(5)
    expect(result.current.fileTypesTotal).toBe(100)
    const shown = result.current.fileTypes.reduce((sum, s) => sum + s.value, 0)
    // 30 + 20 + 15 + 10 + 8 shown; the other three types hold 7 + 6 + 4.
    expect(result.current.fileTypesTotal - shown).toBe(17)
  })

  it('uses the facet\'s own sum as the file-type total when it exceeds the lifecycle total', async () => {
    vi.mocked(search).mockResolvedValue({
      results: [], total_count: 40, latency_ms: 1, search_mode: 'lexical',
      facets: {
        lifecycle_state: [{ value: 'active', count: 10 }],
        doc_type: [{ value: 'application/pdf', count: 30 }, { value: 'text/plain', count: 10 }],
        author: [], created_at: [],
      },
    } satisfies SearchResult)
    const { result } = renderHook(() => metricsHook.useDashboardMetrics(), { wrapper: metricsWrapper() })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.fileTypesTotal).toBe(40)
  })

  it('reports a file-type total of 0 while loading and when no facets came back', async () => {
    vi.mocked(search).mockResolvedValue(
      { results: [], total_count: 1_200_000, latency_ms: 1, search_mode: 'lexical' } as unknown as SearchResult,
    )
    const { result } = renderHook(() => metricsHook.useDashboardMetrics(), { wrapper: metricsWrapper() })
    expect(result.current.fileTypesTotal).toBe(0)
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.fileTypesTotal).toBe(0)
  })

  // I2: above 1M documents the search service strips the aggregations and
  // `facets` (json omitempty) disappears from the response.
  it('reports the facets unavailable — not empty — when documents exist but no facets came back', async () => {
    vi.mocked(search).mockResolvedValue(
      { results: [], total_count: 1_200_000, latency_ms: 1, search_mode: 'lexical' } as unknown as SearchResult,
    )
    const { result } = renderHook(() => metricsHook.useDashboardMetrics(), { wrapper: metricsWrapper() })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.isUnavailable).toBe(true)
  })

  // Found by the R20 axe scan: a search outage raised the global "Server
  // error" toast on top of four widgets that already show their own
  // failure and Retry. The facet read opts out, like the upload flow.
  it('asks the API client not to toast a failure the widgets already show', async () => {
    vi.mocked(search).mockResolvedValue({ results: [], facets: {}, total_count: 0, latency_ms: 1, search_mode: 'lexical' })
    const { result } = renderHook(() => metricsHook.useDashboardMetrics(), { wrapper: metricsWrapper() })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(search).toHaveBeenCalledWith(
      { query: '', facets: ['created_at', 'lifecycle_state', 'doc_type', 'author'], page_size: 1, search_mode: 'lexical' },
      { suppressErrorToast: true },
    )
  })

  it('a tenant with no documents is empty, not unavailable', async () => {
    vi.mocked(search).mockResolvedValue(
      { results: [], total_count: 0, latency_ms: 1, search_mode: 'lexical' } as unknown as SearchResult,
    )
    const { result } = renderHook(() => metricsHook.useDashboardMetrics(), { wrapper: metricsWrapper() })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.isUnavailable).toBe(false)
  })
})

describe('facet widgets when the facets are unavailable (I2)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('both facet cards say "Too many documents to chart", never a "No … yet" claim', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isUnavailable: true })
    render(
      <>
        <DocumentsAddedChart />
        <LibraryBreakdown />
      </>,
    )
    expect(screen.getAllByText('Too many documents to chart')).toHaveLength(2)
    expect(
      screen.queryByText(/no documents added|nothing indexed|no files indexed|no contributors/i),
    ).not.toBeInTheDocument()
    // The ring's count subtitle would read a false "0 indexed documents".
    expect(screen.queryByText(/^[\d,]+ indexed documents$/)).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------
// Final fix wave, commit 3 — geometry and motion.
// ---------------------------------------------------------------------

describe('WidgetCard subtitle (R17)', () => {
  // A data-derived subtitle ("12 indexed documents") above a skeleton or an
  // error card is a fabricated number. The slot stays (header geometry);
  // its text waits for settled data.
  it('keeps the subtitle slot but renders no text while loading or failed', () => {
    const { rerender } = render(
      <WidgetCard title="Lifecycle" subtitle="12 indexed documents" isLoading><p>rows</p></WidgetCard>,
    )
    const slot = () => screen.getByRole('region', { name: 'Lifecycle' }).querySelector('header p')
    expect(slot()).not.toBeNull()
    expect(screen.queryByText('12 indexed documents')).not.toBeInTheDocument()

    rerender(<WidgetCard title="Lifecycle" subtitle="12 indexed documents" isError onRetry={vi.fn()}><p>rows</p></WidgetCard>)
    expect(slot()).not.toBeNull()
    expect(screen.queryByText('12 indexed documents')).not.toBeInTheDocument()

    rerender(<WidgetCard title="Lifecycle" subtitle="12 indexed documents"><p>rows</p></WidgetCard>)
    expect(screen.getByText('12 indexed documents')).toBeInTheDocument()
  })
})

describe('KpiCard geometry (I4, L106)', () => {
  const series = [
    { label: 'Aug', iso: '2026-08-01T00:00:00.000Z', value: 3 },
    { label: 'Sep', iso: '2026-09-01T00:00:00.000Z', value: 9 },
  ]

  it('reserves the hint line and the sparkline slot while loading, and draws no sparkline yet', () => {
    const { container } = render(
      <KpiCard icon={FolderOpen} label="Documents" value={undefined} hint="across 2 workspaces"
        sparkline={series} href="/workspaces" />,
    )
    const hint = container.querySelector('[data-testid="kpi-hint"]')
    expect(hint).not.toBeNull()
    expect(hint).toBeEmptyDOMElement()
    expect(container.querySelector('[data-testid="kpi-sparkline"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="kpi-sparkline-bar"]')).toBeNull()
  })

  it('draws no sparkline under a failed numeral, but keeps its slot', () => {
    const { container } = render(
      <KpiCard icon={FolderOpen} label="Documents" value={undefined} isError hint="unable to load"
        sparkline={series} href="/workspaces" />,
    )
    expect(container.querySelector('[data-testid="kpi-sparkline"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="kpi-sparkline-bar"]')).toBeNull()
  })

  it('draws the sparkline beside a real numeral', () => {
    const { container } = render(
      <KpiCard icon={FolderOpen} label="Documents" value={12} hint="across 2 workspaces"
        sparkline={series} href="/workspaces" />,
    )
    expect(container.querySelectorAll('[data-testid="kpi-sparkline"] [data-testid="kpi-sparkline-bar"]')).toHaveLength(2)
  })

  it('reserves no sparkline slot for a tile that has no series', () => {
    const { container } = render(<KpiCard icon={FolderOpen} label="Unread" value={undefined} onClick={vi.fn()} />)
    expect(container.querySelector('[data-testid="kpi-sparkline"]')).toBeNull()
  })
})

describe('widget bodies reserve their loaded height while loading (I4)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  const bodyClass = (name: string) =>
    screen.getByRole('region', { name }).children[1].getAttribute('class') ?? ''

  it('DocumentsAddedChart and LibraryBreakdown keep one min-height across states', () => {
    const loaded = {
      ...emptyMetrics,
      activity: [{ label: 'Sep', iso: '2026-09-01T00:00:00.000Z', value: 4 }],
      lifecycle: [{ key: 'active', label: 'Active', value: 4, share: 1 }],
      fileTypes: [{ key: 'application/pdf', label: 'PDF', value: 4, share: 1 }],
      contributors: [{ key: 'Alice', label: 'Alice', value: 4, share: 1 }],
    }
    const names = ['Documents added', 'Library breakdown']
    const ui = <><DocumentsAddedChart /><LibraryBreakdown /></>

    const spy = vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isLoading: true })
    const { rerender } = render(ui)
    const loading = names.map(bodyClass)
    spy.mockReturnValue(loaded)
    rerender(<>{ui}</>)
    const ready = names.map(bodyClass)

    for (const [i, name] of names.entries()) {
      expect(loading[i], name).toMatch(/\bmin-h-\[\d+px\]/)
      expect(loading[i], name).toBe(ready[i])
    }
  })
})
