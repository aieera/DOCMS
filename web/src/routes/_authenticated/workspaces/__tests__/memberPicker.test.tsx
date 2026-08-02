// Workspace member picker — searches the server, not a snapshot of it.
//
// Reported from QA: a user created a minute earlier was not returned by the
// member search, but appeared after a hard page reload. Two causes, both
// pinned here:
//
//   1. The query key ignored the search term and cached for 60s, so the
//      first list fetched (taken before the new user existed) was replayed
//      for every subsequent search. Reloading dropped the cache, which is
//      why a refresh "fixed" it.
//   2. It fetched getUsers({limit:'25'}) and filtered in the browser. The
//      backend orders by created_at DESC, so past 25 users everyone older
//      became permanently unfindable no matter what you typed.
//
// Both failures look identical to the user — "that person doesn't exist" —
// and neither raises an error, so only a test keeps them fixed.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { WorkspaceSettingsSections } from '../$workspaceId/settings'
import { listUserDirectory } from '@/api/auth'
import { getUsers } from '@/api/admin'
import { renderWithProviders } from '@/test/renderWithProviders'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(),
  Link: ({ children, to: _t, params: _p, ...rest }: Record<string, unknown> & { children?: React.ReactNode }) => (
    <a {...rest}>{children}</a>
  ),
}))
vi.mock('@/api/auth', () => ({ listUserDirectory: vi.fn() }))
vi.mock('@/api/admin', () => ({ getUsers: vi.fn() }))
vi.mock('@/api/workspaces', () => ({
  getWorkspace: vi.fn().mockResolvedValue({
    id: 'ws-1', name: 'QA Shared Workspace', description: '',
    created_by: 'admin-1', document_count: 0, member_count: 1,
  }),
}))
vi.mock('@/hooks/useAuth', () => ({
  useCurrentUser: () => ({ id: 'admin-1', role: 'owner' }),
}))
vi.mock('@/hooks/useWorkspaces', () => ({
  useUpdateWorkspace: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteWorkspace: () => ({ mutate: vi.fn(), isPending: false }),
  useTransferWorkspaceOwnership: () => ({ mutate: vi.fn(), isPending: false }),
  useWorkspaceMembers: () => ({ data: [], isLoading: false }),
  useAddWorkspaceMember: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateWorkspaceMemberRole: () => ({ mutate: vi.fn(), isPending: false }),
  useRemoveWorkspaceMember: () => ({ mutate: vi.fn(), isPending: false }),
}))
vi.mock('@/components/intelligence/WorkspaceAISettings', () => ({
  WorkspaceAISettingsSection: () => null,
}))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const TESTER_TWO = { id: 'u-two', display_name: 'QA Tester Two', email: 'qa.tester.two@acme.local' }

beforeEach(() => {
  vi.mocked(getUsers).mockReset().mockResolvedValue({ items: [], total_count: 0 })
  vi.mocked(listUserDirectory).mockReset().mockResolvedValue([])
})

async function openPicker() {
  const user = userEvent.setup()
  renderWithProviders(<WorkspaceSettingsSections workspaceId="ws-1" onDeleted={vi.fn()} />)
  const input = await screen.findByTestId('ws-member-picker-search')
  await user.click(input)
  return { user, input }
}

describe('workspace member picker', () => {
  it('sends the typed term to the server instead of filtering a cached list', async () => {
    const { user, input } = await openPicker()
    await user.type(input, 'tester.two')

    await waitFor(() =>
      expect(listUserDirectory).toHaveBeenCalledWith(expect.stringContaining('tester.two')),
    )
    // The old client-filtered path fetched a fixed 25-user page and sifted
    // it in the browser. Nothing may request that page for the picker.
    // (getUsers itself is still legitimately used by the transfer-ownership
    // section rendered in this same tree, so assert on the call shape.)
    for (const call of vi.mocked(getUsers).mock.calls) {
      expect(call[0]).not.toMatchObject({ limit: '25' })
    }
  })

  it('finds a user created after the picker was first opened', async () => {
    // First search: the directory does not know Tester Two yet.
    vi.mocked(listUserDirectory).mockResolvedValueOnce([])
    const { user, input } = await openPicker()
    await user.type(input, 'one')
    await waitFor(() => expect(listUserDirectory).toHaveBeenCalled())

    // She is created; the next search must reach the server again rather
    // than replay the first response.
    vi.mocked(listUserDirectory).mockResolvedValue([TESTER_TWO])
    await user.clear(input)
    await user.type(input, 'two')

    expect(await screen.findByText(/QA Tester Two/i)).toBeInTheDocument()
  })

  it('keys the cache per search term, so two searches are two requests', async () => {
    const { user, input } = await openPicker()
    await user.type(input, 'alice')
    await waitFor(() => expect(listUserDirectory).toHaveBeenCalledTimes(1))
    await user.clear(input)
    await user.type(input, 'bob')
    await waitFor(() => expect(listUserDirectory).toHaveBeenCalledTimes(2))
  })
})
