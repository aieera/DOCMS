// Protected-container recipient viewer — IRM "Protect & share".
//
// Public-ish route: no SessionAuth gate on the route itself. The
// per-recipient license token is the credential and rides in the URL
// (?lt=…) — mirrors the zero-trust share viewer. Internal-bound
// licenses additionally require the recipient to be logged in as the
// bound user; the check returns 403 { error: 'session_required' } and
// we prompt for login.
//
// HONEST about what this buys: it is access-control + audit +
// revocation. Every open calls the license check (validates the
// recipient, logs the open, enforces expiry / revocation / max-opens).
// A revoked license blocks the next open. It does NOT prevent
// screenshots — this is not DRM / copy-prevention.
//
// SECURITY: the token is NEVER written to localStorage/sessionStorage
// (C2 arch-test forbids auth tokens in web storage). It lives in the
// URL search param and component state only.
import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import {
  AlertTriangle,
  Clock,
  Download as DownloadIcon,
  LogIn,
  Printer,
  ShieldAlert,
  ShieldCheck,
} from 'lucide-react'

import { Button } from '@/components/ui/shadcn/button'
import { readErrorMessage } from '@/api/client'
import {
  checkLicense,
  irmContentUrl,
  type CheckLicenseResponse,
} from '@/api/irm'

interface ProtectedSearch {
  lt: string
}

export const Route = createFileRoute('/protected/$containerId')({
  // Token lives in the URL only; validateSearch types it so a missing
  // ?lt is caught rather than silently becoming `undefined`.
  validateSearch: (raw: Record<string, unknown>): ProtectedSearch => ({
    lt: raw.lt == null ? '' : String(raw.lt),
  }),
  component: ProtectedViewerPage,
})

type ViewState =
  | { kind: 'loading' }
  | { kind: 'ok'; data: CheckLicenseResponse }
  | { kind: 'revoked' }
  | { kind: 'expired' }
  | { kind: 'max_opens' }
  | { kind: 'not_found' }
  | { kind: 'session_required' }
  | { kind: 'error'; message: string }

function classifyError(err: unknown): ViewState {
  const status = (err as { response?: { status?: number } }).response?.status
  const code = (err as { response?: { data?: { error?: string } } }).response?.data?.error
  if (status === 410) {
    if (code === 'expired') return { kind: 'expired' }
    if (code === 'max_opens') return { kind: 'max_opens' }
    return { kind: 'revoked' }
  }
  if (status === 404) return { kind: 'not_found' }
  if (status === 403) return { kind: 'session_required' }
  return { kind: 'error', message: readErrorMessage(err) ?? 'Could not open this document' }
}

function ProtectedViewerPage() {
  const { containerId } = Route.useParams()
  const { lt } = Route.useSearch()
  const [state, setState] = useState<ViewState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    if (!lt) {
      setState({ kind: 'error', message: 'This link is missing its license token.' })
      return
    }
    setState({ kind: 'loading' })
    checkLicense({ container_id: containerId, license_token: lt })
      .then((data) => {
        if (!cancelled) setState({ kind: 'ok', data })
      })
      .catch((err) => {
        if (!cancelled) setState(classifyError(err))
      })
    return () => {
      cancelled = true
    }
  }, [containerId, lt])

  if (state.kind === 'loading') {
    return <CenteredCard icon={Clock} title="Checking your license…" body="" />
  }
  if (state.kind === 'revoked') {
    return (
      <CenteredCard
        icon={ShieldAlert}
        title="This license has been revoked"
        body="The sender has revoked access to this document. Contact them if you still need it."
      />
    )
  }
  if (state.kind === 'expired') {
    return (
      <CenteredCard
        icon={Clock}
        title="This license has expired"
        body="Access to this protected document has expired. Ask the sender for a new link."
      />
    )
  }
  if (state.kind === 'max_opens') {
    return (
      <CenteredCard
        icon={ShieldAlert}
        title="Open limit reached"
        body="This license has been opened the maximum number of times allowed."
      />
    )
  }
  if (state.kind === 'not_found') {
    return (
      <CenteredCard
        icon={AlertTriangle}
        title="Not found"
        body="This protected container or license could not be found."
      />
    )
  }
  if (state.kind === 'session_required') {
    return (
      <CenteredCard
        icon={LogIn}
        title="Sign in to open this document"
        body="This license is bound to a specific account. Log in as that user, then reopen this link."
      >
        <Button
          className="mt-4"
          onClick={() => {
            // Preserve the full protected URL so login can bounce back.
            const back = encodeURIComponent(window.location.pathname + window.location.search)
            window.location.href = `/login?redirect=${back}`
          }}
        >
          <LogIn className="me-1 h-4 w-4" /> Go to sign in
        </Button>
      </CenteredCard>
    )
  }
  if (state.kind === 'error') {
    return <CenteredCard icon={AlertTriangle} title="Could not open this document" body={state.message} />
  }

  // state.kind === 'ok'
  const { data } = state
  const contentUrl = irmContentUrl(containerId, lt)
  const canPrint = data.allowed_actions.includes('print')
  const canDownload = data.allowed_actions.includes('download')

  const onPrint = () => {
    // Open the raw content in a new tab so the browser's print dialog
    // targets the document, not this shell page.
    const w = window.open(contentUrl, '_blank', 'noopener')
    w?.addEventListener('load', () => w.print())
  }

  return (
    <div className="flex min-h-screen flex-col bg-background">
      <header className="border-b border-border bg-muted/40 px-4 py-2 text-sm">
        <div className="flex flex-wrap items-center gap-3">
          <span className="inline-flex items-center gap-1.5 font-semibold">
            <ShieldCheck className="h-4 w-4 text-emerald-600" />
            {data.document_title}
          </span>
          <span className="text-muted-foreground">shared by {data.sender_email}</span>
          <span className="text-muted-foreground">
            expires {new Date(data.expires_at).toLocaleString()}
          </span>
          <div className="ms-auto flex items-center gap-2">
            {canPrint && (
              <Button variant="outline" size="sm" onClick={onPrint}>
                <Printer className="me-1 h-4 w-4" /> Print
              </Button>
            )}
            {canDownload && (
              <a
                href={contentUrl}
                download
                className="inline-flex h-8 items-center rounded-md border border-input bg-background px-3 text-xs hover:bg-accent hover:text-accent-foreground"
              >
                <DownloadIcon className="me-1 h-4 w-4" /> Download
              </a>
            )}
          </div>
        </div>
      </header>

      <div className="border-b border-border bg-blue-50/60 px-4 py-1.5 text-xs text-blue-900 dark:bg-blue-950/20 dark:text-blue-100">
        <span className="inline-flex items-center gap-1.5">
          <ShieldCheck className="h-3.5 w-3.5" />
          Access requires periodic online license checks and can be revoked by the sender at
          any time. This document's access is logged.
        </span>
      </div>

      <main className="flex-1">
        <iframe
          title={data.document_title}
          src={contentUrl}
          className="h-full min-h-[70vh] w-full border-0"
        />
      </main>
    </div>
  )
}

function CenteredCard({
  icon: Icon,
  title,
  body,
  children,
}: {
  icon: typeof AlertTriangle
  title: string
  body: string
  children?: React.ReactNode
}) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-6">
      <div className="max-w-md rounded-lg border border-border bg-card p-6 text-center">
        <Icon className="mx-auto mb-2 h-8 w-8 text-muted-foreground" />
        <h1 className="text-lg font-semibold">{title}</h1>
        {body && <p className="mt-2 text-sm text-muted-foreground">{body}</p>}
        {children}
      </div>
    </div>
  )
}
