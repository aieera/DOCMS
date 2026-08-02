import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import ReactMarkdown from 'react-markdown'
import { AlertCircle, Download, ExternalLink, FileText, Stamp } from 'lucide-react'

import { getDownloadURL } from '@/api/documents'
import { getWatermarkStatus } from '@/api/watermark'
import { FileIcon } from '@/components/ui/FileIcon'
import { Spinner } from '@/components/ui/Spinner'
import { Card } from '@/components/ui/card'
import { cn } from '@/lib/cn'
import { WatermarkedPreview } from './WatermarkedPreview'

interface Props {
  documentId: string
  versionId?: string
  mimeType?: string
  title?: string
}

// DocumentPreview renders the document body inline using a presigned
// URL fetched from /storage/downloads/{doc}/{version}. Per ADR 0074
// blueprint §17, the inline viewer is the default tab on the doc
// detail page; we picked native browser viewers (iframe / <img> /
// <video>) over a heavier PDF library to keep the bundle small.
//
//   - PDF                → <iframe src=…#toolbar=0> — Chrome/Firefox/
//                           Safari all ship a PDF renderer.
//   - image/*           → <img>
//   - video/*           → <video controls>
//   - audio/*           → <audio controls>
//   - everything else   → fall-through card with a Download CTA so the
//                          user can open the file in a native app.
//
// The signed URL has a short expiry (~10 min server-side); React
// Query refetches when the tab is reactivated so we don't show a
// stale URL.
export function DocumentPreview({ documentId, versionId, mimeType, title }: Props) {
  const dl = useQuery({
    queryKey: ['download-url', documentId, versionId],
    queryFn: () => getDownloadURL(documentId, versionId ?? ''),
    enabled: !!versionId,
    // The signed URL expires in ~10 min; refetch on focus so a tab
    // that's been parked for a while still works.
    refetchOnWindowFocus: true,
    staleTime: 5 * 60_000,
  })

  if (!versionId) {
    return (
      <Card className="flex flex-col items-center justify-center gap-2 p-12 text-center text-sm text-muted-foreground">
        <FileIcon mime={mimeType} className="h-12 w-12" />
        <p className="text-base font-medium text-foreground">{title ?? 'Document'}</p>
        <p>No file uploaded yet — preview will appear once content is added.</p>
      </Card>
    )
  }

  if (dl.isLoading) {
    return (
      <Card
        className="flex h-[60vh] flex-col items-center justify-center gap-3 bg-gradient-to-br from-muted/40 to-muted/10"
        data-testid="preview-loading"
        aria-busy
        aria-label="Loading preview"
      >
        <div className="relative h-12 w-9 rounded-sm border border-border bg-background shadow-sm">
          <div className="absolute inset-x-2 top-2 h-1 rounded bg-muted-foreground/30" />
          <div className="absolute inset-x-2 top-4 h-1 rounded bg-muted-foreground/20" />
          <div className="absolute inset-x-2 top-6 h-1 w-5 rounded bg-muted-foreground/20" />
          <Spinner className="absolute -end-3 -bottom-3 h-5 w-5" />
        </div>
        <p className="text-xs text-muted-foreground">Preparing preview…</p>
      </Card>
    )
  }

  if (dl.isError || !dl.data?.url) {
    return (
      <Card className="flex flex-col items-center justify-center gap-2 p-12 text-center text-sm" data-testid="preview-error">
        <AlertCircle className="h-10 w-10 text-destructive" />
        <p className="text-base font-medium text-foreground">Could not load preview</p>
        <p className="text-muted-foreground">
          {dl.error instanceof Error ? dl.error.message : 'The signed download URL could not be fetched.'}
        </p>
      </Card>
    )
  }

  const url = dl.data.url
  const mime = (mimeType ?? '').toLowerCase()

  if (mime === 'application/pdf' || mime.endsWith('/pdf')) {
    // versionId is guaranteed defined here — the top-of-component guard
    // returns early when it's missing.
    return <PdfPreviewSwitcher documentId={documentId} versionId={versionId!} url={url} title={title} />
  }

  if (mime.startsWith('image/')) {
    return (
      <Card className="flex items-center justify-center bg-muted/40 p-2" data-testid="preview-image">
        <img
          src={url}
          alt={title ?? 'Document preview'}
          className="max-h-[80vh] w-auto rounded object-contain"
        />
      </Card>
    )
  }

  if (mime.startsWith('video/')) {
    return (
      <Card className="overflow-hidden p-0" data-testid="preview-video">
        <video
          src={url}
          controls
          preload="metadata"
          className="max-h-[80vh] w-full"
          aria-label={title}
        />
      </Card>
    )
  }

  if (mime.startsWith('audio/')) {
    return (
      <Card className="flex flex-col items-center justify-center gap-3 p-12" data-testid="preview-audio">
        <FileIcon mime={mime} className="h-12 w-12" />
        <p className="text-base font-medium text-foreground">{title ?? 'Audio'}</p>
        <audio src={url} controls className="w-full max-w-md" />
      </Card>
    )
  }

  // Text: render markdown / plain text inline instead of forcing a download.
  // Uses the same-origin, cookie-authed download alias (not the presigned S3
  // URL) so the fetch isn't blocked by cross-origin CORS.
  if (mime === 'text/markdown' || mime === 'text/x-markdown' || mime.startsWith('text/')) {
    return (
      <TextPreview
        documentId={documentId}
        versionId={versionId!}
        mime={mime}
        title={title}
        downloadUrl={url}
      />
    )
  }

  // Fallback: anything else (Office docs, archives, custom MIME types)
  // can't be inlined safely; surface a Download CTA instead of a
  // blank box.
  return (
    <Card className="flex flex-col items-center justify-center gap-3 p-12 text-center text-sm" data-testid="preview-fallback">
      <FileText className="h-10 w-10 text-muted-foreground" />
      <p className="text-base font-medium text-foreground">{title ?? 'Document'}</p>
      <p className="text-muted-foreground">
        Inline preview isn't available for <code className="rounded bg-muted px-1">{mime || 'this type'}</code>.
      </p>
      <a
        href={url}
        target="_blank"
        rel="noreferrer"
        className="inline-flex h-9 items-center rounded-md bg-foreground px-3 text-sm font-medium text-background hover:opacity-90"
        download
      >
        Download to open
      </a>
    </Card>
  )
}

// ---- Text / Markdown preview --------------------------------------------
//
// Fetches the document body from the same-origin download alias and renders it
// inline: markdown through react-markdown (raw HTML disabled, so no XSS from
// document content), everything else as wrapped monospaced text. Falls back to
// the shared download card on error. A hard cap keeps a huge text file from
// freezing the tab.
const TEXT_PREVIEW_CAP = 2 * 1024 * 1024 // 2 MiB

function TextPreview({
  documentId,
  versionId,
  mime,
  title,
  downloadUrl,
}: {
  documentId: string
  versionId: string
  mime: string
  title?: string
  downloadUrl: string
}) {
  const q = useQuery({
    queryKey: ['text-preview', documentId, versionId],
    queryFn: async () => {
      const res = await fetch(`/api/v1/documents/${documentId}/versions/${versionId}/download`, {
        credentials: 'include',
      })
      if (!res.ok) throw new Error(`Failed to load text (${res.status})`)
      const text = await res.text()
      return text.length > TEXT_PREVIEW_CAP
        ? { text: text.slice(0, TEXT_PREVIEW_CAP), truncated: true }
        : { text, truncated: false }
    },
    staleTime: 5 * 60_000,
  })

  if (q.isLoading) {
    return (
      <Card className="flex h-[40vh] items-center justify-center gap-2" data-testid="preview-text-loading" aria-busy>
        <Spinner className="h-5 w-5" />
        <span className="text-xs text-muted-foreground">Loading preview…</span>
      </Card>
    )
  }
  if (q.isError || q.data == null) {
    return <PreviewUnavailable url={downloadUrl} title={title} kind="text" />
  }

  const isMarkdown =
    mime === 'text/markdown' || mime === 'text/x-markdown' || (title ?? '').toLowerCase().endsWith('.md')

  return (
    <Card className="max-h-[80vh] overflow-auto p-6" data-testid="preview-text">
      {isMarkdown ? (
        <div className="prose prose-sm max-w-none dark:prose-invert">
          <ReactMarkdown>{q.data.text}</ReactMarkdown>
        </div>
      ) : (
        <pre className="whitespace-pre-wrap break-words font-mono text-sm text-foreground">{q.data.text}</pre>
      )}
      {q.data.truncated && (
        <p className="mt-4 border-t border-border pt-3 text-xs text-muted-foreground">
          Preview truncated at 2&nbsp;MB —{' '}
          <a href={downloadUrl} download className="font-medium text-primary underline underline-offset-2">
            download the full file
          </a>
          .
        </p>
      )}
    </Card>
  )
}

// ---- PDF preview: watermarked (default) vs. original --------------------
//
// PdfPreviewSwitcher makes the server-rendered, per-viewer watermarked
// pages the DEFAULT view for PDFs, with a toggle back to the "Original"
// native iframe render. The original path preserves the react-pdf /
// annotation / OCR-overlay workflows (PDFLayoutViewer lives on the OCR
// tab and is unaffected — this toggle only governs the Preview tab's PDF
// surface). The watermark is burned server-side so it can't be stripped
// client-side.
function PdfPreviewSwitcher({
  documentId,
  versionId,
  url,
  title,
}: {
  documentId: string
  versionId: string
  url: string
  title?: string
}) {
  // userChoice = an explicit toggle click; until then the default derives
  // from watermark availability. Unconditionally defaulting to
  // 'watermarked' greeted every document without a watermarked rendition
  // with the "No watermarked preview" empty state instead of just showing
  // the original. Same queryKey as WatermarkedPreview so react-query
  // serves both components from a single fetch.
  const [userChoice, setUserChoice] = useState<'watermarked' | 'original' | null>(null)
  const statusQ = useQuery({
    queryKey: ['wm-status', documentId, versionId],
    queryFn: () => getWatermarkStatus(documentId, versionId),
  })
  // Availability is POSITIVE-gated: only default to the watermarked
  // rendition once the status query confirms it actually has pages.
  // The old check defaulted to 'watermarked' whenever status was
  // merely unknown (still loading, or an error), so first paint of a
  // document without a rendition was the "No watermarked preview"
  // empty state — an empty box while the real document sat one click
  // away. Unknown now falls back to the original.
  const wmAvailable =
    statusQ.data != null &&
    statusQ.data.status !== 'none' &&
    statusQ.data.status !== 'failed' &&
    (statusQ.data.page_count ?? 0) > 0
  const wmPending = statusQ.data?.status === 'processing'
  const mode = userChoice ?? (wmAvailable ? 'watermarked' : 'original')
  const setMode = setUserChoice

  // Decide BEFORE first paint. Rendering a default while availability is
  // still unknown means either greeting the user with an empty
  // "No watermarked preview" box, or swapping the view out from under
  // them a moment later. A brief spinner beats both.
  if (statusQ.isLoading) {
    return (
      <Card
        className="flex h-[60vh] items-center justify-center gap-2 bg-muted/20"
        data-testid="pdf-preview-resolving"
        aria-busy
      >
        <Spinner className="h-5 w-5" />
        <span className="text-xs text-muted-foreground">Preparing preview…</span>
      </Card>
    )
  }

  return (
    <div className="space-y-2" data-testid="pdf-preview-switcher">
      {/* Segmented control: the selected segment must be unmistakable,
          not just an aria-pressed attribute. Selected = raised card
          surface + border + foreground text; unselected = flat muted. */}
      <div
        role="group"
        aria-label="Preview rendition"
        className="flex items-center justify-end gap-1 rounded-md bg-muted/60 p-1 text-xs sm:ms-auto sm:w-fit"
      >
        <SegmentButton
          selected={mode === 'watermarked'}
          onClick={() => setMode('watermarked')}
          testId="pdf-mode-watermarked"
          disabled={!wmAvailable && !wmPending}
          title={
            wmAvailable || wmPending
              ? 'Server-rendered pages stamped with your identity'
              : 'No watermarked rendition exists for this version'
          }
        >
          <Stamp className="h-3.5 w-3.5" /> Watermarked
        </SegmentButton>
        <SegmentButton
          selected={mode === 'original'}
          onClick={() => setMode('original')}
          testId="pdf-mode-original"
          title="The document exactly as uploaded"
        >
          <FileText className="h-3.5 w-3.5" /> Original
        </SegmentButton>
      </div>
      {mode === 'watermarked' ? (
        <WatermarkedPreview documentId={documentId} versionId={versionId} />
      ) : (
        <PdfPreview url={url} title={title} />
      )}
    </div>
  )
}

// One segment of the rendition switcher. Selected state is carried by
// BOTH aria-pressed (assistive tech) and a distinct visual treatment
// (everyone else) — the previous secondary/ghost pairing rendered the
// two segments indistinguishably.
function SegmentButton({
  selected, onClick, disabled, title, testId, children,
}: {
  selected: boolean
  onClick: () => void
  disabled?: boolean
  title?: string
  testId?: string
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={title}
      aria-pressed={selected}
      data-testid={testId}
      className={cn(
        'inline-flex h-7 items-center gap-1.5 rounded px-2.5 font-medium transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
        'disabled:cursor-not-allowed disabled:opacity-40',
        selected
          ? 'bg-background text-foreground shadow-sm ring-1 ring-border'
          : 'text-muted-foreground hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}

// ---- PDF preview --------------------------------------------------------
//
// The browser's built-in PDF viewer can't report content failures to us: a
// corrupt/empty PDF (or a presigned URL that 404s after the fetch already
// succeeded) renders as a blank/black frame and STILL fires `load`. So we
// (1) show a spinner overlay until the frame loads, (2) fall back to a
// download card if `load` never fires within the timeout, and (3) keep a
// persistent open/download escape hatch for the blank-frame case we can't
// detect programmatically — replacing the bare black rectangle.
function PdfPreview({ url, title }: { url: string; title?: string }) {
  const [state, setState] = useState<'loading' | 'ready' | 'failed'>('loading')

  useEffect(() => {
    if (state !== 'loading') return
    const t = setTimeout(() => setState('failed'), 12_000)
    return () => clearTimeout(t)
  }, [state])

  if (state === 'failed') return <PreviewUnavailable url={url} title={title} kind="PDF" />

  return (
    <Card className="relative overflow-hidden p-0" data-testid="preview-pdf">
      {state === 'loading' && (
        <div
          className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-2 bg-muted/40"
          aria-busy
          aria-label="Rendering PDF"
        >
          <Spinner className="h-6 w-6" />
          <p className="text-xs text-muted-foreground">Rendering PDF…</p>
        </div>
      )}
      <iframe
        // #toolbar=0 hides Chrome's toolbar; browsers that ignore it just
        // show their own controls. view=FitH makes the page fill the
        // frame's width instead of floating small and off-centre in the
        // viewer's dark letterbox. bg-muted avoids a white/black flash
        // before the document paints.
        src={`${url}#toolbar=0&navpanes=0&view=FitH`}
        title={title ?? 'PDF preview'}
        onLoad={() => setState('ready')}
        onError={() => setState('failed')}
        className="h-[80vh] w-full border-0 bg-muted/30"
      />
      {/* Neutral viewer actions. The "preview not displaying" prompt used to
          live here on every render, implying a problem even when the PDF was
          fine; a genuine render failure now routes to PreviewUnavailable. */}
      <div className="flex flex-wrap items-center justify-end gap-x-4 gap-y-1 border-t border-border bg-card px-3 py-1.5 text-xs">
        <a
          href={url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 font-medium text-foreground transition-colors hover:text-primary"
        >
          <ExternalLink className="h-3.5 w-3.5" /> Open in new tab
        </a>
        <a
          href={url}
          download
          className="inline-flex items-center gap-1 font-medium text-foreground transition-colors hover:text-primary"
        >
          <Download className="h-3.5 w-3.5" /> Download
        </a>
      </div>
    </Card>
  )
}

// Shared "can't show this inline" state with open + download actions.
function PreviewUnavailable({ url, title, kind }: { url: string; title?: string; kind: string }) {
  return (
    <Card
      className="flex h-[60vh] flex-col items-center justify-center gap-3 p-12 text-center text-sm"
      data-testid="preview-unavailable"
    >
      <FileText className="h-10 w-10 text-muted-foreground" />
      <p className="text-base font-medium text-foreground">{title ?? 'Document'}</p>
      <p className="text-muted-foreground">The {kind} preview couldn’t be displayed in the browser.</p>
      <div className="mt-1 flex flex-wrap items-center justify-center gap-2">
        <a
          href={url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex h-9 items-center gap-1.5 rounded-md border border-border px-3 text-sm font-medium transition-colors hover:bg-accent"
        >
          <ExternalLink className="h-4 w-4" /> Open in new tab
        </a>
        <a
          href={url}
          download
          className="inline-flex h-9 items-center gap-1.5 rounded-md bg-foreground px-3 text-sm font-medium text-background transition-opacity hover:opacity-90"
        >
          <Download className="h-4 w-4" /> Download
        </a>
      </div>
    </Card>
  )
}
