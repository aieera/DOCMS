import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SearchDropdown } from '@/components/search/SearchDropdown'
import type { SavedSearch } from '@/api/savedSearches'

const saved: SavedSearch[] = [
  { id: 's1', name: 'jasmin', query: 'jasmin', created_at: '2026-07-01', notify: false },
  { id: 's2', name: 'invoices q3', query: 'invoice after:2026-06', created_at: '2026-07-02', notify: true },
]

function setup(over: Partial<Parameters<typeof SearchDropdown>[0]> = {}) {
  const props = {
    recents: ['contract', 'gdpr policy'],
    saved,
    onRun: vi.fn(),
    onSaveRecent: vi.fn(),
    onUnsave: vi.fn(),
    onRemoveRecent: vi.fn(),
    ...over,
  }
  render(<SearchDropdown {...props} />)
  return props
}

describe('<SearchDropdown>', () => {
  it('lists recent and saved sections', () => {
    setup()
    expect(screen.getByText('Recent')).toBeInTheDocument()
    expect(screen.getByText('Saved')).toBeInTheDocument()
    expect(screen.getByText('contract')).toBeInTheDocument()
    expect(screen.getByText('jasmin')).toBeInTheDocument()
  })

  it('clicking an entry runs its query', async () => {
    const user = userEvent.setup()
    const p = setup()
    await user.click(screen.getByText('contract'))
    expect(p.onRun).toHaveBeenCalledWith('contract')
    await user.click(screen.getByText('invoices q3'))
    expect(p.onRun).toHaveBeenCalledWith('invoice after:2026-06')
  })

  it('star on a recent saves it', async () => {
    const user = userEvent.setup()
    const p = setup()
    await user.click(screen.getByRole('button', { name: 'Save search "contract"' }))
    expect(p.onSaveRecent).toHaveBeenCalledWith('contract')
    expect(p.onRun).not.toHaveBeenCalled()
  })

  it('unsave removes a saved search without running it', async () => {
    const user = userEvent.setup()
    const p = setup()
    await user.click(screen.getByRole('button', { name: 'Remove saved search "jasmin"' }))
    expect(p.onUnsave).toHaveBeenCalledWith('s1')
    expect(p.onRun).not.toHaveBeenCalled()
  })

  it('renders nothing when both lists are empty', () => {
    const { container } = render(
      <SearchDropdown recents={[]} saved={[]} onRun={vi.fn()} onSaveRecent={vi.fn()} onUnsave={vi.fn()} onRemoveRecent={vi.fn()} />,
    )
    expect(container).toBeEmptyDOMElement()
  })
})
