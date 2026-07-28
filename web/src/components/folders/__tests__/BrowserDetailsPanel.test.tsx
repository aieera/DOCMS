import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BrowserDetailsPanel } from '@/components/folders/BrowserDetailsPanel'
import type { Document } from '@/types/api'

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

describe('<BrowserDetailsPanel>', () => {
  it('renders nothing when there is no selection', () => {
    const { container } = render(
      <BrowserDetailsPanel selection={null} onOpenFolder={() => {}} onOpenFile={() => {}} onClose={() => {}} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it('shows file details when a file is selected', () => {
    render(
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
    render(
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
})
