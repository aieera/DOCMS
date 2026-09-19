// Task 2 (2026-09-17 UX improvements) — regression test for honest error
// states. The audit found that /tasks, /search, /notifications,
// /workspaces and the admin users list all destructured only
// `{ data, isLoading }` from their queries, so a query FAILURE fell
// through to the same branch as a genuinely empty result — the app told
// the user "You're all caught up" (or rendered a blank canvas) when the
// fetch actually failed. This test pins the fix for the highest-value
// screen (tasks): on failure, a retry-capable ErrorState must render
// and the false "all caught up" copy must never appear.
import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { renderWithProviders } from '@/test/renderWithProviders'

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
  listTasks: vi.fn().mockRejectedValue(new Error('boom')),
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
  getMyTasks: vi.fn().mockRejectedValue(new Error('boom')),
  signalStep: vi.fn(),
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
