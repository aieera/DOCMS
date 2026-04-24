// Per-page thumbnail strip rendered alongside the PDF viewer.
//
// Primary source: pre-rendered preview from the preview service
// (`/api/v1/previews/{documentId}/pages/{n}`). The endpoint 302's to
// a short-lived signed S3 URL; <img src> follows automatically.
//
// Fallback: when the preview isn't ready yet (preview worker still
// processing → 404), the per-thumb onError handler swaps to a
// react-pdf <Page> rendered at low width. The parent <Document> is
// shared across all fallback thumbs so the PDF is loaded once per
// sidebar rather than N times.
//
// The parent viewer already has its own <Document> for the main page
// render; the duplicate here is the acceptable cost of isolating the
// sidebar so a big PDF doesn't block the main view.

import { useState } from 'react'
import { Document, Page } from 'react-pdf'
import { Spinner } from '@/components/ui/Spinner'
import { pageThumbnailURL } from '@/lib/preview'

interface Props {
  documentId: string
  pdfUrl: string
  numPages: number
  currentPage: number
  onSelect: (page: number) => void
}

export function PageThumbnailSidebar({ documentId, pdfUrl, numPages, currentPage, onSelect }: Props) {
  return (
    <Document file={pdfUrl} loading={null} error={null}>
      <aside
        className="flex max-h-[calc(100vh-200px)] w-32 shrink-0 flex-col gap-2 overflow-y-auto border-r border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-2"
        aria-label="Page thumbnails"
      >
        {Array.from({ length: numPages }, (_, i) => i + 1).map((n) => (
          <Thumb
            key={n}
            documentId={documentId}
            page={n}
            active={currentPage === n}
            onClick={() => onSelect(n)}
          />
        ))}
      </aside>
    </Document>
  )
}

function Thumb({
  documentId, page, active, onClick,
}: { documentId: string; page: number; active: boolean; onClick: () => void }) {
  const [useFallback, setUseFallback] = useState(false)
  const url = pageThumbnailURL(documentId, page)

  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={`Go to page ${page}`}
      aria-current={active ? 'page' : undefined}
      className={`relative flex flex-col items-center overflow-hidden rounded border bg-white transition focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-primary)] ${
        active ? 'border-[var(--color-primary)] ring-1 ring-[var(--color-primary)]' : 'border-[var(--color-border)] hover:border-[var(--color-primary)]'
      }`}
    >
      {!useFallback ? (
        <img
          src={url}
          alt=""
          loading="lazy"
          className="h-40 w-full object-contain"
          onError={() => setUseFallback(true)}
        />
      ) : (
        <div className="flex h-40 w-full items-center justify-center">
          <Page
            pageNumber={page}
            width={100}
            renderTextLayer={false}
            renderAnnotationLayer={false}
            loading={<Spinner />}
            error={<span className="text-xs text-red-500">x</span>}
          />
        </div>
      )}
      <span className="w-full border-t border-[var(--color-border)] py-0.5 text-center text-[10px] text-[var(--color-text-secondary)]">
        {page}
      </span>
    </button>
  )
}
