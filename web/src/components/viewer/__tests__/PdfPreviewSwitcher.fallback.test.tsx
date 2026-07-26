import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { DocumentPreview } from '@/components/viewer/DocumentPreview'

// Regression guard: the PDF preview defaulted to the `watermarked`
// rendition unconditionally, so a document with no watermarked pages
// greeted the user with the "No watermarked preview" empty state even
// though the original renders fine. The switcher must derive its
// default from watermark availability (explicit user clicks still win).

vi.mock('@/api/documents', () => ({
  getDownloadURL: vi.fn().mockResolvedValue({ url: 'http://localhost/file.pdf' }),
}))
vi.mock('@/api/watermark', () => ({
  getWatermarkStatus: vi.fn(),
}))

import { getWatermarkStatus } from '@/api/watermark'

function renderPreview() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <DocumentPreview documentId="d1" versionId="v1" mimeType="application/pdf" title="f.pdf" />
    </QueryClientProvider>,
  )
}

describe('PdfPreviewSwitcher watermark fallback', () => {
  it('starts in Original mode when no watermarked pages exist', async () => {
    vi.mocked(getWatermarkStatus).mockResolvedValue({ status: 'none', page_count: 0 } as never)
    renderPreview()

    const original = await screen.findByTestId('pdf-mode-original')
    await waitFor(() => expect(original.getAttribute('aria-pressed')).toBe('true'))
    expect(screen.queryByTestId('wm-preview-empty')).toBeNull()
  })

  it('keeps Watermarked as the default when pages exist', async () => {
    vi.mocked(getWatermarkStatus).mockResolvedValue({ status: 'ready', page_count: 3 } as never)
    renderPreview()

    const watermarked = await screen.findByTestId('pdf-mode-watermarked')
    expect(watermarked.getAttribute('aria-pressed')).toBe('true')
  })
})
