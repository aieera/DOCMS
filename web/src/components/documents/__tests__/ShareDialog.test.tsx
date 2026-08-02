import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'

import { ShareDialog } from '@/components/documents/ShareDialog'
import { listUserDirectory } from '@/api/auth'
import { grantPermission } from '@/api/permissions'
import { renderWithProviders } from '@/test/renderWithProviders'

vi.mock('@/api/auth', () => ({ listUserDirectory: vi.fn() }))
vi.mock('@/api/permissions', () => ({ grantPermission: vi.fn() }))
vi.mock('@/api/shareLinks', () => ({ createShareLink: vi.fn() }))
vi.mock('@/api/ztShare', () => ({ createZTShare: vi.fn() }))
vi.mock('@/api/documents', () => ({ getVersions: vi.fn() }))
vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

const DIRECTORY = [
  { id: 'u-amanda', display_name: 'Amanda Anderson', email: 'amanda.anderson@acme.local' },
  { id: 'u-chris', display_name: 'Christopher Taylor', email: 'christopher.taylor@acme.local' },
]

/** An axios-shaped rejection carrying the services' error envelope. */
const apiError = (message: string) => ({ response: { data: { type: 'FORBIDDEN', message } } })

function renderDialog() {
  return renderWithProviders(
    <ShareDialog
      open
      onOpenChange={vi.fn()}
      documentId="doc-1"
      documentTitle="PO-AFIT-00499.pdf"
    />,
  )
}

async function pick(user: ReturnType<typeof userEvent.setup>, name: string, email: string) {
  await user.type(screen.getByTestId('share-people-input'), name.split(' ')[0])
  await user.click(await screen.findByTestId(`share-people-option-${email}`))
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listUserDirectory).mockResolvedValue(DIRECTORY as never)
})

describe('<ShareDialog> people mode', () => {
  it('grants the picked capability to the selected person', async () => {
    const user = userEvent.setup()
    vi.mocked(grantPermission).mockResolvedValue({} as never)
    renderDialog()

    await pick(user, 'Amanda Anderson', 'amanda.anderson@acme.local')
    await user.click(screen.getByTestId('share-people-submit'))

    await waitFor(() =>
      expect(grantPermission).toHaveBeenCalledWith('document', 'doc-1', 'user', 'u-amanda', 'view'),
    )
    expect(toast.success).toHaveBeenCalledWith('Shared with 1 person')
  })

  // The reported bug: a bare `catch {}` discarded the server's reason, so a
  // 403 read exactly like a network blip.
  it('surfaces the server reason when a grant is refused', async () => {
    const user = userEvent.setup()
    vi.mocked(grantPermission).mockRejectedValue(
      apiError('you need share access to this document before you can share it — ask an owner or admin'),
    )
    renderDialog()

    await pick(user, 'Amanda Anderson', 'amanda.anderson@acme.local')
    await user.click(screen.getByTestId('share-people-submit'))

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    const message = vi.mocked(toast.error).mock.calls[0][0] as string
    expect(message).toContain('Amanda Anderson')
    expect(message).toContain('you need share access')
  })

  it('falls back to a generic reason when the error carries no message', async () => {
    const user = userEvent.setup()
    vi.mocked(grantPermission).mockRejectedValue(new Error('Network Error'))
    renderDialog()

    await pick(user, 'Amanda Anderson', 'amanda.anderson@acme.local')
    await user.click(screen.getByTestId('share-people-submit'))

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(vi.mocked(toast.error).mock.calls[0][0]).toContain('the server rejected the request')
  })

  it('keeps only the failed recipients selected so a retry is one click', async () => {
    const user = userEvent.setup()
    vi.mocked(grantPermission).mockImplementation((_rt, _rid, _pt, principalId) =>
      principalId === 'u-amanda'
        ? Promise.reject(apiError('permission denied'))
        : (Promise.resolve({}) as never),
    )
    renderDialog()

    await pick(user, 'Amanda Anderson', 'amanda.anderson@acme.local')
    await pick(user, 'Christopher Taylor', 'christopher.taylor@acme.local')
    await user.click(screen.getByTestId('share-people-submit'))

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    const chips = screen.getByTestId('share-people-chips')
    expect(chips).toHaveTextContent('Amanda Anderson')
    expect(chips).not.toHaveTextContent('Christopher Taylor')
  })
})
