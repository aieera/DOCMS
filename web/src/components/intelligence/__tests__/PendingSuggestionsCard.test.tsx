import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { renderWithProviders } from '@/test/renderWithProviders'
import { PendingSuggestionsCard } from '@/components/intelligence/PendingSuggestionsCard'
import { listPendingTagSuggestions } from '@/api/intelligence'

// The dashboard card that finally makes the async auto-tag pipeline
// visible: N pending AI suggestions, linking to the review queue.
// It must vanish entirely (not error) for users who can't call the
// /admin endpoint and when there is nothing to review.

vi.mock('@/api/intelligence', () => ({
  listPendingTagSuggestions: vi.fn(),
}))

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, ...rest }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...rest}>{children}</a>
  ),
}))

beforeEach(() => vi.clearAllMocks())

describe('PendingSuggestionsCard', () => {
  it('shows the pending count and links to the review queue', async () => {
    vi.mocked(listPendingTagSuggestions).mockResolvedValue({
      suggestions: [],
      total: 7,
      limit: 1,
      offset: 0,
    })
    renderWithProviders(<PendingSuggestionsCard />)

    expect(await screen.findByText('7')).toBeTruthy()
    const link = screen.getByRole('link')
    expect(link.getAttribute('href')).toContain('/admin/tags')
  })

  it('renders nothing when there are zero pending suggestions', async () => {
    vi.mocked(listPendingTagSuggestions).mockResolvedValue({
      suggestions: [],
      total: 0,
      limit: 1,
      offset: 0,
    })
    const { container } = renderWithProviders(<PendingSuggestionsCard />)
    await waitFor(() => expect(listPendingTagSuggestions).toHaveBeenCalled())
    expect(container.firstChild).toBeNull()
  })

  it('renders nothing on error (non-admin 403)', async () => {
    vi.mocked(listPendingTagSuggestions).mockRejectedValue(new Error('403'))
    const { container } = renderWithProviders(<PendingSuggestionsCard />)
    await waitFor(() => expect(listPendingTagSuggestions).toHaveBeenCalled())
    expect(container.firstChild).toBeNull()
  })
})
