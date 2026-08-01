import { it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

vi.mock('@/api/documents', () => ({
  getDownloadURL: vi.fn(async () => ({ url: 'http://example.test/dl' })),
}))
vi.mock('@/api/watermark', () => ({ getWatermarkStatus: vi.fn(async () => ({ status: 'none', page_count: 0 })) }))

import { DocumentPreview } from '../DocumentPreview'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, text: async () => '# Hello\n\nworld' }) as unknown as Response))
})

it('renders markdown inline for text/markdown', async () => {
  wrap(<DocumentPreview documentId="d1" versionId="v1" mimeType="text/markdown" title="notes.md" />)
  // react-markdown turns "# Hello" into an <h1>
  const heading = await screen.findByRole('heading', { name: 'Hello' })
  expect(heading).toBeInTheDocument()
  expect(screen.getByText('world')).toBeInTheDocument()
})

it('falls back to a download card for non-inlineable types', async () => {
  wrap(<DocumentPreview documentId="d1" versionId="v1" mimeType="application/zip" title="a.zip" />)
  expect(await screen.findByTestId('preview-fallback')).toBeInTheDocument()
  expect(screen.getByText(/Download to open/i)).toBeInTheDocument()
})
