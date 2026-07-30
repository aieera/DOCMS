import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'

import { server } from '@/test/mocks/server'
import { DocumentTasksPanel } from '@/components/tasks/DocumentTasksPanel'
import { useAuthStore } from '@/store/authStore'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}))

const TASK = {
  id: 't1',
  title: 'Countersign the agreement',
  description: '',
  status: 'open',
  priority: 'normal',
  source: 'user',
  due_at: null,
  created_by: 'me-1',
  created_at: '2026-07-28T10:00:00Z',
  updated_at: '2026-07-28T10:00:00Z',
  assignees: [{ user_id: 'me-1', added_by: 'me-1', added_at: '2026-07-28T10:00:00Z' }],
  documents: [],
}

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <DocumentTasksPanel documentId="doc-1" documentTitle="Supplier Agreement.pdf" />
    </QueryClientProvider>,
  )
}

describe('DocumentTasksPanel', () => {
  beforeEach(() => {
    useAuthStore.setState({
      user: { id: 'me-1', role: 'member', email: 'me@acme.test', display_name: 'Me' } as never,
    })
    server.use(
      http.get('*/api/v1/auth/users/directory', () => HttpResponse.json({ users: [] })),
      http.get('*/api/v1/search/suggest', () =>
        HttpResponse.json({ documents: [], tags: [], people: [], recent: [] }),
      ),
    )
  })

  it('lists the tasks linked to this document, scoped by document_id', async () => {
    let seenQuery = ''
    server.use(
      http.get('*/api/v1/tasks', ({ request }) => {
        seenQuery = new URL(request.url).search
        return HttpResponse.json({ items: [TASK], total: 1, limit: 20, offset: 0 })
      }),
    )
    renderPanel()

    expect(await screen.findByTestId('document-task-t1')).toBeInTheDocument()
    expect(screen.getByText('Countersign the agreement')).toBeInTheDocument()
    expect(seenQuery).toContain('document_id=doc-1')
  })

  it('shows an empty message but keeps the create button when there are no tasks', async () => {
    server.use(
      http.get('*/api/v1/tasks', () =>
        HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 }),
      ),
    )
    renderPanel()

    expect(await screen.findByText('No open tasks for this document.')).toBeInTheDocument()
    expect(screen.getByTestId('document-new-task')).toBeInTheDocument()
  })

  it('opens the create dialog pre-linked to this document', async () => {
    server.use(
      http.get('*/api/v1/tasks', () =>
        HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 }),
      ),
    )
    renderPanel()

    await userEvent.click(await screen.findByTestId('document-new-task'))

    await waitFor(() => expect(screen.getByTestId('task-create-dialog')).toBeInTheDocument())
    expect(screen.getByTestId('document-chip-doc-1')).toBeInTheDocument()
    expect(screen.getByTestId('task-title')).toHaveValue('Follow up: Supplier Agreement.pdf')
  })

  it('clicking a task row opens the detail drawer', async () => {
    server.use(
      http.get('*/api/v1/tasks', () =>
        HttpResponse.json({ items: [TASK], total: 1, limit: 20, offset: 0 }),
      ),
      http.get('*/api/v1/tasks/t1', () => HttpResponse.json(TASK)),
      http.get('*/api/v1/tasks/t1/comments', () => HttpResponse.json([])),
      http.get('*/api/v1/tasks/t1/activity', () => HttpResponse.json([])),
    )
    renderPanel()

    await userEvent.click(await screen.findByTestId('document-task-t1'))
    expect(await screen.findByTestId('task-detail-drawer')).toBeInTheDocument()
  })
})
