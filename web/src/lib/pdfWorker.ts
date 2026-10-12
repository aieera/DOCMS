// W-1: one bundled pdf.js worker for every react-pdf viewer.
//
// The viewers used to point GlobalWorkerOptions.workerSrc at unpkg, which
// violates the app CSP once enforced, executes third-party code against
// decrypted PDFs, and breaks air-gapped tenants. Vite's `?url` import
// emits the worker as a same-origin hashed asset instead.
//
// pdfjs-dist is pinned in package.json to the exact version react-pdf
// depends on, so this worker always matches the API react-pdf runs
// (src/lib/__tests__/pdfWorker.test.ts guards that).
import { pdfjs } from 'react-pdf'
import workerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url'

export const PDF_WORKER_SRC: string = workerUrl

pdfjs.GlobalWorkerOptions.workerSrc = PDF_WORKER_SRC
