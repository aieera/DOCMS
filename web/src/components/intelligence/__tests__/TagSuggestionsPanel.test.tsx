import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { TagSuggestionsPanel } from '@/components/intelligence/TagSuggestionsPanel'
import { listTagSuggestions, reviewTagSuggestions } from '@/api/intelligence'

vi.mock('@/api/intelligence', () => ({
  listTagSuggestions: vi.fn(),
  reviewTagSuggestions: vi.fn(),
}))

const sug = (id: string, tag: string, confidence = 0.9, status = 'pending') => ({
  id, tag_name: tag, confidence, status, source: 'ner' as const,
})

const manyPending = [
  sug('1', 'party_name:acme'),
  sug('2', 'party_name:raabyt'),
  sug('3', 'party_name:artiflex'),
  sug('4', 'name:mouse'),
  sug('5', 'name:keyboard'),
  sug('6', 'jurisdiction:dubai'),
  sug('7', 'effective_date:2026-08-15'),
]

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TagSuggestionsPanel documentId="doc-1" />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(listTagSuggestions).mockResolvedValue({ suggestions: manyPending } as never)
  vi.mocked(reviewTagSuggestions).mockResolvedValue({ accepted_count: 0, rejected_count: 0 } as never)
})

describe('<TagSuggestionsPanel>', () => {
  it('groups pending suggestions by category with counts, collapsed when long', async () => {
    renderPanel()
    expect(await screen.findByText('party_name')).toBeInTheDocument()
    expect(screen.getByText('(3)')).toBeInTheDocument()
    expect(screen.getByText('name')).toBeInTheDocument()
    expect(screen.getByText('jurisdiction')).toBeInTheDocument()
    // collapsed: individual rows hidden until the group is expanded
    expect(screen.queryByText('party_name:acme')).not.toBeInTheDocument()
  })

  it('expanding a group reveals its rows', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(await screen.findByRole('button', { name: 'party_name (3)' }))
    expect(screen.getByText('party_name:acme')).toBeInTheDocument()
    expect(screen.getByText('party_name:raabyt')).toBeInTheDocument()
    expect(screen.queryByText('name:mouse')).not.toBeInTheDocument()
  })

  it('group dismiss rejects exactly that group', async () => {
    const user = userEvent.setup()
    renderPanel()
    await screen.findByText('party_name')
    await user.click(screen.getByRole('button', { name: 'Dismiss all name suggestions' }))
    expect(reviewTagSuggestions).toHaveBeenCalledWith('doc-1', [
      { suggestion_id: '4', action: 'reject' },
      { suggestion_id: '5', action: 'reject' },
    ])
  })

  it('shows few suggestions expanded without group chrome collapse', async () => {
    vi.mocked(listTagSuggestions).mockResolvedValue({
      suggestions: [sug('1', 'party_name:acme'), sug('6', 'jurisdiction:dubai')],
    } as never)
    renderPanel()
    // small sets render rows directly visible
    expect(await screen.findByText('party_name:acme')).toBeInTheDocument()
    expect(screen.getByText('jurisdiction:dubai')).toBeInTheDocument()
  })
})
