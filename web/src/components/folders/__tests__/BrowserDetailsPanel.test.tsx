import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/renderWithProviders'
import { BrowserDetailsPanel } from '@/components/folders/BrowserDetailsPanel'
import type { Document, Folder } from '@/types/api'

const activityMock = vi.hoisted(() => vi.fn())
vi.mock('@/hooks/useDocumentDetailGQL', () => ({
  useActivityForDocument: activityMock,
}))

const auditMock = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin', () => ({
  getAuditLog: auditMock,
}))

const roleMock = vi.hoisted(() => ({ role: 'owner' as string | undefined }))
vi.mock('@/store/authStore', () => ({
  useAuthStore: (sel: (st: { user: { role?: string } }) => unknown) =>
    sel({ user: { role: roleMock.role } }),
}))

activityMock.mockReturnValue({ data: { nodes: [] }, isLoading: false, isError: false })
auditMock.mockResolvedValue({ events: [] })

const doc = {
  id: 'd1',
  title: 'contract.pdf',
  mime_type: 'application/pdf',
  total_size_bytes: '1024',
  version_count: 1,
  created_by_name: 'Ada',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-02T00:00:00Z',
  tags: [],
} as unknown as Document

const folder = {
  id: 'f1',
  name: 'Contracts',
  visibility: 'shared',
  document_count: '1',
  child_folder_count: '0',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-02T00:00:00Z',
} as unknown as Folder

describe('<BrowserDetailsPanel>', () => {
  it('renders nothing when there is no selection', () => {
    const { container } = renderWithProviders(
      <BrowserDetailsPanel selection={null} onOpenFolder={() => {}} onOpenFile={() => {}} onClose={() => {}} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it('shows file details when a file is selected', () => {
    renderWithProviders(
      <BrowserDetailsPanel
        selection={{ type: 'file', doc }}
        onOpenFolder={() => {}}
        onOpenFile={() => {}}
        onClose={() => {}}
      />,
    )
    expect(screen.getByText('contract.pdf')).toBeInTheDocument()
  })

  it('close button fires onClose', async () => {
    const user = userEvent.setup()
    const spy = vi.fn()
    renderWithProviders(
      <BrowserDetailsPanel
        selection={{ type: 'file', doc }}
        onOpenFolder={() => {}}
        onOpenFile={() => {}}
        onClose={spy}
      />,
    )
    await user.click(screen.getByRole('button', { name: 'Close details' }))
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('shows recent activity for a file from the document feed', () => {
    activityMock.mockReturnValueOnce({
      data: {
        nodes: [
          { id: 'a1', kind: 'audit', summary: 'Uploaded version 2', actorName: 'Ada', occurredAt: '2026-01-02T00:00:00Z' },
          { id: 'a2', kind: 'comment', summary: 'Commented', occurredAt: '2026-01-01T12:00:00Z' },
        ],
      },
      isLoading: false,
      isError: false,
    })
    renderWithProviders(
      <BrowserDetailsPanel
        selection={{ type: 'file', doc }}
        onOpenFolder={() => {}}
        onOpenFile={() => {}}
        onClose={() => {}}
      />,
    )
    expect(screen.getByText('Recent activity')).toBeInTheDocument()
    expect(screen.getByText('Uploaded version 2')).toBeInTheDocument()
    expect(screen.getByText(/Ada,/)).toBeInTheDocument()
  })

  it('shows folder activity from the audit log for an owner', async () => {
    auditMock.mockResolvedValueOnce({
      events: [
        { id: 'e1', action: 'folder.visibility_changed', actor_name: 'Ada', created_at: '2026-01-02T00:00:00Z' },
      ],
    })
    renderWithProviders(
      <BrowserDetailsPanel
        selection={{ type: 'folder', folder }}
        onOpenFolder={() => {}}
        onOpenFile={() => {}}
        onClose={() => {}}
      />,
    )
    expect(await screen.findByText('folder visibility changed')).toBeInTheDocument()
  })

  it('hides folder activity entirely for a non-admin member', () => {
    roleMock.role = 'member'
    auditMock.mockClear()
    try {
      renderWithProviders(
        <BrowserDetailsPanel
          selection={{ type: 'folder', folder }}
          onOpenFolder={() => {}}
          onOpenFile={() => {}}
          onClose={() => {}}
        />,
      )
      expect(screen.queryByText('Recent activity')).not.toBeInTheDocument()
      expect(auditMock).not.toHaveBeenCalled()
    } finally {
      roleMock.role = 'owner'
    }
  })
})
