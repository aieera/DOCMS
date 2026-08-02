import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { TranslationViewer } from '@/components/intelligence/TranslationViewer'
import { getTranslationText, type TranslationText } from '@/api/translation'
import { renderWithProviders } from '@/test/renderWithProviders'

vi.mock('@/api/translation', () => ({
  getTranslationText: vi.fn(),
}))

// The left pane is the real preview component; stub it so the test doesn't
// depend on presigned-download plumbing.
vi.mock('@/components/viewer/DocumentPreview', () => ({
  DocumentPreview: ({ documentId }: { documentId: string }) => (
    <div data-testid="stub-preview">preview:{documentId}</div>
  ),
}))

const text = (over: Partial<TranslationText> = {}): TranslationText => ({
  status: 'completed',
  source_language: 'en',
  target_language: 'ar',
  translated_text: 'الدفع عبر الإنترنت',
  model_used: 'openai/gpt-4o-mini',
  word_count: 127,
  error_message: '',
  ...over,
})

function renderViewer() {
  return renderWithProviders(
    <TranslationViewer
      translationId="tr-1"
      documentId="doc-1"
      versionId="ver-1"
      mimeType="application/pdf"
      title="invoice-2026-03.pdf"
      onClose={vi.fn()}
    />,
  )
}

beforeEach(() => {
  vi.mocked(getTranslationText).mockResolvedValue(text())
})

describe('<TranslationViewer>', () => {
  it('renders the original and the translation side by side', async () => {
    renderViewer()
    expect(await screen.findByText('الدفع عبر الإنترنت')).toBeInTheDocument()
    // Both panes are populated — the regression was a two-column grid with
    // only the translation filled, leaving half the dialog blank.
    expect(screen.getByTestId('stub-preview')).toHaveTextContent('preview:doc-1')
    expect(screen.getByText('Original (English)')).toBeInTheDocument()
    expect(screen.getByText('Translation (Arabic)')).toBeInTheDocument()
  })

  it('marks RTL target languages with dir and lang', async () => {
    renderViewer()
    const article = await screen.findByTestId('translation-text')
    expect(article).toHaveAttribute('dir', 'rtl')
    expect(article).toHaveAttribute('lang', 'ar')
  })

  it('keeps LTR targets in ltr', async () => {
    vi.mocked(getTranslationText).mockResolvedValue(
      text({ target_language: 'fr', translated_text: 'Paiement en ligne' }),
    )
    renderViewer()
    expect(await screen.findByTestId('translation-text')).toHaveAttribute('dir', 'ltr')
  })

  it('shows word count and model in the header', async () => {
    renderViewer()
    expect(await screen.findByText(/127 words/)).toBeInTheDocument()
    expect(screen.getByText('openai/gpt-4o-mini')).toBeInTheDocument()
  })

  it('surfaces the error message when the translation failed', async () => {
    vi.mocked(getTranslationText).mockResolvedValue(
      text({ status: 'failed', translated_text: '', error_message: 'model quota exceeded' }),
    )
    renderViewer()
    expect(await screen.findByText('model quota exceeded')).toBeInTheDocument()
    expect(screen.queryByTestId('translation-text')).not.toBeInTheDocument()
  })

  it('is a labelled dialog that closes from the header button', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderWithProviders(
      <TranslationViewer
        translationId="tr-1"
        documentId="doc-1"
        versionId="ver-1"
        title="invoice-2026-03.pdf"
        onClose={onClose}
      />,
    )
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveAccessibleName('invoice-2026-03.pdf — translation')
    await user.click(screen.getByTestId('translation-viewer-close'))
    expect(onClose).toHaveBeenCalled()
  })
})
