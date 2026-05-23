import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { BulkInviteDialog } from '../BulkInviteDialog'

vi.mock('sonner', () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}))

const inviteUserMock = vi.fn()
vi.mock('@/api/admin', () => ({
  inviteUser: (email: string, role: string, name: string) => inviteUserMock(email, role, name),
}))

// Phase 4 — BulkInviteDialog CSV parse + validation + per-row send.
// We avoid networking the actual invite endpoint by intercepting
// the inviteUser API import.

vi.mock('@/api/client', () => ({
  readErrorMessage: (e: unknown) => {
    const d = (e as { response?: { data?: { error?: string } } })?.response?.data
    return d?.error ?? null
  },
}))

function wrap(children: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

function csvFile(body: string, name = 'invites.csv'): File {
  return new File([body], name, { type: 'text/csv' })
}

beforeEach(() => {
  inviteUserMock.mockReset()
})

describe('<BulkInviteDialog> CSV bulk invite', () => {
  it('flags invalid rows in the preview and never invites them', async () => {
    const user = userEvent.setup()
    inviteUserMock.mockResolvedValue({ user: {}, invite_token: 't', tenant_slug: 's' })

    render(wrap(<BulkInviteDialog open onOpenChange={() => {}} />))

    const csv = csvFile(
      'name,email,role\n' +
      'Alice,alice@example.com,member\n' +
      'Bob,not-an-email,admin\n' +
      'Carol,carol@example.com,owner\n' +    // owner is rejected — bulk path forbids it
      'Dana,dana@example.com,viewer\n',
    )
    await user.upload(screen.getByTestId('bulk-invite-file'), csv)

    // 4 rows preview.
    await waitFor(() => expect(screen.getByTestId('bulk-invite-row-1')).toBeInTheDocument())
    const row2 = screen.getByTestId('bulk-invite-row-2')
    const row3 = screen.getByTestId('bulk-invite-row-3')
    expect(within(row2).getByText(/not a valid email/i)).toBeInTheDocument()
    expect(within(row3).getByText(/role must be one of/i)).toBeInTheDocument()

    // Header count: 2 valid (Alice, Dana), 2 with errors.
    expect(screen.getByText(/2 valid/)).toBeInTheDocument()
    expect(screen.getByText(/2 with errors/)).toBeInTheDocument()

    // Send.
    await user.click(screen.getByTestId('bulk-invite-send'))
    await waitFor(() => expect(inviteUserMock).toHaveBeenCalledTimes(2))
    expect(inviteUserMock).toHaveBeenCalledWith('alice@example.com', 'member', 'Alice')
    expect(inviteUserMock).toHaveBeenCalledWith('dana@example.com', 'viewer', 'Dana')
  })

  it('rejects a CSV with no email column', async () => {
    const user = userEvent.setup()
    render(wrap(<BulkInviteDialog open onOpenChange={() => {}} />))
    await user.upload(
      screen.getByTestId('bulk-invite-file'),
      csvFile('name,role\nAlice,member\n'),
    )
    expect(await screen.findByTestId('bulk-invite-parse-error')).toHaveTextContent(/email/)
    // No rows preview rendered.
    expect(screen.queryByTestId('bulk-invite-row-1')).not.toBeInTheDocument()
  })

  it('defaults missing role to "member" and tolerates a missing display name', async () => {
    const user = userEvent.setup()
    inviteUserMock.mockResolvedValue({ user: {}, invite_token: 't', tenant_slug: 's' })
    render(wrap(<BulkInviteDialog open onOpenChange={() => {}} />))
    await user.upload(
      screen.getByTestId('bulk-invite-file'),
      csvFile('name,email,role\n,minimal@example.com,\n'),
    )
    await waitFor(() => expect(screen.getByTestId('bulk-invite-row-1')).toBeInTheDocument())
    expect(screen.getByText(/1 valid/)).toBeInTheDocument()

    await user.click(screen.getByTestId('bulk-invite-send'))
    await waitFor(() => expect(inviteUserMock).toHaveBeenCalledTimes(1))
    // Falls back to email when no display name is provided.
    expect(inviteUserMock).toHaveBeenCalledWith('minimal@example.com', 'member', 'minimal@example.com')
  })

  it('reports per-row failure when one invite is rejected by the server', async () => {
    const user = userEvent.setup()
    inviteUserMock.mockImplementationOnce(() => Promise.resolve({ user: {}, invite_token: 't', tenant_slug: 's' }))
    inviteUserMock.mockImplementationOnce(() => Promise.reject({
      response: { data: { error: 'email already invited' } },
    }))
    render(wrap(<BulkInviteDialog open onOpenChange={() => {}} />))
    await user.upload(
      screen.getByTestId('bulk-invite-file'),
      csvFile('name,email,role\nA,a@example.com,member\nB,b@example.com,member\n'),
    )
    await waitFor(() => expect(screen.getByTestId('bulk-invite-row-2')).toBeInTheDocument())
    await user.click(screen.getByTestId('bulk-invite-send'))

    const row2 = await screen.findByTestId('bulk-invite-row-2')
    await waitFor(() => expect(within(row2).getByText(/email already invited/)).toBeInTheDocument())
    const row1 = screen.getByTestId('bulk-invite-row-1')
    expect(within(row1).getByText(/sent/i)).toBeInTheDocument()
  })
})
