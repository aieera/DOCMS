import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Shield, Download } from 'lucide-react'

import { listDSR, requestDSRToken, submitDSR, type DSRRequest } from '@/api/privacy'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatDate, formatRelativeTime } from '@/lib/formatters'

function PrivacyPage() {
  const qc = useQueryClient()
  const [email, setEmail] = useState('')
  const [type, setType] = useState<'export' | 'erase' | 'anonymize'>('export')
  const [token, setToken] = useState('')

  const { data, isLoading } = useQuery({
    queryKey: ['dsr-requests'],
    queryFn: () => listDSR(),
  })

  const requestToken = useMutation({
    mutationFn: () => requestDSRToken(email),
    onSuccess: () =>
      toast.success(
        'Verification token sent to subject (check their notifications; SMTP wires in Wave 12)',
      ),
    onError: () => toast.error('Token request failed'),
  })

  const submit = useMutation({
    mutationFn: () => submitDSR(type, { subject_email: email, verification_token: token || undefined }),
    onSuccess: () => {
      toast.success('Request queued')
      setEmail('')
      setToken('')
      qc.invalidateQueries({ queryKey: ['dsr-requests'] })
    },
    onError: (err: unknown) => {
      const m =
        typeof err === 'object' && err && 'message' in err
          ? String((err as { message?: string }).message)
          : 'Submit failed'
      toast.error(m)
    },
  })

  return (
    <div>
      <PageHeader
        title="Privacy Requests"
        description="GDPR export / erase / anonymize — tracked per subject"
      />

      <div className="mb-6 rounded-lg border border-border bg-card p-4">
        <div className="mb-3 flex items-center gap-2">
          <Shield className="h-4 w-4" />
          <h3 className="font-medium">New request</h3>
        </div>
        <div className="grid grid-cols-[auto_1fr_1fr_auto] items-center gap-3">
          <select
            className="rounded-md border border-border bg-background px-2 py-1 text-sm"
            value={type}
            onChange={(e) => setType(e.target.value as 'export' | 'erase' | 'anonymize')}
          >
            <option value="export">Export</option>
            <option value="erase">Erase</option>
            <option value="anonymize">Anonymize</option>
          </select>
          <input
            className="rounded-md border border-border bg-background px-2 py-1 text-sm"
            placeholder="subject@example.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <input
            className="rounded-md border border-border bg-background px-2 py-1 text-sm"
            placeholder={type === 'erase' ? 'verification token (required)' : 'verification token (optional)'}
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
          <Button
            onClick={() => submit.mutate()}
            disabled={!email || submit.isPending || (type === 'erase' && !token)}
          >
            Submit
          </Button>
        </div>
        {type === 'erase' && (
          <div className="mt-2 flex items-center gap-2">
            <Button
              onClick={() => requestToken.mutate()}
              disabled={!email || requestToken.isPending}
            >
              Request verification token
            </Button>
            <span className="text-xs text-muted-foreground">
              Subject receives a 24h token via notifications (SMTP in Wave 12). Paste the received
              token into the field above.
            </span>
          </div>
        )}
        <p className="mt-2 text-xs text-muted-foreground">
          Erase and anonymize short-circuit on any document under an active legal hold. 30-day SLA.
        </p>
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-16" />)}
        </div>
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Shield className="h-12 w-12" />}
          title="No privacy requests"
          description="Subject-access requests submitted via API or this page will appear here."
        />
      ) : (
        <ul className="space-y-2">
          {data.map((r) => (
            <li
              key={r.id}
              className="flex items-start justify-between rounded-lg border border-border bg-card p-3"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{r.subject_email}</span>
                  <Badge variant={statusVariant(r.status)}>{r.status}</Badge>
                  <span className="text-xs text-muted-foreground">
                    {r.request_type}
                  </span>
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  Submitted {formatRelativeTime(r.created_at)} on {formatDate(r.created_at)}
                  {r.completed_at && <> · finished {formatRelativeTime(r.completed_at)}</>}
                </div>
                {r.blocked_reason && (
                  <p className="mt-1 text-xs text-destructive">{r.blocked_reason}</p>
                )}
              </div>
              {r.export_url && (
                <a
                  href={r.export_url}
                  target="_blank"
                  rel="noreferrer"
                  className="shrink-0 text-sm text-primary"
                >
                  <Download className="mr-1 inline h-4 w-4" />
                  Download
                </a>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function statusVariant(s: DSRRequest['status']): string {
  switch (s) {
    case 'completed':
      return 'active'
    case 'blocked':
    case 'failed':
      return 'disposed'
    case 'running':
    case 'pending':
      return 'in_review'
    default:
      return 'default'
  }
}

export const Route = createFileRoute('/_authenticated/admin/privacy')({ component: PrivacyPage })
