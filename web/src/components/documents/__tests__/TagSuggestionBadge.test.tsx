import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/renderWithProviders'
import { TagSuggestionBadge } from '@/components/documents/TagSuggestionBadge'
import { listTagSuggestions, reviewTagSuggestions } from '@/api/intelligence'
import type { TagSuggestion } from '@/api/intelligence'

// The workspace-grid surface for pending AI tags: a small "N suggested"
// pill per document with one-click accept/reject in a popover. Zero
// pending → no pill (grids must stay clean).

vi.mock('@/api/intelligence', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/intelligence')>()),
  listTagSuggestions: vi.fn(),
  reviewTagSuggestions: vi.fn(),
}))

function suggestion(over: Partial<TagSuggestion>): TagSuggestion {
  return {
    id: 's1',
    document_id: 'd1',
    version_id: 'v1',
    tag_name: 'invoice',
    source: 'ner',
    source_detail: {},
    confidence: 0.9,
    status: 'pending',
    created_at: '2026-07-26T00:00:00Z',
    ...over,
  }
}

beforeEach(() => vi.clearAllMocks())

describe('TagSuggestionBadge', () => {
  it('renders nothing when no suggestions are pending', async () => {
    vi.mocked(listTagSuggestions).mockResolvedValue({
      suggestions: [suggestion({ status: 'auto_applied' })],
    })
    const { container } = renderWithProviders(<TagSuggestionBadge documentId="d1" />)
    await waitFor(() => expect(listTagSuggestions).toHaveBeenCalledWith('d1'))
    expect(container.firstChild).toBeNull()
  })

  it('shows the pending count', async () => {
    vi.mocked(listTagSuggestions).mockResolvedValue({
      suggestions: [
        suggestion({ id: 's1', tag_name: 'invoice' }),
        suggestion({ id: 's2', tag_name: 'finance' }),
        suggestion({ id: 's3', status: 'rejected' }),
      ],
    })
    renderWithProviders(<TagSuggestionBadge documentId="d1" />)
    expect(await screen.findByText(/2 suggested/)).toBeTruthy()
  })

  it('accepting a suggestion calls the review API with that id', async () => {
    vi.mocked(listTagSuggestions).mockResolvedValue({
      suggestions: [suggestion({ id: 's1', tag_name: 'invoice' })],
    })
    vi.mocked(reviewTagSuggestions).mockResolvedValue({
      accepted_count: 1,
      rejected_count: 0,
      accepted_tags: ['invoice'],
      rejected_tags: [],
    })
    renderWithProviders(<TagSuggestionBadge documentId="d1" />)

    await userEvent.click(await screen.findByText(/1 suggested/))
    await userEvent.click(await screen.findByRole('button', { name: /accept tag invoice/i }))

    await waitFor(() =>
      expect(reviewTagSuggestions).toHaveBeenCalledWith('d1', [
        { suggestion_id: 's1', action: 'accept' },
      ]),
    )
  })
})
