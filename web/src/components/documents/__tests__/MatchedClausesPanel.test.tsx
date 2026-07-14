import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MatchedClausesPanel } from '@/components/documents/MatchedClausesPanel'

const matches = [
  {
    clause_id: 'c-1', clause_name: 'Confidentiality — Standard',
    jurisdiction: 'UAE', approved: true, similarity: 0.92,
    chunk_index: 3, matched_text: 'Each party shall keep confidential…',
    detected_at: new Date().toISOString(),
  },
]

vi.mock('@/api/clauses', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  getDocumentClauseMatches: vi.fn(async () => matches),
}))

function wrap(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>
}

describe('<MatchedClausesPanel>', () => {
  beforeEach(() => {
    Object.assign(navigator, { clipboard: { writeText: vi.fn(async () => {}) } })
  })

  it('renders matches with similarity and approval badge', async () => {
    render(wrap(<MatchedClausesPanel documentId="d-1" />))
    expect(await screen.findByText('Confidentiality — Standard')).toBeInTheDocument()
    expect(screen.getByText('92%')).toBeInTheDocument()
    expect(screen.getByText(/approved/i)).toBeInTheDocument()
  })

  it('copies the clause body on Copy click', async () => {
    const user = userEvent.setup({ writeToClipboard: false })
    // userEvent.setup() unconditionally replaces navigator.clipboard with
    // its own internal stub (see @testing-library/user-event's Clipboard.js
    // attachClipboardStubToView), clobbering the beforeEach-installed
    // vi.fn() before the click fires. Spy on the stub in place so the
    // assertion below observes the call.
    vi.spyOn(navigator.clipboard, 'writeText')
    render(wrap(<MatchedClausesPanel documentId="d-1" />))
    await user.click(await screen.findByRole('button', { name: /copy/i }))
    expect(navigator.clipboard.writeText).toHaveBeenCalled()
  })
})
