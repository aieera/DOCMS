import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'

import { server } from '@/test/mocks/server'
import { AssigneePicker } from '@/components/tasks/AssigneePicker'

function renderPicker(props: Partial<React.ComponentProps<typeof AssigneePicker>> = {}) {
  const onChange = vi.fn()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AssigneePicker value={[]} onChange={onChange} {...props} />
    </QueryClientProvider>,
  )
  return { onChange }
}

const DIRECTORY = [
  { id: 'u1', display_name: 'Ada Lovelace', email: 'ada@acme.test' },
  { id: 'u2', display_name: 'Grace Hopper', email: 'grace@acme.test' },
]

describe('AssigneePicker', () => {
  it('searches the directory and adds the clicked user', async () => {
    server.use(
      http.get('*/api/v1/auth/users/directory', () => HttpResponse.json({ users: DIRECTORY })),
    )
    const { onChange } = renderPicker()

    await userEvent.type(screen.getByTestId('assignee-search'), 'ada')
    const option = await screen.findByTestId('assignee-option-u1', {}, { timeout: 3000 })
    await userEvent.click(option)

    expect(onChange).toHaveBeenCalledWith(['u1'])
  })

  it('appends to the existing selection rather than replacing it', async () => {
    server.use(
      http.get('*/api/v1/auth/users/directory', () => HttpResponse.json({ users: DIRECTORY })),
    )
    const { onChange } = renderPicker({ value: ['u1'], known: DIRECTORY })

    await userEvent.type(screen.getByTestId('assignee-search'), 'grace')
    await userEvent.click(await screen.findByTestId('assignee-option-u2', {}, { timeout: 3000 }))

    expect(onChange).toHaveBeenCalledWith(['u1', 'u2'])
  })

  it('renders selected users as chips and removes on X', async () => {
    const { onChange } = renderPicker({ value: ['u1', 'u2'], known: DIRECTORY })

    expect(screen.getByTestId('assignee-chip-u1')).toBeInTheDocument()
    expect(screen.getByText('Grace Hopper')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Remove Ada Lovelace' }))
    expect(onChange).toHaveBeenCalledWith(['u2'])
  })

  it('hides already-selected users from the results', async () => {
    server.use(
      http.get('*/api/v1/auth/users/directory', () => HttpResponse.json({ users: DIRECTORY })),
    )
    renderPicker({ value: ['u1'], known: DIRECTORY })

    await userEvent.type(screen.getByTestId('assignee-search'), 'a')
    await waitFor(() => expect(screen.getByTestId('assignee-results')).toBeInTheDocument())
    expect(screen.queryByTestId('assignee-option-u1')).not.toBeInTheDocument()
    expect(await screen.findByTestId('assignee-option-u2')).toBeInTheDocument()
  })

  it('disabled hides the search box and the chip remove buttons', () => {
    renderPicker({ value: ['u1'], known: DIRECTORY, disabled: true })
    expect(screen.queryByTestId('assignee-search')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Remove Ada Lovelace' })).not.toBeInTheDocument()
  })
})
