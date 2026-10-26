// The dashboard page, composed: head, KPI strip, then the 12-column grid of
// Documents added, Needs your attention, Library breakdown and Ask a question.
//
// jsdom has no layout, so equal heights and the breakpoints cannot be
// measured here. What is pinned instead is the contract that produces them:
// the grid, the span classes on each grid cell, and that each card fills its
// cell. The route is rendered through its own `component`, with the data
// layer mocked at the module boundary (the way widgets.test.tsx does).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/renderWithProviders'
import { getWorkspaces } from '@/api/workspaces'
import { listMyTasks } from '@/api/tasks'
import { getNotifications, getUnreadCount } from '@/api/notifications'
import { useAuthStore } from '@/store/authStore'
import * as metricsHook from '@/components/dashboard/useDashboardMetrics'
import type { Slice } from '@/components/dashboard/metrics'
import type { User, Workspace } from '@/types/api'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, search: _search, ...rest }:
    { children: React.ReactNode; to?: string; search?: unknown } & Record<string, unknown>) =>
    <a href={to} {...rest}>{children}</a>,
  useNavigate: () => vi.fn(),
  // The route file registers itself on import; hand back its options so the
  // test can render `component` without the generated route tree.
  createFileRoute: () => (opts: Record<string, unknown>) => opts,
}))

vi.mock('@/api/workspaces', () => ({
  getWorkspaces: vi.fn(),
  getFolders: vi.fn(() => Promise.resolve([])),
}))
vi.mock('@/api/tasks', () => ({
  taskKeys: { mine: () => ['tasks', 'mine'] },
  listMyTasks: vi.fn(),
}))
vi.mock('@/api/notifications', () => ({
  getUnreadCount: vi.fn(),
  getNotifications: vi.fn(),
  markAsRead: vi.fn(),
}))
vi.mock('@/api/intelligence', () => ({ listPendingTagSuggestions: vi.fn(() => Promise.resolve({ total: 0 })) }))
vi.mock('@/api/search', () => ({ search: vi.fn() }))

import { Route } from '../_authenticated/index'

const Page = (Route as unknown as { component: () => React.ReactNode }).component

const slice = (key: string, value: number, share: number): Slice => ({ key, label: key, value, share })
const metrics = {
  activity: [{ label: 'Jan', iso: '2026-01-01T00:00:00.000Z', value: 12 }],
  lifecycle: [slice('Active', 30, 0.6), slice('Draft', 20, 0.4)],
  fileTypes: [slice('PDF', 20, 0.4)],
  fileTypesTotal: 50,
  contributors: [slice('Alice Mason', 25, 0.5)],
  isLoading: false, isError: false, isUnavailable: false, refetch: vi.fn(),
}

const workspaces = [
  { id: 'w1', name: 'Legal', document_count: '4' },
  { id: 'w2', name: 'Finance', document_count: '8' },
] as unknown as Workspace[]

const REGION_NAMES = ['Documents added', 'Needs your attention', 'Library breakdown', 'Ask a question'] as const

/** The direct child of the page grid that contains `el`. */
function gridCell(el: HTMLElement): HTMLElement {
  const grid = screen.getByRole('heading', { level: 1 }).closest('.grid-cols-12') as HTMLElement
  let node: HTMLElement = el
  while (node.parentElement && node.parentElement !== grid) node = node.parentElement
  expect(node.parentElement).toBe(grid)
  return node
}

async function renderPage() {
  const utils = renderWithProviders(<Page />)
  // Let the workspaces / tasks / notifications queries settle.
  await screen.findByText(/across your 2 workspaces/i)
  return utils
}

beforeEach(() => {
  // Only Date is faked, so nothing else in the run waits on fake timers.
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 9, 26, 9, 0, 0))
  vi.mocked(getWorkspaces).mockReset().mockResolvedValue(workspaces)
  vi.mocked(listMyTasks).mockReset().mockResolvedValue([])
  vi.mocked(getUnreadCount).mockReset().mockResolvedValue(3)
  vi.mocked(getNotifications).mockReset().mockResolvedValue({ items: [], total_count: 0 })
  vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...metrics })
  useAuthStore.setState({
    user: { id: 'u1', email: 'x@y.z', display_name: 'Sulthan Sha', role: 'member' } as unknown as User,
  })
})

afterEach(() => {
  // Unmount before restoring the metrics spy, so nothing re-renders into the real hook.
  cleanup()
  vi.restoreAllMocks()
  vi.useRealTimers()
  useAuthStore.setState({ user: null })
})

describe('dashboard page', () => {
  it('shows the greeting as the h1 and the head actions', async () => {
    await renderPage()
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Good morning, Sulthan')
    expect(screen.getByTestId('header-upload-button')).toHaveTextContent('Upload')
    // The head link goes to /ask; the Ask card has its own, separate button.
    expect(screen.getByRole('link', { name: 'Ask a question' })).toHaveAttribute('href', '/ask')
  })

  it.each(REGION_NAMES)('renders the "%s" card as a named region', async (name) => {
    await renderPage()
    expect(screen.getByRole('region', { name })).toBeInTheDocument()
  })

  it('no longer renders the Quick actions block', async () => {
    await renderPage()
    expect(screen.queryByTestId('quick-action-upload')).toBeNull()
    expect(screen.queryByRole('region', { name: /quick actions/i })).toBeNull()
    expect(screen.queryByRole('heading', { name: /quick actions/i })).toBeNull()
  })

  describe('layout', () => {
    it('is one 12-column grid with a 16px gap that back-fills the half rows below the wide breakpoint', async () => {
      await renderPage()
      const grid = screen.getByRole('heading', { level: 1 }).closest('.grid-cols-12') as HTMLElement
      expect(grid).toHaveClass('grid', 'grid-cols-12', 'gap-4')
      // Between the shell and wide breakpoints attention and ask each span 6
      // but are separated in the DOM by the full-width library card; dense
      // packing puts them side by side instead of leaving two half rows.
      expect(grid).toHaveClass('grid-flow-dense')
    })

    it('lays the cells out in order: head, KPIs, chart, attention, library, ask', async () => {
      await renderPage()
      const grid = screen.getByRole('heading', { level: 1 }).closest('.grid-cols-12') as HTMLElement
      const cells = Array.from(grid.children) as HTMLElement[]
      expect(cells).toHaveLength(6)
      expect(cells[0]).toContainElement(screen.getByRole('heading', { level: 1 }))
      expect(cells[1]).toContainElement(screen.getByRole('link', { name: /^Open tasks/ }))
      REGION_NAMES.forEach((name, i) => {
        expect(cells[i + 2]).toContainElement(screen.getByRole('region', { name }))
      })
    })

    it('gives the head and the KPI strip the full width', async () => {
      await renderPage()
      expect(gridCell(screen.getByRole('heading', { level: 1 }))).toHaveClass('col-span-12')
      const kpiCell = gridCell(screen.getByRole('link', { name: /^Open tasks/ }))
      expect(kpiCell).toHaveClass('col-span-12')
      // The strip does its own 1 / 2 / 4 column split inside the cell.
      expect(kpiCell.firstElementChild).toHaveClass('grid-cols-1', 'shell:grid-cols-2', 'wide:grid-cols-4')
    })

    it('spans the chart and the library 12 columns, then 8 from the wide breakpoint', async () => {
      await renderPage()
      for (const name of ['Documents added', 'Library breakdown']) {
        const cell = gridCell(screen.getByRole('region', { name }))
        expect(cell).toHaveClass('col-span-12', 'wide:col-span-8')
        expect(cell.className).not.toMatch(/shell:col-span/)
      }
    })

    it('spans attention and ask 12 columns, 6 from the shell breakpoint and 4 from the wide breakpoint', async () => {
      await renderPage()
      for (const name of ['Needs your attention', 'Ask a question']) {
        expect(gridCell(screen.getByRole('region', { name }))).toHaveClass(
          'col-span-12', 'shell:col-span-6', 'wide:col-span-4',
        )
      }
    })

    it('makes every card fill its grid cell, so cards in a row are the same height', async () => {
      await renderPage()
      for (const name of REGION_NAMES) {
        const card = screen.getByRole('region', { name })
        const cell = gridCell(card)
        // The cell is full height, and nothing sets align-self, so it stretches to the row.
        expect(cell).toHaveClass('h-full')
        expect(cell.className).not.toMatch(/\bself-/)
        // The card is the cell, or sits in a single-cell grid wrapper that stretches it.
        if (cell !== card) {
          expect(cell).toHaveClass('grid')
          expect(cell.children).toHaveLength(1)
          expect(cell.firstElementChild).toBe(card)
        }
      }
    })

    it('uses only logical utilities and no neumorphic shadow on the grid cells', async () => {
      await renderPage()
      const grid = screen.getByRole('heading', { level: 1 }).closest('.grid-cols-12') as HTMLElement
      for (const cell of Array.from(grid.children)) {
        expect(cell.className).not.toMatch(/shadow-neu|\b(ml|mr|pl|pr|left|right)-/)
      }
    })
  })

  describe('upload dialog', () => {
    it('opens from the head Upload button', async () => {
      await renderPage()
      vi.useRealTimers()
      expect(screen.queryByRole('dialog')).toBeNull()
      await userEvent.click(screen.getByTestId('header-upload-button'))
      const dialog = await screen.findByRole('dialog')
      expect(within(dialog).getByText('Upload documents')).toBeInTheDocument()
    })

    it('closes again and can be reopened', async () => {
      await renderPage()
      vi.useRealTimers()
      await userEvent.click(screen.getByTestId('header-upload-button'))
      await screen.findByRole('dialog')
      await userEvent.keyboard('{Escape}')
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
      await act(async () => { await userEvent.click(screen.getByTestId('header-upload-button')) })
      expect(await screen.findByRole('dialog')).toBeInTheDocument()
    })
  })
})
