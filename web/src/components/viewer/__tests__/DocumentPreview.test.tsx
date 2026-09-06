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

// The real download alias never returns file bytes: it answers with a JSON
// envelope {"url": ...} pointing at either the same-origin decrypt-stream
// (envelope-encrypted blobs — every blob in this stack) or a presigned URL.
// The old mock returned raw text, which is exactly why QA SD-01 (text docs
// previewing as the raw JSON body) slipped past these tests.
function stubAliasFetch(fileText: string) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const u = String(input)
    if (u.endsWith('/download')) {
      return {
        ok: true,
        headers: new Headers({ 'content-type': 'application/json' }),
        json: async () => ({ url: '/api/v1/documents/d1/versions/v1/decrypt-stream' }),
        text: async () => JSON.stringify({ url: '/api/v1/documents/d1/versions/v1/decrypt-stream' }),
      } as unknown as Response
    }
    if (u.endsWith('/decrypt-stream')) {
      return {
        ok: true,
        headers: new Headers({ 'content-type': 'text/plain' }),
        text: async () => fileText,
      } as unknown as Response
    }
    throw new Error(`unexpected fetch ${u}`)
  }))
}

beforeEach(() => {
  stubAliasFetch('# Hello\n\nworld')
})

it('renders markdown inline for text/markdown', async () => {
  wrap(<DocumentPreview documentId="d1" versionId="v1" mimeType="text/markdown" title="notes.md" />)
  // react-markdown turns "# Hello" into an <h1>
  const heading = await screen.findByRole('heading', { name: 'Hello' })
  expect(heading).toBeInTheDocument()
  expect(screen.getByText('world')).toBeInTheDocument()
})

it('renders the file text for text/plain, not the alias JSON envelope (SD-01)', async () => {
  stubAliasFetch('This clause shall indemnify the supplier.')
  wrap(<DocumentPreview documentId="d1" versionId="v1" mimeType="text/plain" title="t.txt" />)
  expect(await screen.findByText(/shall indemnify the supplier/)).toBeInTheDocument()
  // The internal stream path must never be printed as document content.
  expect(screen.queryByText(/decrypt-stream/)).not.toBeInTheDocument()
})

it('falls back to a download card for non-inlineable types', async () => {
  wrap(<DocumentPreview documentId="d1" versionId="v1" mimeType="application/zip" title="a.zip" />)
  expect(await screen.findByTestId('preview-fallback')).toBeInTheDocument()
  expect(screen.getByText(/Download to open/i)).toBeInTheDocument()
})
