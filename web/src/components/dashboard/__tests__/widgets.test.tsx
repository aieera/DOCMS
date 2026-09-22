import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, renderHook, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider, onlineManager } from '@tanstack/react-query'
import { WidgetCard } from '../WidgetCard'
import { ChartDataTable } from '../ChartDataTable'
import { Sparkline } from '../Sparkline'
import { FolderOpen } from 'lucide-react'
import { KpiTile } from '../KpiTile'
import { ActivityChart } from '../ActivityChart'
import * as metricsHook from '../useDashboardMetrics'
import { KpiStrip } from '../KpiStrip'
import { BreakdownBars } from '../BreakdownBars'
import { LifecycleDonut } from '../LifecycleDonut'
import { NeedsAttention } from '../NeedsAttention'
import { FileTypesAndContributors } from '../FileTypesAndContributors'
import { renderWithProviders } from '@/test/renderWithProviders'
import { getWorkspaces } from '@/api/workspaces'
import { listMyTasks } from '@/api/tasks'
import { getNotifications, getUnreadCount, markAsRead } from '@/api/notifications'
import { search } from '@/api/search'
import type { Notification, SearchResult, Workspace } from '@/types/api'
import type { Task } from '@/api/tasks'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, ...rest }: { children: React.ReactNode; to?: string } & Record<string, unknown>) =>
    <a href={to} {...rest}>{children}</a>,
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
// getNotifications feeds NeedsAttention's unread mentions (R24). The
// default resolves an empty inbox so tests that don't care stay green;
// markAsRead is here only so a test can prove it is never called.
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

describe('Sparkline', () => {
  it('renders nothing for fewer than two points — a one-point line is not a trend', () => {
    const { container } = render(<Sparkline points={[{ label: 'Jan', iso: '2026-01-01', value: 3 }]} />)
    expect(container.querySelector('svg')).toBeNull()
  })

  it('draws a polyline for a real series', () => {
    const { container } = render(
      <Sparkline points={[
        { label: 'Jan', iso: '2026-01-01', value: 3 },
        { label: 'Feb', iso: '2026-02-01', value: 9 },
      ]} />,
    )
    expect(container.querySelector('polyline')).not.toBeNull()
  })

  it('survives a flat series without producing NaN coordinates', () => {
    const { container } = render(
      <Sparkline points={[
        { label: 'Jan', iso: '2026-01-01', value: 5 },
        { label: 'Feb', iso: '2026-02-01', value: 5 },
      ]} />,
    )
    expect(container.querySelector('polyline')?.getAttribute('points')).not.toMatch(/NaN/)
  })
})

describe('KpiTile', () => {
  it('shows a skeleton instead of a zero while the value is still undefined', () => {
    const { container } = render(
      <KpiTile icon={FolderOpen} label="Documents" value={undefined} href="/workspaces" />,
    )
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(container.querySelector('[data-testid="kpi-skeleton"]')).not.toBeNull()
  })

  it('names the tile for assistive tech as label plus value', () => {
    render(<KpiTile icon={FolderOpen} label="Documents" value={42} href="/workspaces" />)
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
  activity: [], lifecycle: [], fileTypes: [], contributors: [],
  isLoading: false, isError: false, isUnavailable: false, refetch: vi.fn(),
}

describe('ActivityChart', () => {
  it('renders the failure state with Retry, not the empty state', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isError: true })
    render(<ActivityChart />)
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.queryByText(/no documents added/i)).not.toBeInTheDocument()
    vi.restoreAllMocks()
  })

  // I3: an empty 12-month window is not an empty tenant — an archive
  // imported more than a year ago sits right beside a Documents KPI of 500.
  it('says the window is empty, never that no documents exist "yet"', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics })
    render(<ActivityChart />)
    expect(screen.getByText('No documents added in the last 12 months')).toBeInTheDocument()
    expect(screen.queryByText(/yet/i)).not.toBeInTheDocument()
    vi.restoreAllMocks()
  })

  it('exposes the series as an accessible table so the numbers are not trapped in an image', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      activity: [
        { label: 'Jan', iso: '2026-01-01T00:00:00.000Z', value: 12 },
        { label: 'Feb', iso: '2026-02-01T00:00:00.000Z', value: 48 },
      ],
    })
    render(<ActivityChart />)
    expect(screen.getByRole('table', { name: /documents added per month/i })).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: '48' })).toBeInTheDocument()
    vi.restoreAllMocks()
  })
})

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

describe('KpiTile — fix round 1', () => {
  it('suppresses the hint while the value is still loading, even if the caller supplied one', () => {
    render(
      <KpiTile
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
      <KpiTile icon={FolderOpen} label="Documents" value={undefined} isError hint="unable to load" href="/workspaces" />,
    )
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(container.querySelector('[data-testid="kpi-skeleton"]')).toBeNull()
    expect(container.querySelector('[data-testid="kpi-failed"]')).not.toBeNull()
  })

  it('gives a failed tile an accessible name that says unavailable, never a number', () => {
    render(
      <KpiTile icon={FolderOpen} label="Documents" value={undefined} isError hint="unable to load" href="/workspaces" />,
    )
    expect(screen.getByRole('link', { name: 'Documents: unavailable' })).toBeInTheDocument()
  })

  it('forces the failed hint to the alert tone regardless of the hintTone the caller passed', () => {
    render(
      <KpiTile
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
      <KpiTile icon={FolderOpen} label="Open tasks" value={2} hint="2 overdue" hintTone="alert" href="/tasks" />,
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
    expect(screen.getByRole('link', { name: /^Unread: 3/ })).toBeInTheDocument()
  })

  it('when every query fails, shows one consolidated failure card with a working Retry instead of four tiles', async () => {
    vi.mocked(getWorkspaces).mockRejectedValue(new Error('boom'))
    vi.mocked(listMyTasks).mockRejectedValue(new Error('boom'))
    vi.mocked(getUnreadCount).mockRejectedValue(new Error('boom'))

    renderWithProviders(<KpiStrip />)

    const retry = await screen.findByRole('button', { name: /retry/i })
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
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

    await waitFor(() => expect(screen.getByRole('link', { name: /^Unread: 0/ })).toBeInTheDocument())
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

  it('KpiTile renders its numeral with locale grouping', async () => {
    render(<KpiTile icon={FolderOpen} label="Documents" value={big} href="/workspaces" />)
    expect(await screen.findByText(formatted, {}, { timeout: 2000 })).toBeInTheDocument()
  })

  it('BreakdownBars renders each figure with locale grouping', () => {
    render(
      <BreakdownBars title="File types" slices={[{ key: 'pdf', label: 'PDF', value: big, share: 1 }]}
        isLoading={false} isError={false} onRetry={vi.fn()} emptyLabel="No files yet" unit="documents" />,
    )
    expect(screen.getByText(formatted)).toBeInTheDocument()
  })

  it('LifecycleDonut renders legend figures with locale grouping', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      lifecycle: [{ key: 'active', label: 'Active', value: big, share: 1 }],
    })
    render(<LifecycleDonut />)
    expect(screen.getByText(formatted)).toBeInTheDocument()
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
        <ActivityChart />
        <LifecycleDonut />
        <NeedsAttention />
        <FileTypesAndContributors />
      </>,
    )

    // Prove the queries really are paused, not merely in flight.
    expect(getWorkspaces).not.toHaveBeenCalled()
    expect(listMyTasks).not.toHaveBeenCalled()
    expect(getNotifications).not.toHaveBeenCalled()

    expect(container.querySelectorAll('[data-testid="kpi-skeleton"]')).toHaveLength(4)
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /: 0\b/ })).not.toBeInTheDocument()
    expect(screen.queryByText(/across \d+ workspace/i)).not.toBeInTheDocument()
    expect(
      screen.queryByText(/no documents added|nothing indexed|no files indexed|no contributors|nothing needs you/i),
    ).not.toBeInTheDocument()
    for (const name of ['Documents added', 'Lifecycle', 'Needs your attention', 'File types', 'Top contributors']) {
      expect(screen.getByRole('region', { name })).toHaveAttribute('aria-busy', 'true')
    }
  })
})

// ---------------------------------------------------------------------
// Task 7 — lifecycle donut, breakdown bars, needs-attention.
// ---------------------------------------------------------------------

describe('BreakdownBars', () => {
  const slices = [
    { key: 'pdf', label: 'PDF', value: 30, share: 0.75 },
    { key: 'docx', label: 'Word', value: 10, share: 0.25 },
  ]

  it('prints the figure beside every bar so colour is never the only encoding', () => {
    render(
      <BreakdownBars title="File types" slices={slices} isLoading={false} isError={false}
        onRetry={vi.fn()} emptyLabel="No files yet" unit="documents" />,
    )
    expect(screen.getByText('30')).toBeInTheDocument()
    expect(screen.getByText('10')).toBeInTheDocument()
    expect(screen.getByText('PDF')).toBeInTheDocument()
  })

  it('shows Retry and hides the rows when the query failed', () => {
    render(
      <BreakdownBars title="File types" slices={[]} isLoading={false} isError
        onRetry={vi.fn()} emptyLabel="No files yet" unit="documents" />,
    )
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.queryByText('No files yet')).not.toBeInTheDocument()
  })
})

describe('LifecycleDonut', () => {
  it('lists every state with its count in the accessible table', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      lifecycle: [
        { key: 'active', label: 'Active', value: 30, share: 0.75 },
        { key: 'draft', label: 'Draft', value: 10, share: 0.25 },
      ],
    })
    render(<LifecycleDonut />)
    expect(screen.getByRole('table', { name: /documents by lifecycle state/i })).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: 'Active' })).toBeInTheDocument()
    vi.restoreAllMocks()
  })

  // react-query hazard check (Task 5 review, Critical A/B): a subtitle
  // derived from fetched data ("N indexed documents") must never render
  // a confident count while the query is loading or after it failed —
  // `lifecycle` is `[]` in both those states, so an unguarded subtitle
  // would lie "0 indexed documents" under the loading skeleton / error
  // card instead of describing nothing.
  it('never shows a fabricated "0 indexed documents" subtitle while loading', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isLoading: true })
    render(<LifecycleDonut />)
    expect(screen.queryByText(/indexed documents/i)).not.toBeInTheDocument()
    vi.restoreAllMocks()
  })

  it('never shows a fabricated "0 indexed documents" subtitle after the query failed', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isError: true })
    render(<LifecycleDonut />)
    expect(screen.queryByText(/indexed documents/i)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    vi.restoreAllMocks()
  })
})

// NeedsAttention isn't covered by the Step 1 snippet in the task-7
// brief (it only exercises BreakdownBars/LifecycleDonut), but the
// component is a produced interface with its own ranking rule (R4) and
// asChild link (R3) that need direct coverage rather than trust.
describe('NeedsAttention', () => {
  const baseTask: Omit<Task, 'id' | 'title' | 'status' | 'priority' | 'source' | 'due_at'> = {
    description: '',
    created_by: 'u1',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    assignees: [],
    documents: [],
  }

  beforeEach(() => {
    vi.mocked(listMyTasks).mockReset()
    vi.mocked(getNotifications).mockReset().mockResolvedValue({ items: [], total_count: 0 })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders "View all" as a real link to /tasks via Button asChild (R3)', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([])
    renderWithProviders(<NeedsAttention />)
    const link = await screen.findByRole('link', { name: /view all/i })
    expect(link).toHaveAttribute('href', '/tasks')
  })

  it('ranks an overdue task ahead of a merely high-priority one, and drops completed tasks', async () => {
    const now = Date.now()
    const tasks: Task[] = [
      { ...baseTask, id: 't-done', title: 'Finished thing', status: 'done', priority: 'urgent', source: 'user', due_at: null },
      { ...baseTask, id: 't-high', title: 'High priority task', status: 'open', priority: 'high', source: 'user', due_at: null },
      { ...baseTask, id: 't-overdue', title: 'Overdue task', status: 'open', priority: 'normal', source: 'user', due_at: new Date(now - 86_400_000).toISOString() },
    ]
    vi.mocked(listMyTasks).mockResolvedValue(tasks)
    renderWithProviders(<NeedsAttention />)

    await screen.findByText('Overdue task')
    const titles = screen.getAllByText(/task$/i).map((el) => el.textContent)
    expect(titles.indexOf('Overdue task')).toBeLessThan(titles.indexOf('High priority task'))
    expect(screen.queryByText('Finished thing')).not.toBeInTheDocument()
  })

  it('shows Retry, not a stale row list, when the task query fails', async () => {
    vi.mocked(listMyTasks).mockRejectedValue(new Error('boom'))
    renderWithProviders(<NeedsAttention />)
    expect(await screen.findByRole('button', { name: /retry/i })).toBeInTheDocument()
  })

  // react-query hazard check: while the query is pending, `isLoading` is
  // still true and WidgetCard must suppress the row list entirely —
  // nothing derived from the not-yet-arrived `data` may reach the DOM.
  it('renders no task rows while the query is still pending', () => {
    vi.mocked(listMyTasks).mockReturnValue(new Promise<Task[]>(() => {}))
    const { container } = renderWithProviders(<NeedsAttention />)
    expect(container.querySelectorAll('li').length).toBe(0)
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

  it('a tenant with no documents is empty, not unavailable', async () => {
    vi.mocked(search).mockResolvedValue(
      { results: [], total_count: 0, latency_ms: 1, search_mode: 'lexical' } as unknown as SearchResult,
    )
    const { result } = renderHook(() => metricsHook.useDashboardMetrics(), { wrapper: metricsWrapper() })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.isUnavailable).toBe(false)
  })
})

describe('facet widgets when the facets are unavailable (I2) and scope labels (M7)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('all four facet widgets say "Too many documents to chart", never a "No … yet" claim', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isUnavailable: true })
    render(
      <>
        <ActivityChart />
        <LifecycleDonut />
        <FileTypesAndContributors />
      </>,
    )
    expect(screen.getAllByText('Too many documents to chart')).toHaveLength(4)
    expect(
      screen.queryByText(/no documents added|nothing indexed|no files indexed|no contributors/i),
    ).not.toBeInTheDocument()
    // The donut's count subtitle would read a false "0 indexed documents".
    expect(screen.queryByText(/^[\d,]+ indexed documents$/)).not.toBeInTheDocument()
  })

  it('File types and Top contributors say what they are a share of', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      fileTypes: [{ key: 'application/pdf', label: 'PDF', value: 3, share: 1 }],
      contributors: [{ key: 'Alice', label: 'Alice', value: 3, share: 1 }],
    })
    render(<FileTypesAndContributors />)
    for (const name of ['File types', 'Top contributors']) {
      const region = screen.getByRole('region', { name })
      expect(region).toHaveTextContent('Of indexed documents')
    }
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

describe('KpiTile geometry (I4, L106)', () => {
  const series = [
    { label: 'Aug', iso: '2026-08-01T00:00:00.000Z', value: 3 },
    { label: 'Sep', iso: '2026-09-01T00:00:00.000Z', value: 9 },
  ]

  it('reserves the hint line and the sparkline slot while loading, and draws no sparkline yet', () => {
    const { container } = render(
      <KpiTile icon={FolderOpen} label="Documents" value={undefined} hint="across 2 workspaces"
        sparkline={series} href="/workspaces" />,
    )
    const hint = container.querySelector('[data-testid="kpi-hint"]')
    expect(hint).not.toBeNull()
    expect(hint).toBeEmptyDOMElement()
    expect(container.querySelector('[data-testid="kpi-sparkline"]')).not.toBeNull()
    expect(container.querySelector('svg polyline')).toBeNull()
  })

  it('draws no sparkline under a failed numeral, but keeps its slot', () => {
    const { container } = render(
      <KpiTile icon={FolderOpen} label="Documents" value={undefined} isError hint="unable to load"
        sparkline={series} href="/workspaces" />,
    )
    expect(container.querySelector('[data-testid="kpi-sparkline"]')).not.toBeNull()
    expect(container.querySelector('svg polyline')).toBeNull()
  })

  it('draws the sparkline beside a real numeral', () => {
    const { container } = render(
      <KpiTile icon={FolderOpen} label="Documents" value={12} hint="across 2 workspaces"
        sparkline={series} href="/workspaces" />,
    )
    expect(container.querySelector('[data-testid="kpi-sparkline"] svg polyline')).not.toBeNull()
  })

  it('reserves no sparkline slot for a tile that has no series', () => {
    const { container } = render(<KpiTile icon={FolderOpen} label="Unread" value={undefined} href="/notifications" />)
    expect(container.querySelector('[data-testid="kpi-sparkline"]')).toBeNull()
  })
})

describe('widget bodies reserve their loaded height while loading (I4)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  const bodyClass = (name: string) =>
    screen.getByRole('region', { name }).children[1].getAttribute('class') ?? ''

  it('ActivityChart, LifecycleDonut and the breakdown bars keep one min-height across states', () => {
    const loaded = {
      ...emptyMetrics,
      activity: [{ label: 'Sep', iso: '2026-09-01T00:00:00.000Z', value: 4 }],
      lifecycle: [{ key: 'active', label: 'Active', value: 4, share: 1 }],
      fileTypes: [{ key: 'application/pdf', label: 'PDF', value: 4, share: 1 }],
      contributors: [{ key: 'Alice', label: 'Alice', value: 4, share: 1 }],
    }
    const names = ['Documents added', 'Lifecycle', 'File types', 'Top contributors']
    const ui = <><ActivityChart /><LifecycleDonut /><FileTypesAndContributors /></>

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

  it('NeedsAttention keeps one min-height across states', async () => {
    let resolve: (t: Task[]) => void = () => {}
    vi.mocked(listMyTasks).mockReset().mockReturnValue(new Promise<Task[]>((r) => { resolve = r }))
    renderWithProviders(<NeedsAttention />)
    const loading = bodyClass('Needs your attention')
    resolve([])
    await screen.findByText('Nothing needs you right now')
    expect(loading).toMatch(/\bmin-h-\[\d+px\]/)
    expect(bodyClass('Needs your attention')).toBe(loading)
  })
})

// ---------------------------------------------------------------------
// Final fix wave, commit 4 — R24 unread mentions in NeedsAttention, + I6.
// ---------------------------------------------------------------------

describe('NeedsAttention — unread mentions (R24) and honest priority (I6)', () => {
  const DAY = 86_400_000
  const mkTask = (o: Partial<Task> & Pick<Task, 'id' | 'title'>): Task => ({
    description: '', status: 'open', priority: 'normal', source: 'user', due_at: null,
    created_by: 'u1', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
    assignees: [], documents: [], ...o,
  })
  const mkNote = (o: Partial<Notification> & Pick<Notification, 'id' | 'type'>): Notification => ({
    title: 'Someone mentioned you', body: 'Can you check clause 4.2?', read: false,
    created_at: new Date(Date.now() - 3_600_000).toISOString(), ...o,
  })
  const inbox = (items: Notification[]) => ({ items, total_count: items.length })
  const rowTexts = () => screen.getAllByRole('listitem').map((li) => li.textContent ?? '')

  beforeEach(() => {
    vi.mocked(listMyTasks).mockReset()
    vi.mocked(getNotifications).mockReset()
    vi.mocked(markAsRead).mockReset()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('reads the shared notifications-inbox query: getNotifications() with no params', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([])
    vi.mocked(getNotifications).mockResolvedValue(inbox([]))
    const { client } = renderWithProviders(<NeedsAttention />)
    await screen.findByText('Nothing needs you right now')
    expect(getNotifications).toHaveBeenCalledWith()
    expect(client.getQueryData(['notifications-inbox'])).toEqual(inbox([]))
  })

  // Review §5 case 1.
  it('lists only UNREAD mentions — not read mentions, not other notification types', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([])
    vi.mocked(getNotifications).mockResolvedValue(inbox([
      mkNote({ id: 'n-unread', type: 'comment.mention', body: 'Unread mention body' }),
      mkNote({ id: 'n-read', type: 'comment.mention', body: 'Read mention body', read: true }),
      mkNote({ id: 'n-share', type: 'document.shared', body: 'Shared doc body' }),
      mkNote({ id: 'n-digest', type: 'digest.comment', body: 'Digest body' }),
    ]))
    renderWithProviders(<NeedsAttention />)
    expect(await screen.findByText('Unread mention body')).toBeInTheDocument()
    expect(screen.queryByText('Read mention body')).not.toBeInTheDocument()
    expect(screen.queryByText('Shared doc body')).not.toBeInTheDocument()
    expect(screen.queryByText('Digest body')).not.toBeInTheDocument()
    expect(screen.getByText('Mentioned in a comment')).toBeInTheDocument()
    expect(markAsRead).not.toHaveBeenCalled()
  })

  // Review §5 case 2 (+ I6 rank order).
  it('ranks overdue, then a mention, then urgent, high, workflow approval, then other open work', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([
      mkTask({ id: 't-other', title: 'Other task' }),
      mkTask({ id: 't-wf', title: 'Workflow task', source: 'workflow' }),
      mkTask({ id: 't-high', title: 'High task', priority: 'high' }),
      mkTask({ id: 't-urgent', title: 'Urgent task', priority: 'urgent' }),
      mkTask({ id: 't-overdue', title: 'Overdue task', due_at: new Date(Date.now() - DAY).toISOString() }),
    ])
    vi.mocked(getNotifications).mockResolvedValue(inbox([
      mkNote({ id: 'n-1', type: 'task.mention', body: 'Mention body' }),
    ]))
    renderWithProviders(<NeedsAttention />)
    await screen.findByText('Mention body')
    const order = ['Overdue task', 'Mention body', 'Urgent task', 'High task', 'Workflow task', 'Other task']
    const texts = rowTexts()
    const at = order.map((t) => texts.findIndex((x) => x.includes(t)))
    expect(at).toEqual([0, 1, 2, 3, 4, 5])
  })

  it('links a task mention to /tasks and a comment mention to /notifications, with collision-free keys', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 'same-id', title: 'A task' })])
    vi.mocked(getNotifications).mockResolvedValue(inbox([
      mkNote({ id: 'same-id', type: 'comment.mention', body: 'Comment mention', resource_type: 'comment' }),
      mkNote({ id: 'n-2', type: 'task.mention', body: 'Task mention', resource_type: 'task' }),
    ]))
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    renderWithProviders(<NeedsAttention />)
    expect((await screen.findByText('Comment mention')).closest('a')).toHaveAttribute('href', '/notifications')
    expect(screen.getByText('Task mention').closest('a')).toHaveAttribute('href', '/tasks')
    expect(errors.mock.calls.some((c) => String(c[0]).includes('same key'))).toBe(false)
  })

  // I6: priority is shown as text, and the subtitle promises only what the
  // list holds — static, never a count.
  it('names urgent and high priority in text, and keeps a static subtitle', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([
      mkTask({ id: 't-u', title: 'Urgent one', priority: 'urgent' }),
      mkTask({ id: 't-h', title: 'High one', priority: 'high' }),
      mkTask({ id: 't-l', title: 'Low one', priority: 'low' }),
    ])
    vi.mocked(getNotifications).mockResolvedValue(inbox([]))
    renderWithProviders(<NeedsAttention />)
    await screen.findByText('Urgent one')
    const texts = rowTexts()
    expect(texts.find((t) => t.includes('Urgent one'))).toMatch(/\bUrgent\b.*No due date/)
    expect(texts.find((t) => t.includes('High one'))).toMatch(/\bHigh\b.*No due date/)
    expect(texts.find((t) => t.includes('Low one'))).not.toMatch(/\b(Urgent|High|Low)\b ·/)
    expect(screen.getByText('Overdue tasks, urgent work and unread mentions')).toBeInTheDocument()
  })

  it('shows six rows, and never lets a pile of overdue tasks bury every unread mention', async () => {
    const past = new Date(Date.now() - DAY).toISOString()
    vi.mocked(listMyTasks).mockResolvedValue(
      Array.from({ length: 7 }, (_, i) => mkTask({ id: `t-${i}`, title: `Overdue ${i}`, due_at: past })),
    )
    vi.mocked(getNotifications).mockResolvedValue(inbox([
      mkNote({ id: 'n-new', type: 'comment.mention', body: 'Newest mention' }),
      mkNote({ id: 'n-old', type: 'comment.mention', body: 'Older mention' }),
    ]))
    renderWithProviders(<NeedsAttention />)
    await screen.findByText('Overdue 0')
    // Mentions rank 1, so with 7 overdue tasks (rank 0) none makes the cut
    // on rank alone; the newest takes the last of the six rows.
    const texts = rowTexts()
    expect(texts).toHaveLength(6)
    expect(texts[5]).toContain('Newest mention')
    expect(screen.queryByText('Older mention')).not.toBeInTheDocument()
  })

  // Review §5 case 3.
  it('tasks OK, notifications failed: task rows plus an inline mentions notice — never the empty copy', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'Real task' })])
    vi.mocked(getNotifications).mockRejectedValue(new Error('boom'))
    renderWithProviders(<NeedsAttention />)
    expect(await screen.findByText(/couldn.t load mentions/i)).toBeInTheDocument()
    expect(screen.getByText('Real task')).toBeInTheDocument()
    expect(screen.queryByText('Nothing needs you right now')).not.toBeInTheDocument()

    // The inline Retry refetches only the failed query.
    vi.mocked(listMyTasks).mockClear()
    vi.mocked(getNotifications).mockClear().mockResolvedValue(inbox([]))
    await userEvent.click(screen.getByRole('button', { name: /retry/i }))
    await waitFor(() => expect(getNotifications).toHaveBeenCalledTimes(1))
    expect(listMyTasks).not.toHaveBeenCalled()
  })

  // Review §5 case 4.
  it('tasks failed, notifications OK with no mentions: an inline tasks notice — never the empty copy', async () => {
    vi.mocked(listMyTasks).mockRejectedValue(new Error('boom'))
    vi.mocked(getNotifications).mockResolvedValue(inbox([]))
    renderWithProviders(<NeedsAttention />)
    expect(await screen.findByText(/couldn.t load your tasks/i)).toBeInTheDocument()
    expect(screen.queryByText('Nothing needs you right now')).not.toBeInTheDocument()
  })

  it('both failed: one whole-card failure whose Retry refetches both', async () => {
    vi.mocked(listMyTasks).mockRejectedValue(new Error('boom'))
    vi.mocked(getNotifications).mockRejectedValue(new Error('boom'))
    renderWithProviders(<NeedsAttention />)
    const retry = await screen.findByRole('button', { name: /retry/i })
    expect(screen.getAllByRole('button', { name: /retry/i })).toHaveLength(1)
    vi.mocked(listMyTasks).mockClear().mockResolvedValue([])
    vi.mocked(getNotifications).mockClear().mockResolvedValue(inbox([]))
    await userEvent.click(retry)
    await waitFor(() => expect(listMyTasks).toHaveBeenCalledTimes(1))
    expect(getNotifications).toHaveBeenCalledTimes(1)
  })

  // Review §5 case 5.
  it('renders no rows while either query is still pending', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'Real task' })])
    vi.mocked(getNotifications).mockReturnValue(new Promise(() => {}))
    const { container } = renderWithProviders(<NeedsAttention />)
    await waitFor(() => expect(listMyTasks).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 20))
    expect(container.querySelectorAll('li')).toHaveLength(0)
    expect(screen.getByRole('region', { name: 'Needs your attention' })).toHaveAttribute('aria-busy', 'true')
  })

  // Review §5 case 6.
  it('never renders a raw notification type code', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([])
    vi.mocked(getNotifications).mockResolvedValue(inbox([
      mkNote({ id: 'n-1', type: 'comment.mention' }),
      mkNote({ id: 'n-2', type: 'task.mention' }),
    ]))
    const { container } = renderWithProviders(<NeedsAttention />)
    await screen.findByText('Mentioned in a comment')
    expect(container.textContent).not.toMatch(/comment\.mention|task\.mention/)
  })
})
