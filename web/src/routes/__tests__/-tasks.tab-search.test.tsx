// The dashboard's "Awaiting approval" KPI deep-links to /tasks?tab=approvals.
// The tasks page keeps its tab in local state; the route's search param
// only seeds it, so the approvals tab is selected on arrival.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/renderWithProviders'

let search: Record<string, unknown> = {}
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, ...r }: { children: React.ReactNode } & Record<string, unknown>) => <a {...r}>{children}</a>,
  useNavigate: () => vi.fn(),
  createFileRoute: () => (opts: Record<string, unknown>) => ({
    ...opts,
    useSearch: () => search,
  }),
}))

vi.mock('@/api/tasks', () => ({
  taskKeys: {
    all: ['tasks'],
    mine: () => ['tasks', 'mine'],
    created: () => ['tasks', 'created'],
    list: (p: unknown) => ['tasks', 'list', p],
    detail: (id: string) => ['tasks', 'detail', id],
  },
  listTasks: vi.fn(() => new Promise(() => {})),
  invalidateTasks: vi.fn(),
  completeTask: vi.fn(),
  reopenTask: vi.fn(),
  cancelTask: vi.fn(),
  deleteTask: vi.fn(),
  startTask: vi.fn(),
  getTask: vi.fn(),
}))

vi.mock('@/api/workflows', () => ({
  getMyTasks: vi.fn(() => new Promise(() => {})),
  signalStep: vi.fn(),
}))

import { Route } from '../_authenticated/tasks'

const route = Route as unknown as {
  component: () => React.ReactNode
  validateSearch: (s: Record<string, unknown>) => { tab?: string }
}
const Page = route.component

describe('tasks ?tab= deep link', () => {
  beforeEach(() => {
    search = {}
  })

  it('validateSearch keeps a known tab and drops anything else', () => {
    expect(route.validateSearch({ tab: 'approvals' })).toEqual({ tab: 'approvals' })
    expect(route.validateSearch({ tab: 'created' })).toEqual({ tab: 'created' })
    expect(route.validateSearch({ tab: 'my' })).toEqual({ tab: 'my' })
    expect(route.validateSearch({ tab: 'bogus' }).tab).toBeUndefined()
    expect(route.validateSearch({ tab: 42 }).tab).toBeUndefined()
    expect(route.validateSearch({}).tab).toBeUndefined()
  })

  it('selects the approvals tab when the page is opened with ?tab=approvals', () => {
    search = { tab: 'approvals' }
    renderWithProviders(<Page />)
    expect(screen.getByTestId('tab-approvals')).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('tab-my-tasks')).toHaveAttribute('aria-selected', 'false')
  })

  it('selects My tasks when no tab is given', () => {
    renderWithProviders(<Page />)
    expect(screen.getByTestId('tab-my-tasks')).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('tab-approvals')).toHaveAttribute('aria-selected', 'false')
  })
})
