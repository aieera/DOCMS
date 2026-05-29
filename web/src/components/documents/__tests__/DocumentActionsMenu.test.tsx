import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { DocumentActionsMenu } from '../DocumentActionsMenu'
import type { Document } from '@/types/api'

// Mock react-router so the embedded Link inside ShareDialog/other
// dialogs (none here, but defensive) doesn't blow up without a router.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, ...rest }: { children: ReactNode } & Record<string, unknown>) => (
    <a {...rest}>{children}</a>
  ),
  useNavigate: () => () => {},
  useParams: () => ({}),
  createFileRoute: () => ({ component: () => null }),
}))

vi.mock('sonner', () => ({
  toast: { error: vi.fn(), success: vi.fn(), message: vi.fn() },
}))

// Mock the API surface the menu's actions call into. Each handler
// records that it was invoked so we can pin the wiring.
const updateDocumentMock = vi.fn()
const deleteDocumentMock = vi.fn()
const moveDocumentMock = vi.fn()
const getVersionsMock = vi.fn()
const getDownloadURLMock = vi.fn()

// Forward exactly as many positional args as the real function takes
// — react-query v5 passes a second context arg to mutationFn that the
// real api functions ignore. If the test mock greedy-spreads both,
// .toHaveBeenCalledWith fails on a sneaky-second arg.
vi.mock('@/api/documents', () => ({
  updateDocument: (id: string, body: unknown) => updateDocumentMock(id, body),
  deleteDocument: (id: string) => deleteDocumentMock(id),
  moveDocument: (id: string, folderId: string) => moveDocumentMock(id, folderId),
  getVersions: (id: string) => getVersionsMock(id),
  getDownloadURL: (docId: string, vid: string) => getDownloadURLMock(docId, vid),
}))

// Light replica of readErrorMessage so the wrapper's default onError
// surfaces backend envelopes (used by the 423 LEGAL_HOLD regression).
vi.mock('@/api/client', () => ({
  readErrorMessage: (err: unknown) => {
    const d = (err as { response?: { data?: { error?: string; message?: string } } })?.response?.data
    return d?.error ?? d?.message ?? null
  },
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

vi.mock('@/api/workspaces', () => ({
  getFolders: vi.fn(() => Promise.resolve([])),
}))

vi.mock('@/api/shareLinks', () => ({
  createShareLink: vi.fn(),
}))

vi.mock('@/api/ztShare', () => ({
  createZTShare: vi.fn(),
}))

// Stub the dialog so the menu test only asserts the open/close
// wiring; the dialog has its own focused test file.
vi.mock('../ManageAccessDialog', () => ({
  ManageAccessDialog: ({ open, resourceId, resourceType }: { open: boolean; resourceId: string; resourceType: string }) =>
    open
      ? <div data-testid="manage-access-dialog" data-rid={resourceId} data-rtype={resourceType}>stub</div>
      : null,
}))

const createTaskMock = vi.fn()
vi.mock('@/api/tasks', () => ({
  createTask: (input: unknown) => createTaskMock(input),
}))

const doc: Document = {
  id: 'doc-1',
  tenant_id: 't-1',
  workspace_id: 'ws-1',
  folder_id: 'fld-1',
  title: 'Quarterly report',
  lifecycle_state: 'active',
  mime_type: 'application/pdf',
  total_size_bytes: 1024,
  version_count: 1,
  tags: [],
  created_by: 'u-1',
  created_by_name: 'Alice',
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
  updateDocumentMock.mockReset()
  deleteDocumentMock.mockReset()
  moveDocumentMock.mockReset()
  getVersionsMock.mockReset()
  getDownloadURLMock.mockReset()
  createTaskMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('<DocumentActionsMenu>', () => {
  it('renders an accessible ⋯ trigger over the card surface', () => {
    render(wrap(
      <DocumentActionsMenu doc={doc}>
        <div data-testid="card-surface">Card body</div>
      </DocumentActionsMenu>,
    ))
    const trigger = screen.getByRole('button', { name: 'Document actions' })
    expect(trigger).toBeInTheDocument()
    expect(screen.getByTestId('card-surface')).toBeInTheDocument()
  })

  it('opens the dropdown and lists Rename / Move / Download / Share / Delete', async () => {
    const user = userEvent.setup()
    render(wrap(
      <DocumentActionsMenu doc={doc}>
        <div>card</div>
      </DocumentActionsMenu>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))
    expect(await screen.findByTestId('document-action-rename')).toBeInTheDocument()
    expect(screen.getByTestId('document-action-move')).toBeInTheDocument()
    expect(screen.getByTestId('document-action-download')).toBeInTheDocument()
    expect(screen.getByTestId('document-action-share')).toBeInTheDocument()
    expect(screen.getByTestId('document-action-delete')).toBeInTheDocument()
  })

  it('clicking ⋯ does not propagate to the card surface', async () => {
    const user = userEvent.setup()
    const cardClick = vi.fn()
    render(wrap(
      <div onClick={cardClick}>
        <DocumentActionsMenu doc={doc}>
          <div data-testid="card-body">card body</div>
        </DocumentActionsMenu>
      </div>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))
    // The Radix trigger fires onClick on the button itself; the
    // parent listener must not be invoked because the menu calls
    // stopPropagation on the wrapper.
    expect(cardClick).not.toHaveBeenCalled()
  })

  it('Rename action opens the rename dialog with the current title pre-filled', async () => {
    const user = userEvent.setup()
    render(wrap(
      <DocumentActionsMenu doc={doc}>
        <div>card</div>
      </DocumentActionsMenu>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))
    await user.click(await screen.findByTestId('document-action-rename'))

    const input = await screen.findByTestId('rename-document-input') as HTMLInputElement
    expect(input.value).toBe('Quarterly report')
  })

  it('Rename submit fires updateDocument with the trimmed title', async () => {
    const user = userEvent.setup()
    updateDocumentMock.mockResolvedValue({ ...doc, title: 'Q1 Report' })

    render(wrap(
      <DocumentActionsMenu doc={doc}>
        <div>card</div>
      </DocumentActionsMenu>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))
    await user.click(await screen.findByTestId('document-action-rename'))

    const input = await screen.findByTestId('rename-document-input') as HTMLInputElement
    await user.clear(input)
    await user.type(input, '  Q1 Report  ')
    await user.click(screen.getByTestId('rename-document-submit'))

    await waitFor(() => {
      expect(updateDocumentMock).toHaveBeenCalledWith('doc-1', { title: 'Q1 Report' })
    })
  })

  it('Download action fetches the latest version and follows the signed URL', async () => {
    const user = userEvent.setup()
    getVersionsMock.mockResolvedValue([
      { id: 'v-1', version_number: 1 },
      { id: 'v-2', version_number: 2 },
    ])
    getDownloadURLMock.mockResolvedValue({ url: 'https://signed.example/blob', expires_at: '' })
    const anchorClick = vi.fn()
    // jsdom won't trigger navigations on anchor click; assert on the
    // synthetic anchor element instead.
    const origCreate = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreate(tag)
      if (tag === 'a') el.click = anchorClick
      return el
    })

    render(wrap(
      <DocumentActionsMenu doc={doc}>
        <div>card</div>
      </DocumentActionsMenu>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))
    await user.click(await screen.findByTestId('document-action-download'))

    await waitFor(() => expect(getVersionsMock).toHaveBeenCalledWith('doc-1'))
    await waitFor(() => expect(getDownloadURLMock).toHaveBeenCalledWith('doc-1', 'v-2'))
    await waitFor(() => expect(anchorClick).toHaveBeenCalled())
  })

  it('Delete action opens the confirm dialog and fires deleteDocument on confirm', async () => {
    const user = userEvent.setup()
    deleteDocumentMock.mockResolvedValue(undefined)

    render(wrap(
      <DocumentActionsMenu doc={doc}>
        <div>card</div>
      </DocumentActionsMenu>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))
    await user.click(await screen.findByTestId('document-action-delete'))

    // The confirm dialog is portalled. Wait for the dropdown's
    // dismissable layer to fully tear down before clicking the
    // confirm — otherwise its outside-click handler can intercept
    // the synthetic click that should fire onConfirm.
    const alert = await screen.findByRole('alertdialog')
    expect(alert).toHaveTextContent('Quarterly report')
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())

    const confirm = within(alert).getByRole('button', { name: 'Delete' })
    expect(confirm).not.toBeDisabled()
    // Radix AlertDialogAction's onClick listens through React synthetic
    // events; userEvent's pointer pipeline triggers Radix's overlay
    // dismiss before the action's onClick can run in jsdom. fireEvent
    // dispatches a plain click that the AlertDialogAction sees.
    fireEvent.click(confirm)

    await waitFor(() => expect(deleteDocumentMock).toHaveBeenCalledWith('doc-1'))
  })

  // Regression — the dropdown trigger must not bubble its click up
  // to the surrounding clickable card (in production the card is a
  // <Link>; here a div onClick stands in to assert propagation).
  it('opening the menu does not bubble click into the underlying card surface', async () => {
    const user = userEvent.setup()
    const surface = vi.fn()
    render(wrap(
      <div onClick={surface}>
        <DocumentActionsMenu doc={doc}>
          <div>card</div>
        </DocumentActionsMenu>
      </div>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))
    expect(surface).not.toHaveBeenCalled()
  })

  it('Add to task action opens a dialog and createTask runs with linked_document_id', async () => {
    const user = userEvent.setup()
    createTaskMock.mockResolvedValue({ id: 'task-1' })

    render(wrap(
      <DocumentActionsMenu doc={doc}>
        <div>card</div>
      </DocumentActionsMenu>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))
    await user.click(await screen.findByTestId('document-action-add-to-task'))

    const input = await screen.findByTestId('add-to-task-title') as HTMLInputElement
    expect(input.value).toBe('Follow up: Quarterly report')
    await user.click(screen.getByTestId('add-to-task-submit'))

    await waitFor(() => expect(createTaskMock).toHaveBeenCalledTimes(1))
    expect(createTaskMock).toHaveBeenCalledWith(expect.objectContaining({
      title: 'Follow up: Quarterly report',
      priority: 'normal',
      linked_document_id: 'doc-1',
    }))
  })

  it('Copy item is marked aria-disabled and clicking surfaces the "endpoint pending" hint', async () => {
    const { toast } = await import('sonner')
    const user = userEvent.setup()
    render(wrap(
      <DocumentActionsMenu doc={doc}>
        <div>card</div>
      </DocumentActionsMenu>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))

    const copy = await screen.findByTestId('document-action-copy')
    expect(copy).toHaveAttribute('aria-disabled', 'true')
    await user.click(copy)
    expect(toast.message).toHaveBeenCalledWith(expect.stringMatching(/copy endpoint is pending/i))
  })

  it('Manage access item opens the ManageAccessDialog with this document as the target', async () => {
    const user = userEvent.setup()
    render(wrap(
      <DocumentActionsMenu doc={doc}>
        <div>card</div>
      </DocumentActionsMenu>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))

    const item = await screen.findByTestId('document-action-manage-access')
    // The action item is live (not aria-disabled); clicking opens the dialog.
    expect(item).not.toHaveAttribute('aria-disabled', 'true')
    await user.click(item)

    const dialog = await screen.findByTestId('manage-access-dialog')
    expect(dialog).toHaveAttribute('data-rid', 'doc-1')
    expect(dialog).toHaveAttribute('data-rtype', 'document')
  })

  it('disables Delete and Move for legal-hold documents but leaves Rename enabled', async () => {
    const user = userEvent.setup()
    render(wrap(
      <DocumentActionsMenu doc={{ ...doc, lifecycle_state: 'legal_hold' }}>
        <div>card</div>
      </DocumentActionsMenu>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))

    const deleteItem = await screen.findByTestId('document-action-delete')
    const moveItem = screen.getByTestId('document-action-move')
    const renameItem = screen.getByTestId('document-action-rename')

    // Backend IsLegalHoldBlocked blocks delete and move, but explicitly
    // allows update_title — so rename must stay enabled.
    expect(deleteItem).toHaveAttribute('aria-disabled', 'true')
    expect(moveItem).toHaveAttribute('aria-disabled', 'true')
    expect(renameItem).not.toHaveAttribute('aria-disabled', 'true')
  })

  // Regression — deletion of a document under legal hold returns
  // 423 Locked with a {error, code:"LEGAL_HOLD"} envelope. The
  // useDeleteDocument hook's onError reads the envelope via
  // readErrorMessage and toasts the actual reason — not a generic
  // "Could not delete". The api/client.ts response interceptor has
  // no specific 423 branch, so this whole path runs through the
  // mutation's onError.
  it('Delete that fails with 423 LEGAL_HOLD surfaces the backend message in the toast', async () => {
    const { toast } = await import('sonner')
    const user = userEvent.setup()
    deleteDocumentMock.mockRejectedValue({
      response: {
        status: 423,
        data: { code: 'LEGAL_HOLD', error: 'document is under legal hold' },
      },
    })

    render(wrap(
      <DocumentActionsMenu doc={doc}>
        <div>card</div>
      </DocumentActionsMenu>,
    ))
    await user.click(screen.getByRole('button', { name: 'Document actions' }))
    await user.click(await screen.findByTestId('document-action-delete'))

    const alert = await screen.findByRole('alertdialog')
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())
    const confirm = within(alert).getByRole('button', { name: 'Delete' })
    fireEvent.click(confirm)

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('document is under legal hold'))
  })
})
