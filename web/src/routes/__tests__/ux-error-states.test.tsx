// Task 2 (2026-09-17 UX improvements) — regression test for honest error
// states. The audit found that /tasks, /search, /notifications,
// /workspaces and the admin users list all destructured only
// `{ data, isLoading }` from their queries, so a query FAILURE fell
// through to the same branch as a genuinely empty result — the app told
// the user "You're all caught up" (or rendered a blank canvas) when the
// fetch actually failed. This test pins the fix for the highest-value
// screen (tasks): on failure, a retry-capable ErrorState must render
// and the false "all caught up" copy must never appear.
//
// Fix round 1 (review finding): MyTasksSection gated its EMPTY branch on
// `!isError` but left the results branches (TaskTable/TaskKanban/
// pagination) ungated. React Query keeps the last successful `data`
// around across a FAILED background refetch (it does not clear it) —
// with this app's defaults (staleTime: 60_000, retry: 1,
// refetchOnWindowFocus: true, see src/main.tsx), a tab regaining focus
// after a stale window and hitting a network blip / expired session /
// 5xx would leave `isError: true` AND a non-empty, fully interactive
// `data` at the same time. Without the `!isError` guard on the results
// branches, the user saw the new ErrorState banner *and* a stale task
// list underneath it — the same dishonest-UI defect, just triggered on
// a later failure instead of first load. The second test below pins
// that: a successful first fetch followed by a failed refetch must hide
// the stale rows, not render them beside the retry button.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { renderWithProviders } from '@/test/renderWithProviders'
import type { Task, TaskPage } from '@/api/tasks'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, ...r }: { children: React.ReactNode } & Record<string, unknown>) => <a {...r}>{children}</a>,
  useNavigate: () => vi.fn(),
  createFileRoute: () => (o: Record<string, unknown>) => o,
}))

vi.mock('@/api/tasks', () => ({
  taskKeys: {
    all: ['tasks'],
    mine: () => ['tasks', 'mine'],
    created: () => ['tasks', 'created'],
    list: (p: unknown) => ['tasks', 'list', p],
    detail: (id: string) => ['tasks', 'detail', id],
  },
  listTasks: vi.fn(),
  invalidateTasks: vi.fn(),
  // Referenced (but never invoked, since no rows render on failure) by
  // TaskRow / KanbanCard / TaskDetailDrawer in the same module tree.
  completeTask: vi.fn(),
  reopenTask: vi.fn(),
  cancelTask: vi.fn(),
  deleteTask: vi.fn(),
  startTask: vi.fn(),
  getTask: vi.fn(),
}))

vi.mock('@/api/workflows', () => ({
  getMyTasks: vi.fn(),
  signalStep: vi.fn(),
}))

import { Route } from '../_authenticated/tasks'
import { listTasks } from '@/api/tasks'
import { getMyTasks } from '@/api/workflows'

const Page = (Route as unknown as { component: () => React.ReactNode }).component

const listTasksMock = vi.mocked(listTasks)
const getMyTasksMock = vi.mocked(getMyTasks)

const staleTask: Task = {
  id: 't1',
  title: 'Existing task',
  description: '',
  status: 'open',
  priority: 'normal',
  source: 'user',
  created_by: 'u1',
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z',
  assignees: [],
  documents: [],
}
const stalePage: TaskPage = { items: [staleTask], total: 1, limit: 50, offset: 0 }

describe('tasks — query failure', () => {
  beforeEach(() => {
    listTasksMock.mockReset()
    getMyTasksMock.mockReset()
  })

  it('shows an error with retry, never the "caught up" empty state', async () => {
    listTasksMock.mockRejectedValue(new Error('boom'))
    getMyTasksMock.mockRejectedValue(new Error('boom'))

    renderWithProviders(<Page />)
    await waitFor(() => expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument())
    expect(screen.queryByText(/caught up/i)).toBeNull()
  })

  it('does not render the stale task list beside the error banner after a failed refetch', async () => {
    // First fetch succeeds with one task, the second (background) fetch
    // fails — the exact sequence a >60s-stale, refetch-on-focus tab hits
    // on a network blip.
    listTasksMock.mockResolvedValueOnce(stalePage).mockRejectedValue(new Error('boom'))
    getMyTasksMock.mockRejectedValue(new Error('boom'))

    const { client } = renderWithProviders(<Page />)

    // Initial successful load — this is the row that must disappear once
    // the follow-up fetch fails.
    await waitFor(() => expect(screen.getByText('Existing task')).toBeInTheDocument())

    await client.refetchQueries({ queryKey: ['tasks', 'list'] })

    await waitFor(() => expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument())
    expect(screen.queryByText('Existing task')).toBeNull()
    expect(screen.queryByTestId('task-table')).toBeNull()
  })
})
