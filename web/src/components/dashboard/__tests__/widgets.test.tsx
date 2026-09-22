import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { WidgetCard } from '../WidgetCard'
import { ChartDataTable } from '../ChartDataTable'
import { Sparkline } from '../Sparkline'
import { FolderOpen } from 'lucide-react'
import { KpiTile } from '../KpiTile'
import { ActivityChart } from '../ActivityChart'
import * as metricsHook from '../useDashboardMetrics'
import { KpiStrip } from '../KpiStrip'
import { renderWithProviders } from '@/test/renderWithProviders'
import { getWorkspaces } from '@/api/workspaces'
import { listMyTasks } from '@/api/tasks'
import { getUnreadCount } from '@/api/notifications'
import type { Workspace } from '@/types/api'

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
vi.mock('@/api/notifications', () => ({ getUnreadCount: vi.fn() }))

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
  isLoading: false, isError: false, refetch: vi.fn(),
}

describe('ActivityChart', () => {
  it('renders the failure state with Retry, not the empty state', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isError: true })
    render(<ActivityChart />)
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.queryByText(/no documents added yet/i)).not.toBeInTheDocument()
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

const okWorkspace: Workspace = {
  id: 'w1', name: 'Acme', document_count: 5, member_count: 1, created_at: '2026-01-01T00:00:00Z',
}

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
