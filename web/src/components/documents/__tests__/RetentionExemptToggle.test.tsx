import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { RetentionExemptToggle } from '../RetentionExemptToggle'
import type { Document } from '@/types/api'

vi.mock('sonner', () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}))

const setRetentionExemptMock = vi.fn()
vi.mock('@/api/documents', () => ({
  setRetentionExempt: (id: string, exempt: boolean, reason?: string) =>
    setRetentionExemptMock(id, exempt, reason),
}))

vi.mock('@/api/client', () => ({
  readErrorMessage: (e: unknown) => {
    const d = (e as { response?: { data?: { error?: string } } })?.response?.data
    return d?.error ?? null
  },
}))

const baseDoc: Document = {
  id: 'doc-1',
  tenant_id: 't',
  workspace_id: 'ws',
  title: 'Acme MSA 2026',
  lifecycle_state: 'active',
  mime_type: 'application/pdf',
  size_bytes: 1024,
  version_count: 1,
  tags: [],
  created_by: 'u',
  created_by_name: 'A',
  created_at: '2026-05-22T00:00:00Z',
  has_thumbnail: false,
}

function wrap(children: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

beforeEach(() => {
  setRetentionExemptMock.mockReset()
})

describe('<RetentionExemptToggle>', () => {
  it('renders nothing for non-admin viewers when the doc is NOT exempt', () => {
    const { container } = render(wrap(
      <RetentionExemptToggle doc={baseDoc} canManage={false} />,
    ))
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the read-only exempt badge for non-admins when the doc IS exempt', () => {
    render(wrap(
      <RetentionExemptToggle
        doc={{ ...baseDoc, retention_exempt: true, retention_exempt_reason: 'Litigation anchor' }}
        canManage={false}
      />,
    ))
    expect(screen.getByTestId('retention-exempt-badge')).toBeInTheDocument()
    expect(screen.getByText(/litigation anchor/i)).toBeInTheDocument()
    // No "Set" or "Clear" buttons surfaced.
    expect(screen.queryByTestId('retention-exempt-set')).not.toBeInTheDocument()
    expect(screen.queryByTestId('retention-exempt-clear')).not.toBeInTheDocument()
  })

  it('requires a reason before posting exempt=true', async () => {
    const user = userEvent.setup()
    setRetentionExemptMock.mockResolvedValue(undefined)
    render(wrap(<RetentionExemptToggle doc={baseDoc} canManage />))

    await user.click(screen.getByTestId('retention-exempt-set'))
    // Dialog opens with submit disabled while reason is empty.
    const submit = await screen.findByTestId('retention-exempt-submit')
    expect(submit).toBeDisabled()

    const reason = screen.getByTestId('retention-exempt-reason') as HTMLTextAreaElement
    fireEvent.change(reason, { target: { value: 'Pending IRS audit, keep through 2027.' } })
    expect(submit).not.toBeDisabled()
    fireEvent.click(submit)

    await waitFor(() => expect(setRetentionExemptMock).toHaveBeenCalledTimes(1))
    expect(setRetentionExemptMock).toHaveBeenCalledWith(
      'doc-1',
      true,
      'Pending IRS audit, keep through 2027.',
    )
  })

  it('clearing exemption goes through a confirm dialog and posts exempt=false', async () => {
    const user = userEvent.setup()
    setRetentionExemptMock.mockResolvedValue(undefined)
    render(wrap(
      <RetentionExemptToggle
        doc={{ ...baseDoc, retention_exempt: true, retention_exempt_reason: 'old reason' }}
        canManage
      />,
    ))
    await user.click(screen.getByTestId('retention-exempt-clear'))
    const alert = await screen.findByRole('alertdialog')
    fireEvent.click(within(alert).getByRole('button', { name: /clear exemption/i }))
    await waitFor(() => expect(setRetentionExemptMock).toHaveBeenCalledWith('doc-1', false, ''))
  })
})
