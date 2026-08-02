// My Trash — the member-facing half of the two-tier trash.
//
// The load-bearing detail is the SECOND action's contract. Clearing an item
// from your own trash destroys nothing: the row and its bytes remain and an
// administrator can still restore it. If this surface ever calls the admin
// purge endpoint, or tells the user the file is gone forever, a member
// either destroys recoverable content or stops asking for a recovery that
// would have worked. Both failures are silent, so they are pinned here.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { MyTrashSection } from '@/components/trash/MyTrashSection'
import { listMyTrash, restoreMyTrash, clearFromMyTrash, purgeFromTrash } from '@/api/trash'
import { renderWithProviders } from '@/test/renderWithProviders'

// The row title is a router <Link>, which needs a RouterProvider this test
// has no reason to build. Swap it for a plain anchor; `to`/`params` are
// dropped so React doesn't warn about unknown DOM attributes.
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({
    children,
    to: _to,
    params: _params,
    ...rest
  }: {
    children?: React.ReactNode
    to?: string
    params?: unknown
    [k: string]: unknown
  }) => <a {...rest}>{children}</a>,
}))

vi.mock('@/api/trash', () => ({
  listMyTrash: vi.fn(),
  restoreMyTrash: vi.fn(),
  clearFromMyTrash: vi.fn(),
  purgeFromTrash: vi.fn(),
}))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const ENTRY = {
  id: 'doc-1',
  title: 'Q3-forecast.xlsx',
  workspace_id: 'ws-1',
  total_size_bytes: 2048,
  lifecycle_state: 'draft',
  deleted_at: '2026-08-01T10:00:00Z',
  deleted_by_name: 'Amanda Anderson',
}

beforeEach(() => {
  vi.mocked(listMyTrash).mockReset().mockResolvedValue({ items: [ENTRY], next_page_token: '' })
  vi.mocked(restoreMyTrash).mockReset().mockResolvedValue(undefined)
  vi.mocked(clearFromMyTrash).mockReset().mockResolvedValue(undefined)
  vi.mocked(purgeFromTrash).mockReset()
})

describe('MyTrashSection', () => {
  it('lists the items this user deleted', async () => {
    renderWithProviders(<MyTrashSection />)
    expect(await screen.findByText('Q3-forecast.xlsx')).toBeInTheDocument()
  })

  it('restores through the per-user route, not the admin route', async () => {
    const user = userEvent.setup()
    renderWithProviders(<MyTrashSection />)
    await screen.findByText('Q3-forecast.xlsx')

    await user.click(screen.getByRole('button', { name: /restore q3-forecast/i }))
    await waitFor(() => expect(restoreMyTrash).toHaveBeenCalledWith('doc-1'))
  })

  it('clears via the per-user route and NEVER calls the admin purge', async () => {
    const user = userEvent.setup()
    renderWithProviders(<MyTrashSection />)
    await screen.findByText('Q3-forecast.xlsx')

    await user.click(screen.getByRole('button', { name: /remove q3-forecast.*from my trash/i }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: /^remove$/i }))

    await waitFor(() => expect(clearFromMyTrash).toHaveBeenCalledWith('doc-1'))
    // The member surface must have no path to destroy bytes.
    expect(purgeFromTrash).not.toHaveBeenCalled()
  })

  it('does not claim the item is permanently deleted', async () => {
    const user = userEvent.setup()
    renderWithProviders(<MyTrashSection />)
    await screen.findByText('Q3-forecast.xlsx')

    await user.click(screen.getByRole('button', { name: /remove q3-forecast.*from my trash/i }))
    const dialog = await screen.findByRole('alertdialog')

    const copy = dialog.textContent ?? ''
    expect(copy).toMatch(/administrator can still recover/i)
    expect(copy).not.toMatch(/permanent|cannot be undone|forever/i)
  })

  it('tells a user with nothing deleted what this list is for', async () => {
    vi.mocked(listMyTrash).mockResolvedValue({ items: [], next_page_token: '' })
    renderWithProviders(<MyTrashSection />)
    expect(await screen.findByText(/haven’t deleted anything/i)).toBeInTheDocument()
  })

  it('stops paging when the cursor repeats', async () => {
    // Same guard as the document browser: a server echoing its token must
    // not turn "Load more" into an unbounded fetch loop.
    vi.mocked(listMyTrash).mockResolvedValue({ items: [ENTRY], next_page_token: '' })
    renderWithProviders(<MyTrashSection />)
    await screen.findByText('Q3-forecast.xlsx')
    expect(screen.queryByRole('button', { name: /load more/i })).not.toBeInTheDocument()
  })
})
