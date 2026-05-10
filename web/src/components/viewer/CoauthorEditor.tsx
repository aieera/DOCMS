// ADR 0065 — Edit-in-browser button + iframe modal.
//
// Behavior:
//   1. On click we POST /coauth/start to mint an access_token.
//   2. We probe the editor's health URL (no-cors fetch). If
//      unreachable in 2s we fall back to "Open in desktop app"
//      (a download link with the document's native mime type).
//   3. Otherwise we render a fullscreen iframe with the URL the
//      backend returned. The iframe's `sandbox` attr is intentionally
//      permissive — OnlyOffice + Collabora need same-origin scripts
//      and downloads.
//   4. Closing the modal is a hint, not a save. The editor's own
//      autosave + the backend's PutFile lock handshake decide when
//      changes commit.
//
// "Other editors" presence is rendered by the editor itself (both
// OnlyOffice and Collabora draw their own avatars in the header),
// so the host UI doesn't need a separate presence indicator. We
// surface a count badge derived from the WOPI lock state for the
// tasks-list "N people editing" hint that lives outside the modal.

import { useEffect, useRef, useState } from 'react'
import { Pencil, ExternalLink, X, AlertTriangle } from 'lucide-react'
import toast from 'react-hot-toast'

import { startCoauthSession, probeEditorReachable, type CoauthSession } from '@/api/coauth'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'

interface CoauthorEditorProps {
  documentId: string
  versionId: string
  mimeType: string
  fileName: string
  // Caller passes a download URL we can offer as the desktop-app
  // fallback. The backend exposes one at
  // /documents/{id}/versions/{vid}/download already.
  downloadUrl: string
  // Whether the user has edit permission. The button still shows in
  // view-only mode (renders "Open in editor (read-only)").
  canEdit: boolean
}

const SUPPORTED = new Set([
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document',  // docx
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',         // xlsx
  'application/vnd.openxmlformats-officedocument.presentationml.presentation', // pptx
  'application/msword',
  'application/vnd.ms-excel',
  'application/vnd.ms-powerpoint',
])

export function CoauthorEditor({
  documentId, versionId, mimeType, fileName, downloadUrl, canEdit,
}: CoauthorEditorProps) {
  const [open, setOpen] = useState(false)
  const [session, setSession] = useState<CoauthSession | null>(null)
  const [loading, setLoading] = useState(false)
  const [unreachable, setUnreachable] = useState(false)

  if (!SUPPORTED.has(mimeType)) {
    return null // not an editable Office format; don't show the button
  }

  const start = async () => {
    setLoading(true)
    setUnreachable(false)
    try {
      const mode = canEdit ? 'edit' : 'view'
      const s = await startCoauthSession(documentId, versionId, mode)
      if (s.provider === 'disabled') {
        toast('Co-authoring is disabled by your administrator.', { icon: 'ℹ️' })
        return
      }
      // Probe before opening so we can fall back cleanly.
      const ok = await probeEditorReachable(s.editor_health_url)
      if (!ok) {
        setUnreachable(true)
        return
      }
      setSession(s)
      setOpen(true)
    } catch (e: any) {
      toast.error(e?.response?.data?.error ?? 'Failed to start editor session')
    } finally {
      setLoading(false)
    }
  }

  return (
    <>
      <Button onClick={start} disabled={loading} data-testid="edit-in-browser">
        {loading ? <Spinner /> : <Pencil className="h-4 w-4" />}
        {canEdit ? 'Edit in browser' : 'Open in editor'}
      </Button>

      {/* Fallback: editor unreachable. Show desktop-app option. */}
      {unreachable && (
        <div className="mt-2 flex items-start gap-2 rounded border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100" data-testid="coauth-fallback">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <div className="flex-1">
            <p>The browser editor isn't reachable from your network.</p>
            <a
              href={downloadUrl}
              download={fileName}
              className="mt-2 inline-flex items-center gap-1 text-xs underline"
            >
              <ExternalLink className="h-3 w-3" /> Open in desktop app
            </a>
          </div>
          <button onClick={() => setUnreachable(false)} aria-label="dismiss" className="text-xs">
            <X className="h-4 w-4" />
          </button>
        </div>
      )}

      {/* Iframe modal */}
      {open && session && (
        <CoauthorIframeModal session={session} fileName={fileName} onClose={() => setOpen(false)} />
      )}
    </>
  )
}

function CoauthorIframeModal({ session, fileName, onClose }: { session: CoauthSession; fileName: string; onClose: () => void }) {
  const ref = useRef<HTMLIFrameElement>(null)
  // Some editors (Collabora) post a window.message when the user
  // hits Close; bridge that into our onClose so the X works the
  // same regardless of editor.
  useEffect(() => {
    const handler = (e: MessageEvent) => {
      if (typeof e.data === 'string' && e.data.startsWith('{')) {
        try {
          const m = JSON.parse(e.data)
          if (m?.MessageId === 'Close_Session' || m?.MessageId === 'CLOSE') {
            onClose()
          }
        } catch { /* not JSON */ }
      }
    }
    window.addEventListener('message', handler)
    return () => window.removeEventListener('message', handler)
  }, [onClose])

  return (
    <div className="fixed inset-0 z-[60] flex flex-col bg-black/80" data-testid="coauth-modal">
      <header className="flex items-center justify-between gap-3 bg-[var(--color-bg-secondary)] px-4 py-2 text-sm">
        <div className="truncate">
          <strong>{fileName}</strong>
          <span className="ml-2 text-xs text-[var(--color-text-secondary)]">
            via {session.provider} {session.mode === 'view' ? '(read-only)' : ''}
          </span>
        </div>
        <button onClick={onClose} aria-label="Close editor" className="rounded p-1 hover:bg-slate-100 dark:hover:bg-slate-800">
          <X className="h-5 w-5" />
        </button>
      </header>
      <iframe
        ref={ref}
        title={`Editing ${fileName}`}
        src={session.iframe_url}
        sandbox="allow-scripts allow-same-origin allow-forms allow-downloads allow-popups allow-popups-to-escape-sandbox"
        className="h-full w-full flex-1 border-0 bg-white"
        data-testid="coauth-iframe"
      />
    </div>
  )
}
