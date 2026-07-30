import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'

import { server } from '@/test/mocks/server'
import { DocumentPicker } from '@/components/tasks/DocumentPicker'

function renderPicker(props: Partial<React.ComponentProps<typeof DocumentPicker>> = {}) {
  const onChange = vi.fn()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <DocumentPicker value={[]} onChange={onChange} {...props} />
    </QueryClientProvider>,
  )
  return { onChange }
}

const SUGGEST = {
  documents: [
    { text: 'Supplier Agreement.pdf', document_id: 'd1', score: 1 },
    { text: 'Invoice June.pdf', document_id: 'd2', score: 0.8 },
  ],
  tags: [],
  people: [],
  recent: [],
}

describe('DocumentPicker', () => {
  it('searches documents and adds the clicked result with its title', async () => {
    server.use(http.get('*/api/v1/search/suggest', () => HttpResponse.json(SUGGEST)))
    const { onChange } = renderPicker()

    await userEvent.type(screen.getByTestId('document-search'), 'supplier')
    await userEvent.click(await screen.findByTestId('document-option-d1', {}, { timeout: 3000 }))

    expect(onChange).toHaveBeenCalledWith([
      { document_id: 'd1', title: 'Supplier Agreement.pdf' },
    ])
  })

  it('renders selected documents as chips and removes on X', async () => {
    const { onChange } = renderPicker({
      value: [
        { document_id: 'd1', title: 'Supplier Agreement.pdf' },
        { document_id: 'd2', title: 'Invoice June.pdf' },
      ],
    })

    expect(screen.getByTestId('document-chip-d1')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Remove Invoice June.pdf' }))

    expect(onChange).toHaveBeenCalledWith([
      { document_id: 'd1', title: 'Supplier Agreement.pdf' },
    ])
  })

  it('hides already-selected documents from the results', async () => {
    server.use(http.get('*/api/v1/search/suggest', () => HttpResponse.json(SUGGEST)))
    renderPicker({ value: [{ document_id: 'd1', title: 'Supplier Agreement.pdf' }] })

    await userEvent.type(screen.getByTestId('document-search'), 'pdf')
    await waitFor(() => expect(screen.getByTestId('document-results')).toBeInTheDocument())
    expect(screen.queryByTestId('document-option-d1')).not.toBeInTheDocument()
    expect(await screen.findByTestId('document-option-d2')).toBeInTheDocument()
  })

  it('does not query until something is typed', async () => {
    let called = 0
    server.use(
      http.get('*/api/v1/search/suggest', () => {
        called += 1
        return HttpResponse.json(SUGGEST)
      }),
    )
    renderPicker()
    await new Promise((r) => setTimeout(r, 400))
    expect(called).toBe(0)
    expect(screen.queryByTestId('document-results')).not.toBeInTheDocument()
  })
})
