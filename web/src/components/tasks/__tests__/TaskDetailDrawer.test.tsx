import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'

import { server } from '@/test/mocks/server'
import { TaskDetailDrawer } from '@/components/tasks/TaskDetailDrawer'
import { describeActivity } from '@/components/tasks/TaskActivity'
import { useAuthStore } from '@/store/authStore'

// The drawer renders TanStack Router <Link>s for linked documents; the
// test only asserts they exist, so a minimal stub keeps the test free of
// a router provider.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, ...rest }: { children: React.ReactNode; [k: string]: unknown }) => (
    <a data-testid={rest['data-testid'] as string}>{children}</a>
  ),
}))

const TASK = {
  id: 't1',
  title: 'Review supplier agreement',
  description: 'Check the indemnity clause',
  status: 'open',
  priority: 'high',
  source: 'user',
  due_at: null,
  created_by: 'creator-1',
  created_at: '2026-07-28T10:00:00Z',
  updated_at: '2026-07-28T10:00:00Z',
  assignees: [{ user_id: 'assignee-1', added_by: 'creator-1', added_at: '2026-07-28T10:00:00Z' }],
  documents: [
    {
      document_id: 'd1',
      workspace_id: 'w1',
      title: 'Supplier Agreement.pdf',
      linked_by: 'creator-1',
      linked_at: '2026-07-28T10:00:00Z',
    },
  ],
}

function mockTaskRoutes(overrides: Partial<typeof TASK> = {}) {
  server.use(
    http.get('*/api/v1/tasks/t1', () => HttpResponse.json({ ...TASK, ...overrides })),
    http.get('*/api/v1/tasks/t1/comments', () => HttpResponse.json([])),
    http.get('*/api/v1/tasks/t1/activity', () => HttpResponse.json([])),
    http.get('*/api/v1/auth/users/directory', () => HttpResponse.json({ users: [] })),
  )
}

function renderDrawer(onClose = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <TaskDetailDrawer taskId="t1" onClose={onClose} />
    </QueryClientProvider>,
  )
  return { onClose }
}

function signIn(id: string, role = 'member') {
  useAuthStore.setState({
    user: { id, role, email: `${id}@acme.test`, display_name: id } as never,
  })
}

describe('TaskDetailDrawer', () => {
  beforeEach(() => signIn('assignee-1'))

  it('renders nothing when no task is selected', () => {
    const qc = new QueryClient()
    const { container } = render(
      <QueryClientProvider client={qc}>
        <TaskDetailDrawer taskId={null} onClose={vi.fn()} />
      </QueryClientProvider>,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the task detail, status/priority badges and linked documents', async () => {
    mockTaskRoutes()
    renderDrawer()

    expect(await screen.findByText('Review supplier agreement')).toBeInTheDocument()
    expect(screen.getByText('Check the indemnity clause')).toBeInTheDocument()
    expect(screen.getByText('open')).toBeInTheDocument()
    expect(screen.getByText('high')).toBeInTheDocument()
    expect(await screen.findByTestId('task-doc-link-d1')).toBeInTheDocument()
  })

  it('an assignee can complete the task — the "anyone completes" rule', async () => {
    mockTaskRoutes()
    let completed = false
    server.use(
      http.post('*/api/v1/tasks/t1/complete', () => {
        completed = true
        return HttpResponse.json({ ...TASK, status: 'done' })
      }),
    )
    renderDrawer()

    await userEvent.click(await screen.findByTestId('task-complete'))
    await waitFor(() => expect(completed).toBe(true))
  })

  it('hides status actions from a user who is neither assignee, creator nor admin', async () => {
    signIn('stranger-9')
    mockTaskRoutes()
    renderDrawer()

    await screen.findByText('Review supplier agreement')
    expect(screen.queryByTestId('task-actions')).not.toBeInTheDocument()
    expect(screen.queryByTestId('task-complete')).not.toBeInTheDocument()
  })

  it('offers Delete to the creator but not to a plain assignee', async () => {
    mockTaskRoutes()
    renderDrawer()
    await screen.findByText('Review supplier agreement')
    expect(screen.queryByTestId('task-delete')).not.toBeInTheDocument()

    signIn('creator-1')
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <TaskDetailDrawer taskId="t1" onClose={vi.fn()} />
      </QueryClientProvider>,
    )
    expect(await screen.findByTestId('task-delete')).toBeInTheDocument()
  })

  it('shows Reopen instead of Complete once the task is done', async () => {
    mockTaskRoutes({ status: 'done' })
    renderDrawer()

    expect(await screen.findByRole('button', { name: /reopen/i })).toBeInTheDocument()
    expect(screen.queryByTestId('task-complete')).not.toBeInTheDocument()
  })

  it('posts a comment through the comment box', async () => {
    mockTaskRoutes()
    let body: unknown
    server.use(
      http.post('*/api/v1/tasks/t1/comments', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ id: 'c1', body: 'looks fine', mentions: [] }, { status: 201 })
      }),
    )
    renderDrawer()

    await userEvent.type(await screen.findByTestId('task-comment-input'), 'looks fine')
    await userEvent.click(screen.getByRole('button', { name: /comment/i }))
    await waitFor(() => expect(body).toEqual({ body: 'looks fine' }))
  })
})

describe('describeActivity', () => {
  it('renders each action as a sentence, reading only its own detail keys', () => {
    expect(describeActivity({ action: 'created', detail: {} } as never)).toBe('created the task')
    expect(
      describeActivity({ action: 'status_changed', detail: { from: 'open', to: 'done' } } as never),
    ).toBe('moved it from open to done')
    expect(
      describeActivity({ action: 'updated', detail: { fields: ['title', 'priority'] } } as never),
    ).toBe('updated title, priority')
    expect(
      describeActivity({ action: 'document_linked', detail: { title: 'Invoice.pdf' } } as never),
    ).toBe('linked Invoice.pdf')
    // Unknown actions fall back to the raw verb rather than crashing.
    expect(describeActivity({ action: 'future_action', detail: {} } as never)).toBe('future_action')
  })
})
