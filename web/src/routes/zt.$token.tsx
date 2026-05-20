// Zero-trust view-only recipient page (ADR 0098 §18 F2).
//
// Public route — no SessionAuth required; the token IS the auth.
// Renders the document via pdf.js with:
//   - watermark overlay (CSS, repeating, low opacity)
//   - copy / print / contextmenu disabled
//   - page-view + dwell-time + focus telemetry POSTed back to the
//     server so the sender can see the activity timeline
//
// HONEST about what this buys (per ADR 0098 § threat model):
//   ✅ blocks: end-click save, Ctrl+P, drag-and-drop, raw PDF download
//   ❌ does NOT block: screenshots, screen recording, phone-on-monitor.
//      The watermark exists to TRACE leaks, not prevent them. Anything
//      that pretends to "prevent screenshots in a browser" without DRM
//      is security theater.
import { useEffect, useRef, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { Document, Page, pdfjs } from 'react-pdf'
import { ShieldAlert, AlertTriangle, Clock } from 'lucide-react'

import {
  fetchZTManifest,
  postZTTelemetry,
  type ZTManifest,
} from '@/api/ztShare'

// react-pdf needs the worker URL; bundled via the existing pdfjs-dist dep.
pdfjs.GlobalWorkerOptions.workerSrc = new URL(
  'pdfjs-dist/build/pdf.worker.min.mjs',
  import.meta.url,
).toString()

export const Route = createFileRoute('/zt/$token')({
  component: ZTViewerPage,
})

function ZTViewerPage() {
  const { token } = Route.useParams()
  const [manifest, setManifest] = useState<ZTManifest | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [pageNumber, setPageNumber] = useState(1)
  const [numPages, setNumPages] = useState(0)
  const dwellStart = useRef<number>(Date.now())
  const lastPage = useRef<number>(1)

  // Manifest fetch.
  useEffect(() => {
    let cancelled = false
    fetchZTManifest(token)
      .then((m) => { if (!cancelled) setManifest(m) })
      .catch((e) => { if (!cancelled) setError(e.message) })
    return () => { cancelled = true }
  }, [token])

  // Telemetry: page-view on each page change with dwell of previous.
  useEffect(() => {
    if (!manifest) return
    const dwellMs = Date.now() - dwellStart.current
    if (lastPage.current !== pageNumber) {
      postZTTelemetry(token, {
        event_type: 'page_view',
        page_number: lastPage.current,
        dwell_ms: dwellMs,
      })
      lastPage.current = pageNumber
      dwellStart.current = Date.now()
    }
    // Initial page-view ping
    postZTTelemetry(token, { event_type: 'page_view', page_number: pageNumber })
  }, [pageNumber, manifest, token])

  // Focus / blur telemetry
  useEffect(() => {
    const onBlur = () => postZTTelemetry(token, { event_type: 'focus_blur' })
    window.addEventListener('blur', onBlur)
    return () => window.removeEventListener('blur', onBlur)
  }, [token])

  // DevTools-open heuristic (easy to defeat; included per ADR for
  // tenants that want it).
  useEffect(() => {
    const check = () => {
      const heightDelta = window.outerHeight - window.innerHeight
      const widthDelta = window.outerWidth - window.innerWidth
      if (heightDelta > 200 || widthDelta > 200) {
        postZTTelemetry(token, { event_type: 'devtools_open' })
      }
    }
    const t = setInterval(check, 5000)
    return () => clearInterval(t)
  }, [token])

  // Disable copy / contextmenu / printing.
  useEffect(() => {
    const stop = (e: Event) => e.preventDefault()
    document.addEventListener('contextmenu', stop)
    document.addEventListener('copy', stop)
    document.addEventListener('cut', stop)
    document.addEventListener('dragstart', stop)
    return () => {
      document.removeEventListener('contextmenu', stop)
      document.removeEventListener('copy', stop)
      document.removeEventListener('cut', stop)
      document.removeEventListener('dragstart', stop)
    }
  }, [])

  if (error) return <CenteredCard icon={AlertTriangle} title="Could not load this share" body={error} />
  if (!manifest) return <CenteredCard icon={Clock} title="Loading…" body="" />
  if (manifest.revoked) return <CenteredCard icon={ShieldAlert} title="This share has been revoked" body="" />
  if (manifest.max_views > 0 && manifest.view_count >= manifest.max_views) {
    return <CenteredCard icon={ShieldAlert} title="View limit reached" body="" />
  }

  const isPDF = manifest.mime_type === 'application/pdf'
  const streamUrl = `/api/v1/zt/${token}/stream`

  return (
    <div
      className="zt-viewer"
      style={{ userSelect: 'none', WebkitUserSelect: 'none' }}
      onDragStart={(e) => e.preventDefault()}
    >
      {/* Suppress print entirely. */}
      <style>{`
        @media print { body * { display: none !important; } }
        .zt-viewer img, .zt-viewer canvas { -webkit-user-drag: none; }
      `}</style>

      <header className="border-b border-border bg-muted/40 px-4 py-2 text-sm">
        <div className="flex flex-wrap items-center gap-3">
          <span className="font-semibold">{manifest.document_title}</span>
          <span className="text-muted-foreground">shared by {manifest.sender_name}</span>
          <span className="ms-auto inline-flex items-center gap-1 text-xs text-amber-700 dark:text-amber-300">
            <ShieldAlert className="h-3.5 w-3.5" />
            View-only · screenshots are not blocked
          </span>
        </div>
      </header>

      <main className="relative mx-auto max-w-4xl px-4 py-6">
        {isPDF ? (
          <Document
            file={streamUrl}
            onLoadSuccess={({ numPages }) => setNumPages(numPages)}
            onLoadError={(e) => setError(e.message)}
          >
            <Page pageNumber={pageNumber} width={780} renderTextLayer={false} renderAnnotationLayer={false} />
          </Document>
        ) : (
          <img src={streamUrl} alt="Shared content" className="max-w-full" />
        )}

        {/* Watermark overlay — CSS only, repeating, pointer-events:none
            so the user can still scroll the document. Pixel-burn isn't
            necessary because the watermark only needs to be in any
            screenshot the recipient takes, not in the rendered bytes
            on the server (we don't ship server-side burn-in in Phase 1
            because pdf.js renders client-side; Phase 1.5 wires a
            server-side rendered tile stream behind a feature flag). */}
        <Watermark text={manifest.watermark_text} />
      </main>

      {isPDF && numPages > 0 && (
        <footer className="sticky bottom-0 border-t border-border bg-background px-4 py-2 text-sm">
          <div className="flex items-center justify-center gap-3">
            <button
              onClick={() => setPageNumber((n) => Math.max(1, n - 1))}
              disabled={pageNumber <= 1}
              className="rounded border px-2 py-1 disabled:opacity-50"
            >
              ←
            </button>
            <span>
              Page {pageNumber} / {numPages}
            </span>
            <button
              onClick={() => setPageNumber((n) => Math.min(numPages, n + 1))}
              disabled={pageNumber >= numPages}
              className="rounded border px-2 py-1 disabled:opacity-50"
            >
              →
            </button>
          </div>
        </footer>
      )}
    </div>
  )
}

function Watermark({ text }: { text: string }) {
  return (
    <div
      aria-hidden
      style={{
        position: 'absolute',
        inset: 0,
        pointerEvents: 'none',
        backgroundImage: `url("data:image/svg+xml;utf8,${encodeURIComponent(
          `<svg xmlns='http://www.w3.org/2000/svg' width='600' height='200'>
            <text x='0' y='100' fill='rgba(120,120,120,0.18)' font-size='18'
                  font-family='system-ui' transform='rotate(-25 0 100)'>${escapeSvg(text)}</text>
          </svg>`,
        )}")`,
        backgroundRepeat: 'repeat',
      }}
    />
  )
}

function escapeSvg(s: string) {
  return s.replace(/[<>&'"]/g, (c) =>
    ({ '<': '&lt;', '>': '&gt;', '&': '&amp;', '\'': '&apos;', '"': '&quot;' }[c] as string),
  )
}

function CenteredCard({ icon: Icon, title, body }: { icon: any; title: string; body: string }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-6">
      <div className="max-w-md rounded-lg border border-border bg-card p-6 text-center">
        <Icon className="mx-auto mb-2 h-8 w-8 text-muted-foreground" />
        <h1 className="text-lg font-semibold">{title}</h1>
        {body && <p className="mt-2 text-sm text-muted-foreground">{body}</p>}
      </div>
    </div>
  )
}
