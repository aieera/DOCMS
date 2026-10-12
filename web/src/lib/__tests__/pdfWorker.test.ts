import { describe, it, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createRequire } from 'node:module'

// react-pdf's pdf.js 5 needs DOMMatrix, which jsdom lacks — stand in a
// minimal pdfjs object so the module's side effect is observable.
vi.mock('react-pdf', () => ({ pdfjs: { GlobalWorkerOptions: { workerSrc: '' } } }))

import { pdfjs } from 'react-pdf'
import { PDF_WORKER_SRC } from '@/lib/pdfWorker'

// W-1: the pdf.js worker was loaded from unpkg at runtime — it violates
// the app CSP once enforced, runs third-party code on decrypted PDFs and
// breaks air-gapped tenants. The worker is now a bundled module shared by
// every viewer, and must be the exact pdf.js build react-pdf's API runs
// (a 4.x worker under the 5.x API fails "API version does not match").
describe('bundled pdf.js worker (W-1)', () => {
  it('is served same-origin from the bundle, never a CDN', () => {
    expect(PDF_WORKER_SRC).toBeTruthy()
    expect(PDF_WORKER_SRC).not.toMatch(/unpkg|jsdelivr|cdnjs|^\/\/|^https?:/i)
    expect(pdfjs.GlobalWorkerOptions.workerSrc).toBe(PDF_WORKER_SRC)
  })

  it('matches the pdf.js version react-pdf runs', () => {
    const req = createRequire(import.meta.url)
    const workerPkg = req('pdfjs-dist/package.json') as { version: string }
    const reactPdf = req('react-pdf/package.json') as { dependencies: Record<string, string> }
    expect(workerPkg.version).toBe(reactPdf.dependencies['pdfjs-dist'])
  })

  it('every viewer uses the shared module and no viewer sets a remote workerSrc', () => {
    const files = [
      'src/components/viewer/PDFViewer.tsx',
      'src/components/viewer/PDFLayoutViewer.tsx',
      'src/routes/zt.$token.tsx',
    ]
    for (const f of files) {
      const src = readFileSync(resolve(__dirname, '../../..', f), 'utf8')
      expect(src, f).toContain("@/lib/pdfWorker")
      expect(src, f).not.toMatch(/workerSrc\s*=/)
      expect(src, f).not.toMatch(/unpkg/)
    }
  })
})
