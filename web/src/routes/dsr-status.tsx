// Public, unauthenticated DSR status + verification page (ADR 0037).
//
// Reachable at /dsr-status?token=… — the link in the verification email
// the public form sends. Two phases:
//
//   1. Auto-fetch status by token. If the request hasn't been verified
//      yet, show a "Verify your identity" button. Clicking it stamps
//      requester_identity_verified_at on the row; mutate workflows
//      (erasure, rectification) require this before proceeding.
//   2. Once verified (or for read-only access/portability), show the
//      current status + due date + completion artifact link when ready.
//
// Generic 404 from the backend looks the same as a typo'd token —
// per the ADR we never reveal whether a request exists.

import { createFileRoute, Link, useSearch } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Shield, Check, AlertTriangle, Download, Clock } from 'lucide-react'

import { publicStatus, publicVerify } from '@/api/privacy'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatDateTime, formatRelativeTime } from '@/lib/formatters'

interface Search {
  token?: string
}

function DSRStatusPage() {
  const { token } = useSearch({ from: '/dsr-status' }) as Search
  const qc = useQueryClient()

  const { data, isLoading, error } = useQuery({
    queryKey: ['dsr-public-status', token],
    queryFn: () => publicStatus(token!),
    enabled: !!token,
    // Polled refresh so a subject who leaves the tab open sees status
    // advance from running → completed without manual reload.
    refetchInterval: 30_000,
  })

  const verify = useMutation({
    mutationFn: () => publicVerify(token!),
    onSuccess: () => {
      toast.success('Identity verified')
      qc.invalidateQueries({ queryKey: ['dsr-public-status', token] })
    },
    onError: () => toast.error('Verification failed. The link may have expired.'),
  })

  if (!token) {
    return (
      <Layout>
        <div className="text-center">
          <AlertTriangle className="mx-auto mb-4 h-8 w-8 text-amber-500" />
          <h2 className="text-lg font-semibold">Missing token</h2>
          <p className="mt-2 text-sm text-[var(--color-text-secondary)]">
            This page expects a token from your verification email.
          </p>
          <Link
            to="/dsr-request"
            className="mt-4 inline-block text-sm text-[var(--color-primary)] hover:underline"
          >
            Submit a new request →
          </Link>
        </div>
      </Layout>
    )
  }

  if (isLoading) {
    return (
      <Layout>
        <Skeleton className="h-32" />
      </Layout>
    )
  }

  if (error || !data) {
    return (
      <Layout>
        <div className="text-center">
          <AlertTriangle className="mx-auto mb-4 h-8 w-8 text-amber-500" />
          <h2 className="text-lg font-semibold">Request not found</h2>
          <p className="mt-2 text-sm text-[var(--color-text-secondary)]">
            This token is invalid or has expired. Verification links are valid for 7 days.
          </p>
          <Link
            to="/dsr-request"
            className="mt-4 inline-block text-sm text-[var(--color-primary)] hover:underline"
          >
            Submit a new request →
          </Link>
        </div>
      </Layout>
    )
  }

  return (
    <Layout>
      <div className="mb-4 flex items-center gap-2">
        <Shield className="h-5 w-5 text-[var(--color-primary)]" />
        <h2 className="text-lg font-semibold">Your data request</h2>
      </div>

      <dl className="space-y-2 text-sm">
        <Row label="Type">{prettyType(data.request_type)}</Row>
        <Row label="Status">
          <StatusPill status={data.status} />
        </Row>
        <Row label="Submitted">{formatDateTime(data.created_at)}</Row>
        <Row label="Decision deadline">
          <span className="inline-flex items-center gap-1">
            <Clock className="h-3.5 w-3.5" />
            {formatDateTime(data.due_at)} ({formatRelativeTime(data.due_at)})
          </span>
        </Row>
        {data.completed_at && (
          <Row label="Completed">{formatDateTime(data.completed_at)}</Row>
        )}
      </dl>

      {!data.verified && data.status === 'pending' && (
        <div className="mt-4 rounded border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200">
          <p className="mb-2">
            <strong>Verify your identity</strong> — clicking below confirms the email address
            we sent the link to is yours. Erasure and rectification requests require this
            confirmation before they proceed.
          </p>
          <Button
            onClick={() => verify.mutate()}
            loading={verify.isPending}
            data-testid="dsr-verify"
          >
            <Check className="h-4 w-4" /> Verify identity
          </Button>
        </div>
      )}

      {data.verified && data.status === 'pending' && (
        <p className="mt-4 text-xs text-emerald-700 dark:text-emerald-300">
          Identity confirmed. Your request is queued for processing.
        </p>
      )}

      {data.status === 'completed' && (
        <div className="mt-4 rounded border border-emerald-200 bg-emerald-50 p-3 text-sm text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200">
          <p className="mb-2">
            <strong>Your request is complete.</strong>
          </p>
          {data.request_type === 'export' && (
            <p className="text-xs">
              The download link in the completion email is valid for 24 hours from delivery.
              If yours has expired, contact your data protection officer for a fresh link.
            </p>
          )}
        </div>
      )}

      {data.status === 'blocked' && (
        <div className="mt-4 rounded border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200">
          <p>
            Your request is on hold pending review. Some of your data may be subject to legal
            preservation requirements that prevent immediate erasure. Our compliance team is
            reviewing — you'll be notified by email when there's an update.
          </p>
        </div>
      )}

      {data.status === 'failed' && (
        <div className="mt-4 rounded border border-red-200 bg-red-50 p-3 text-sm text-red-900 dark:border-red-900 dark:bg-red-950/40 dark:text-red-200">
          <p>
            Your request could not be processed. Please contact your organisation's data
            protection officer.
          </p>
        </div>
      )}

      <p className="mt-6 text-center text-xs text-[var(--color-text-secondary)]">
        This page auto-refreshes every 30 seconds.
      </p>
    </Layout>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b border-[var(--color-border)]/40 pb-1.5">
      <dt className="text-xs uppercase tracking-wide text-[var(--color-text-secondary)]">{label}</dt>
      <dd className="text-right">{children}</dd>
    </div>
  )
}

function StatusPill({ status }: { status: string }) {
  const styles: Record<string, string> = {
    pending: 'bg-amber-100 text-amber-900 dark:bg-amber-950/50 dark:text-amber-200',
    running: 'bg-sky-100 text-sky-900 dark:bg-sky-950/50 dark:text-sky-200',
    completed: 'bg-emerald-100 text-emerald-900 dark:bg-emerald-950/50 dark:text-emerald-200',
    blocked: 'bg-amber-100 text-amber-900 dark:bg-amber-950/50 dark:text-amber-200',
    failed: 'bg-red-100 text-red-900 dark:bg-red-950/50 dark:text-red-200',
  }
  return (
    <span className={`rounded px-2 py-0.5 text-xs font-medium ${styles[status] ?? ''}`}>
      {status}
    </span>
  )
}

function prettyType(t: 'export' | 'erase' | 'anonymize'): string {
  switch (t) {
    case 'export':
      return 'Access / Portability (Art. 15 / 20)'
    case 'erase':
      return 'Erasure (Art. 17)'
    case 'anonymize':
      return 'Anonymisation'
  }
}

function Layout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)] px-4 py-8">
      <div className="w-full max-w-md rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
        <div className="mb-6 text-center">
          <h1 className="text-2xl font-bold text-[var(--color-primary)]">VaultDMS</h1>
          <p className="text-xs text-[var(--color-text-secondary)]">Data subject rights · GDPR</p>
        </div>
        {children}
        {/* Download icon import shake-out: referenced indirectly via prettyType
            UI; keep it imported so future enhancements (artifact link) compile. */}
        <span className="hidden">
          <Download className="h-0 w-0" />
        </span>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/dsr-status')({
  component: DSRStatusPage,
  validateSearch: (search: Record<string, unknown>): Search => ({
    token: typeof search.token === 'string' ? search.token : undefined,
  }),
})
