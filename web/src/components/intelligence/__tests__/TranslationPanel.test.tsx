import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { TranslationPanel } from '@/components/intelligence/TranslationPanel'
import {
  getDocumentLanguage,
  getTranslationText,
  listTranslations,
  requestTranslation,
  type Translation,
} from '@/api/translation'
import { renderWithProviders } from '@/test/renderWithProviders'

vi.mock('@/api/translation', () => ({
  getDocumentLanguage: vi.fn(),
  getTranslationText: vi.fn(),
  listTranslations: vi.fn(),
  requestTranslation: vi.fn(),
}))

vi.mock('@/components/viewer/DocumentPreview', () => ({
  DocumentPreview: () => <div data-testid="stub-preview" />,
}))

const translation = (over: Partial<Translation> = {}): Translation => ({
  id: 'tr-1',
  version_id: 'ver-1',
  source_language: 'en',
  target_language: 'ar',
  status: 'completed',
  model_used: 'openai/gpt-4o-mini',
  word_count: 127,
  tokens_used: 900,
  error_message: '',
  created_at: '2026-08-01T10:00:00Z',
  completed_at: '2026-08-01T10:00:30Z',
  ...over,
})

function renderPanel() {
  return renderWithProviders(
    <TranslationPanel
      documentId="doc-1"
      versionId="ver-1"
      mimeType="application/pdf"
      title="invoice-2026-03.pdf"
    />,
  )
}

beforeEach(() => {
  vi.mocked(getDocumentLanguage).mockResolvedValue({ detected_language: 'en', confidence: 0.98 })
  vi.mocked(listTranslations).mockResolvedValue([translation()])
  vi.mocked(getTranslationText).mockResolvedValue({
    status: 'completed',
    source_language: 'en',
    target_language: 'ar',
    translated_text: 'الدفع عبر الإنترنت',
    model_used: 'openai/gpt-4o-mini',
    word_count: 127,
    error_message: '',
  })
  vi.mocked(requestTranslation).mockResolvedValue({
    translation_id: 'tr-2',
    status: 'pending',
    target_language: 'fr',
    deduplicated: false,
  })
})

describe('<TranslationPanel>', () => {
  it('shows the detected language with its confidence', async () => {
    renderPanel()
    expect(await screen.findByText('English')).toBeInTheDocument()
    expect(screen.getByText('98% confident')).toBeInTheDocument()
  })

  it('lists completed translations with word count and a View action', async () => {
    renderPanel()
    expect(await screen.findByText('Arabic')).toBeInTheDocument()
    expect(screen.getByText('Completed')).toBeInTheDocument()
    expect(screen.getByText('127 words')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'View' })).toBeInTheDocument()
  })

  it('opens the side-by-side viewer from a row', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(await screen.findByRole('button', { name: 'View' }))
    expect(await screen.findByTestId('translation-viewer')).toBeInTheDocument()
    expect(screen.getByTestId('stub-preview')).toBeInTheDocument()
  })

  it('offers Retry instead of View when a translation failed', async () => {
    vi.mocked(listTranslations).mockResolvedValue([
      translation({ status: 'failed', error_message: 'model quota exceeded' }),
    ])
    const user = userEvent.setup()
    renderPanel()
    expect(await screen.findByText('model quota exceeded')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'View' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Retry/ }))
    expect(requestTranslation).toHaveBeenCalledWith({
      document_id: 'doc-1',
      version_id: 'ver-1',
      target_language: 'ar',
    })
  })

  it('requests a new translation for the picked language', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(await screen.findByRole('combobox'))
    await user.click(await screen.findByRole('option', { name: 'French' }))
    await user.click(screen.getByRole('button', { name: 'Translate' }))
    expect(requestTranslation).toHaveBeenCalledWith({
      document_id: 'doc-1',
      version_id: 'ver-1',
      target_language: 'fr',
    })
  })

  it('excludes the detected language and already-translated ones from the picker', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(await screen.findByRole('combobox'))
    // 'en' is the detected language, 'ar' already has a translation.
    expect(screen.queryByRole('option', { name: 'English' })).not.toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Arabic' })).not.toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'German' })).toBeInTheDocument()
  })

  it('ignores translations belonging to another version', async () => {
    vi.mocked(listTranslations).mockResolvedValue([
      translation({ id: 'tr-old', version_id: 'ver-0', target_language: 'fr' }),
    ])
    renderPanel()
    expect(await screen.findByText('No translations yet.')).toBeInTheDocument()
  })
})
