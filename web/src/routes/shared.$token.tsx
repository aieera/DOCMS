// Public share-link viewer. No session cookie, no auth store — the
// token itself is the credential.
//
// Two-step UX (matches backend AccessShareLink semantics):
//   1. Peek: POST /api/v1/shared/{token} with no body. Server returns
//      either the document + download URL (open link), or
//      {password_required: true, document: null} (locked link).
//   2. Verify (locked only): POST same path with {password}. A wrong
//      password returns 403 Forbidden, which we surface as "Wrong
//      password" without leaking whether the link exists. A 404 means
//      the link is missing/revoked/the underlying doc was deleted; 409
//      means expired or view-count exhausted.
//
// Anything but a successful peek/verify leaves `status` in a
// non-`unlocked` state; the unlocked panel is the ONLY render path
// that surfaces document metadata or the download button, so an
// unverified caller never reaches content (C-1 invariant).
import { PasswordInput } from '@/components/ui/PasswordInput'
import { createFileRoute } from '@tanstack/react-router'
import { FileIcon } from '@/components/ui/FileIcon'
import { Button } from '@/components/ui/shadcn/button'
import { Download, Lock, Loader2, AlertTriangle, FileX } from 'lucide-react'
import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { accessShareLink, type ShareLinkAccessResult } from '@/api/shareLinks'
import { readErrorMessage } from '@/api/client'
import { formatFileSize, formatDateTime } from '@/lib/formatters'

type Status = 'loading' | 'needs_password' | 'unlocked' | 'expired' | 'not_found' | 'error'

function httpStatus(err: unknown): number | undefined {
  return (err as { response?: { status?: number } } | null)?.response?.status
}

function SharedViewerPage() {
  const { token } = Route.useParams()
  const [password, setPassword] = useState('')
  const [status, setStatus] = useState<Status>('loading')
  const [result, setResult] = useState<ShareLinkAccessResult | null>(null)
  const [errorMsg, setErrorMsg] = useState<string | null>(null)
  const [verifying, setVerifying] = useState(false)

  // Peek on mount. We deliberately do NOT auto-call with a stored
  // password from any prior session — the token is the only thing the
  // browser holds, and a fresh page load must re-prove possession of
  // the password.
  useEffect(() => {
    let cancelled = false
    accessShareLink(token)
      .then((res) => {
        if (cancelled) return
        if (res.password_required) {
          setStatus('needs_password')
          return
        }
        if (!res.document) {
          // Server returned 200 but no document and no password
          // requirement — shouldn't happen, but treat as not-found
          // rather than render an empty unlocked panel.
          setStatus('not_found')
          return
        }
        setResult(res)
        setStatus('unlocked')
      })
      .catch((err: unknown) => {
        if (cancelled) return
        const s = httpStatus(err)
        if (s === 404) { setStatus('not_found'); return }
        if (s === 409) { setStatus('expired'); return }
        // A public link never asks the recipient to sign in, so a 401/403
        // on the *peek* (no password was sent yet) is not an auth problem
        // the visitor can act on — it means the token is bad, revoked, or
        // no longer valid. Surfacing the server's raw "authentication
        // required" here told people to log in to a page that has no
        // login.
        if (s === 401 || s === 403) { setStatus('not_found'); return }
        setErrorMsg(readErrorMessage(err) ?? 'Failed to load share link')
        setStatus('error')
      })
    return () => { cancelled = true }
  }, [token])

  const handleUnlock = async () => {
    if (!password) {
      toast.error('Enter the password to continue')
      return
    }
    setVerifying(true)
    try {
      const res = await accessShareLink(token, password)
      // Backend returns 403 on wrong password (caught below), so a
      // 200 with password_required=true here would be unexpected —
      // be defensive and treat it as a failed verify.
      if (res.password_required || !res.document) {
        toast.error('Wrong password')
        return
      }
      setResult(res)
      setStatus('unlocked')
    } catch (err: unknown) {
      const s = httpStatus(err)
      if (s === 401 || s === 403) {
        toast.error('Wrong password')
      } else if (s === 404) {
        setStatus('not_found')
      } else if (s === 409) {
        setStatus('expired')
      } else {
        toast.error(readErrorMessage(err) ?? 'Could not verify password')
      }
    } finally {
      setVerifying(false)
    }
  }

  if (status === 'loading') {
    return (
      <CenteredPanel>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading shared document…
        </div>
      </CenteredPanel>
    )
  }

  if (status === 'not_found') {
    return (
      <CenteredPanel>
        <FileX className="mb-2 h-10 w-10 text-muted-foreground" aria-hidden />
        <h2 className="text-lg font-semibold">This link isn&apos;t valid</h2>
        <p className="text-sm text-muted-foreground">
          The link may have expired, been revoked, or been copied incorrectly. Ask the
          sender for a new one.
        </p>
      </CenteredPanel>
    )
  }

  if (status === 'expired') {
    return (
      <CenteredPanel>
        <AlertTriangle className="mb-2 h-10 w-10 text-warning-strong" aria-hidden />
        <h2 className="text-lg font-semibold">Link no longer accessible</h2>
        <p className="text-sm text-muted-foreground">
          This share link has expired or reached its view limit. Ask the
          sender for a new link.
        </p>
      </CenteredPanel>
    )
  }

  if (status === 'error') {
    return (
      <CenteredPanel>
        <AlertTriangle className="mb-2 h-10 w-10 text-destructive" aria-hidden />
        <h2 className="text-lg font-semibold">Couldn't load this link</h2>
        <p className="text-sm text-muted-foreground">{errorMsg ?? 'Unknown error'}</p>
      </CenteredPanel>
    )
  }

  if (status === 'needs_password') {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <form
          className="w-full max-w-sm space-y-4 rounded-xl bg-card p-8 shadow-neu"
          onSubmit={(e) => { e.preventDefault(); void handleUnlock() }}
        >
          <div className="flex flex-col items-center gap-2">
            <Lock className="h-8 w-8 text-primary" aria-hidden />
            <h2 className="text-lg font-semibold">Password required</h2>
            <p className="text-center text-sm text-muted-foreground">
              The sender protected this link with a password.
            </p>
          </div>
          <PasswordInput
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Enter password"
            autoFocus
            autoComplete="current-password"
            disabled={verifying}
          />
          <Button type="submit" className="w-full" loading={verifying} disabled={verifying || !password}>
            Unlock
          </Button>
        </form>
      </div>
    )
  }

  // status === 'unlocked' — we have a verified document.
  const doc = result!.document!
  // FIX-10 follow-up: the backend's anonymous /shared/{token}/download
  // endpoint re-validates the link and re-runs the password check on
  // every byte fetch (the link is the entitlement; there's no
  // session). For password-protected links we therefore need to
  // append the password the user already typed to unlock, otherwise
  // the download click 401s. The backend reads `?password=`
  // directly from the query string.
  let downloadHref = result!.download_url
  if (downloadHref && password) {
    const sep = downloadHref.includes('?') ? '&' : '?'
    downloadHref = downloadHref + sep + 'password=' + encodeURIComponent(password)
  }
  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-background p-8">
      <div className="w-full max-w-2xl rounded-xl bg-card p-8 shadow-neu">
        <div className="mb-6 flex items-start justify-between gap-4">
          <div className="flex items-center gap-3">
            <FileIcon mime={doc.mime_type} className="h-8 w-8" />
            <div className="min-w-0">
              <h1 className="truncate text-lg font-semibold" title={doc.title}>{doc.title}</h1>
              <p className="text-sm text-muted-foreground">
                {doc.mime_type || 'unknown'} · {formatFileSize(doc.total_size_bytes)}
                {doc.created_at ? ` · shared ${formatDateTime(doc.created_at)}` : ''}
              </p>
            </div>
          </div>
          {downloadHref ? (
            <Button asChild variant="outline">
              <a href={downloadHref} download>
                <Download className="me-2 h-4 w-4" /> Download
              </a>
            </Button>
          ) : (
            <span className="text-xs text-muted-foreground">Download not permitted</span>
          )}
        </div>
        {doc.description ? (
          <div className="mb-4 rounded-md bg-muted p-3 text-sm shadow-neu-inset">
            {doc.description}
          </div>
        ) : null}
        <div className="rounded-lg bg-muted px-4 py-12 text-center shadow-neu-inset">
          <FileIcon mime={doc.mime_type} className="mx-auto mb-3 h-12 w-12" />
          <p className="text-sm text-muted-foreground">
            Inline preview isn't available on public share links.
            {downloadHref ? ' Download the file to view it locally.' : ''}
          </p>
        </div>
        <p className="mt-4 text-center text-xs text-muted-foreground">Shared via SeDoc</p>
      </div>
    </div>
  )
}

function CenteredPanel({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background">
      <div className="flex w-full max-w-sm flex-col items-center gap-2 rounded-xl bg-card p-8 text-center shadow-neu">
        {children}
      </div>
    </div>
  )
}

export const Route = createFileRoute('/shared/$token')({ component: SharedViewerPage })
