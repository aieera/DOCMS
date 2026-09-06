import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'

import { server } from '@/test/mocks/server'
import { TaskCreateDialog } from '@/components/tasks/TaskCreateDialog'
import { useAuthStore } from '@/store/authStore'

function renderDialog(props: Partial<React.ComponentProps<typeof TaskCreateDialog>> = {}) {
  const onOpenChange = vi.fn()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <TaskCreateDialog open onOpenChange={onOpenChange} {...props} />
    </QueryClientProvider>,
  )
  return { onOpenChange }
}

describe('TaskCreateDialog', () => {
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

  it('submits title, priority and the self-assignment as assignee_ids', async () => {
    let body: unknown
    server.use(
      http.post('*/api/v1/tasks', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ id: 'new-1' }, { status: 201 })
      }),
    )
    renderDialog()

    await userEvent.type(screen.getByTestId('task-title'), 'Chase the invoice')
    await userEvent.click(screen.getByTestId('task-create-submit'))

    await waitFor(() =>
      expect(body).toMatchObject({
        title: 'Chase the invoice',
        priority: 'normal',
        assignee_ids: ['me-1'],
      }),
    )
  })

  it('pre-links defaultDocument and pre-fills the title from it', async () => {
    let body: unknown
    server.use(
      http.post('*/api/v1/tasks', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ id: 'new-2' }, { status: 201 })
      }),
    )
    renderDialog({ defaultDocument: { document_id: 'doc-9', title: 'Invoice June.pdf' } })

    // Opening from a document pre-fills the title and shows the chip.
    expect(screen.getByTestId('task-title')).toHaveValue('Follow up: Invoice June.pdf')
    expect(screen.getByTestId('document-chip-doc-9')).toBeInTheDocument()

    await userEvent.click(screen.getByTestId('task-create-submit'))
    await waitFor(() => expect(body).toMatchObject({ document_ids: ['doc-9'] }))
  })

  it('refuses an empty title with an inline message instead of a dead button (SD-21)', async () => {
    let posted = 0
    server.use(
      http.post('*/api/v1/tasks', () => {
        posted++
        return HttpResponse.json({ id: 'never' }, { status: 201 })
      }),
    )
    renderDialog()
    const submit = screen.getByTestId('task-create-submit')
    // The button is clickable — a silently 50%-opacity button explains
    // nothing. Clicking with an empty title answers on the field.
    expect(submit).toBeEnabled()
    await userEvent.click(submit)
    expect(screen.getByText('Enter a title.')).toBeInTheDocument()
    expect(posted).toBe(0)
  })

  it('closes and reports the new id after a successful create', async () => {
    server.use(http.post('*/api/v1/tasks', () => HttpResponse.json({ id: 'new-3' }, { status: 201 })))
    const onCreated = vi.fn()
    const { onOpenChange } = renderDialog({ onCreated })

    await userEvent.type(screen.getByTestId('task-title'), 'Something')
    await userEvent.click(screen.getByTestId('task-create-submit'))

    await waitFor(() => expect(onCreated).toHaveBeenCalledWith('new-3'))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})
