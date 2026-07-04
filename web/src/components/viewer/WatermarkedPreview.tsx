import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertCircle, Printer, Stamp } from 'lucide-react'

import {
  getWatermarkStatus,
  wmPageUrl,
  wmDownloadUrl,
} from '@/api/watermark'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Spinner } from '@/components/ui/Spinner'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

interface Props {
  documentId: string
  versionId: string
}

// WatermarkedPreview renders the server-rendered, per-viewer watermarked
// page images for a PDF version. Each page is a plain <img> pointed at
// the /wm/pages/{n} endpoint — the browser ships the session cookie on
// the same-origin request, so no presigned URL is needed. This is the
// DEFAULT preview path for PDFs (see DocumentPreview); the react-pdf /
// annotation flow stays reachable via the "Original / Annotate" toggle
// so OCR overlay + markup workflows are unaffected.
//
// The watermark is burned into the raster server-side (dynamic tokens:
// viewer email / timestamp / ip / tenant / classification), so it can't
// be stripped by the client the way a CSS overlay could.
export function WatermarkedPreview({ documentId, versionId }: Props) {
  const [page, setPage] = useState(1)
  const [imgError, setImgError] = useState(false)

  const statusQ = useQuery({
    queryKey: ['wm-status', documentId, versionId],
    queryFn: () => getWatermarkStatus(documentId, versionId),
    // The render job may still be processing right after upload; poll
    // until it lands so the viewer flips to the pages without a manual
    // refresh.
    refetchInterval: (query) =>
      query.state.data?.status === 'processing' ? 4000 : false,
  })

  // Opens the watermarked PDF in a new tab for printing. window.open on
  // the same-origin /wm/download URL ships the session cookie; the tab's
  // built-in PDF viewer gives the user the Print dialog.
  const onPrint = () => {
    window.open(wmDownloadUrl(documentId, versionId), '_blank', 'noopener')
  }

  if (statusQ.isLoading) {
    return (
      <Card
        className="flex h-[60vh] flex-col items-center justify-center gap-3 bg-muted/20"
        data-testid="wm-preview-loading"
        aria-busy
        aria-label="Loading watermarked preview"
      >
        <Spinner className="h-6 w-6" />
        <p className="text-xs text-muted-foreground">Loading watermarked preview…</p>
      </Card>
    )
  }

  if (statusQ.isError) {
    return (
      <Card
        className="flex flex-col items-center justify-center gap-2 p-12 text-center text-sm"
        data-testid="wm-preview-error"
      >
        <AlertCircle className="h-10 w-10 text-destructive" />
        <p className="text-base font-medium text-foreground">Could not load preview</p>
        <p className="text-muted-foreground">
          {statusQ.error instanceof Error
            ? statusQ.error.message
            : 'The watermark status could not be fetched.'}
        </p>
      </Card>
    )
  }

  const status = statusQ.data

  if (status?.status === 'processing') {
    return (
      <Card
        className="flex h-[60vh] flex-col items-center justify-center gap-3 bg-muted/20"
        data-testid="wm-preview-processing"
        aria-busy
      >
        <Spinner className="h-6 w-6" />
        <p className="text-sm font-medium text-foreground">Applying watermark…</p>
        <p className="text-xs text-muted-foreground">
          The watermarked pages are still rendering. This view refreshes automatically.
        </p>
      </Card>
    )
  }

  if (status?.status === 'failed') {
    return (
      <Card
        className="flex flex-col items-center justify-center gap-2 p-12 text-center text-sm"
        data-testid="wm-preview-failed"
      >
        <AlertCircle className="h-10 w-10 text-destructive" />
        <p className="text-base font-medium text-foreground">Watermarking failed</p>
        <p className="text-muted-foreground">
          The watermarked render didn&rsquo;t complete. Switch to the original view, or retry later.
        </p>
      </Card>
    )
  }

  const pageCount = status?.page_count ?? 0

  if (status?.status === 'none' || pageCount <= 0) {
    return (
      <Card
        className="flex flex-col items-center justify-center gap-2 p-12 text-center text-sm"
        data-testid="wm-preview-empty"
      >
        <Stamp className="h-10 w-10 text-muted-foreground" />
        <p className="text-base font-medium text-foreground">No watermarked preview</p>
        <p className="text-muted-foreground">
          There are no watermarked pages to display for this version.
        </p>
      </Card>
    )
  }

  const current = Math.min(Math.max(page, 1), pageCount)

  return (
    <div className="flex flex-col items-center" data-testid="wm-preview">
      <div className="mb-2 flex w-full items-center justify-end">
        <Button
          variant="outline"
          size="sm"
          onClick={onPrint}
          data-testid="wm-print"
        >
          <Printer className="h-4 w-4" /> Print (watermarked)
        </Button>
      </div>

      <Card className="flex w-full items-center justify-center bg-muted/40 p-2">
        {imgError ? (
          <div className="flex flex-col items-center gap-2 p-12 text-center text-sm">
            <AlertCircle className="h-8 w-8 text-destructive" />
            <p className="text-muted-foreground">
              Page {current} could not be loaded.
            </p>
          </div>
        ) : (
          <img
            // key on the page so React remounts the <img> per page,
            // resetting the per-page error state via onError below.
            key={current}
            src={wmPageUrl(documentId, versionId, current)}
            alt={`Watermarked page ${current} of ${pageCount}`}
            onError={() => setImgError(true)}
            className="max-h-[80vh] w-auto rounded object-contain"
            data-testid="wm-page-image"
          />
        )}
      </Card>

      {pageCount > 1 && (
        <div className="mt-3 flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            disabled={current <= 1}
            onClick={() => {
              setImgError(false)
              setPage(current - 1)
            }}
            aria-label="Previous page"
          >
            <DirectionalIcon name="ChevronLeft" className="h-4 w-4" />
          </Button>
          <span className="text-sm" data-testid="wm-page-indicator">
            Page {current} of {pageCount}
          </span>
          <Button
            variant="ghost"
            size="sm"
            disabled={current >= pageCount}
            onClick={() => {
              setImgError(false)
              setPage(current + 1)
            }}
            aria-label="Next page"
          >
            <DirectionalIcon name="ChevronRight" className="h-4 w-4" />
          </Button>
        </div>
      )}
    </div>
  )
}
