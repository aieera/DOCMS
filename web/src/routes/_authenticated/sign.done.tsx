import { createFileRoute, useSearch } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { CheckCircle2, XCircle, Loader2 } from 'lucide-react'

import { getQESSession, getQESCertificates } from '@/api/signatures'
import { PageHeader } from '@/components/shared/PageHeader'

// /sign/done?session=…&status=…[&reason=…]
//
// The QTSP redirects to /api/v1/signatures/qes/return; that handler
// 302s here. We display the outcome and (on success) the qualified
// certificate that was issued. Polls the session row for ~10s in
// case the embedder is still finalizing — most of the time the
// status is already 'completed' on first read.

interface DoneSearch {
  session?: string
  status?: 'completed' | 'failed' | 'expired'
  reason?: string
}

export const Route = createFileRoute('/_authenticated/sign/done')({
  validateSearch: (s: Record<string, unknown>): DoneSearch => ({
    session: typeof s.session === 'string' ? s.session : undefined,
    status: typeof s.status === 'string' ? (s.status as DoneSearch['status']) : undefined,
    reason: typeof s.reason === 'string' ? s.reason : undefined,
  }),
  component: SignDonePage,
})

function SignDonePage() {
  const { session: sessionID, status: redirectStatus, reason } = useSearch({ from: '/_authenticated/sign/done' })

  const sessionQ = useQuery({
    queryKey: ['qes-session', sessionID],
    queryFn: () => getQESSession(sessionID!),
    enabled: !!sessionID,
    // Poll up to 5 times if the row hasn't flipped to completed yet
    // (the embedder runs after handler return, so there's a small
    // window where status is 'authorized').
    refetchInterval: (q) => (q.state.data?.status === 'completed' || q.state.data?.status === 'failed' ? false : 2000),
  })

  const status = sessionQ.data?.status ?? redirectStatus ?? 'pending'
  const failureReason = sessionQ.data?.failure_reason ?? reason

  const certsQ = useQuery({
    queryKey: ['qes-certs', sessionQ.data?.request_id],
    queryFn: () => getQESCertificates(sessionQ.data!.request_id),
    enabled: status === 'completed' && !!sessionQ.data?.request_id,
  })

  return (
    <div className="mx-auto max-w-2xl p-6">
      <PageHeader title="Signing result" />

      {!sessionID && <p className="text-sm text-destructive">Missing session id in URL.</p>}

      {status === 'pending' || status === 'authorized' ? (
        <div className="flex items-center gap-2 rounded-md border border-border bg-card p-4" data-testid="qes-status-pending">
          <Loader2 className="h-5 w-5 animate-spin" /> Finalizing your signature…
        </div>
      ) : status === 'completed' ? (
        <div className="space-y-4" data-testid="qes-status-completed">
          <div className="flex items-center gap-2 rounded-md border border-success/40 bg-success/10 p-4 text-success">
            <CheckCircle2 className="h-5 w-5" /> Document signed via {sessionQ.data?.provider}.
          </div>
          {certsQ.data && certsQ.data.length > 0 && (
            <section className="rounded-lg border border-border bg-card p-4" data-testid="qes-cert-block">
              <h2 className="mb-2 text-lg font-semibold">Qualified certificate</h2>
              {certsQ.data.map((c) => (
                <dl key={c.id} className="grid grid-cols-[140px_1fr] gap-y-1 text-sm" data-testid={`qes-cert-${c.id}`}>
                  <dt className="text-muted-foreground">Subject</dt><dd className="font-mono">{c.subject_dn}</dd>
                  <dt className="text-muted-foreground">Issuer</dt><dd className="font-mono">{c.issuer_dn}</dd>
                  <dt className="text-muted-foreground">Serial</dt><dd className="font-mono">{c.serial_hex}</dd>
                  <dt className="text-muted-foreground">Valid</dt>
                  <dd>{new Date(c.not_before).toLocaleDateString()} — {new Date(c.not_after).toLocaleDateString()}</dd>
                  <dt className="text-muted-foreground">Provider</dt><dd>{c.provider}</dd>
                </dl>
              ))}
            </section>
          )}
        </div>
      ) : (
        <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-4 text-destructive" data-testid="qes-status-failed">
          <XCircle className="mt-0.5 h-5 w-5" />
          <div>
            <div className="font-medium">Signing did not complete.</div>
            <div className="text-sm">
              {failureReason === 'expired'
                ? 'The signing session expired. Restart the signing flow to try again.'
                : failureReason === 'consumed'
                  ? 'This auth code has already been used. Restart the signing flow.'
                  : `Reason: ${failureReason || 'unknown'}`}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
