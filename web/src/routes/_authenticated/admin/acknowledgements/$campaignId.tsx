// Per-campaign acknowledgement detail page.
//
// Backend exposes GET /acknowledgement/campaigns/{id}, /report, and the
// Close action; ListAssignments is explicitly deferred to avoid leaking
// PII (ip_address, user_agent) through the public surface — see the
// "list-assignments deferred" return in handler.go. So this page
// renders the campaign metadata, the rolled-up report (live polled),
// the policy body, and the Close action. Per-assignee status sits in
// the audit export until that gate is opened.

import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { useState } from 'react'
import { ArrowLeft, FileText, ClipboardCheck, AlertTriangle } from 'lucide-react'

import {
  closeCampaign,
  getCampaign,
  getReport,
} from '@/api/acknowledgements'
import { useDocument } from '@/hooks/useDocuments'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { Skeleton } from '@/components/ui/Skeleton'
import { PageHeader } from '@/components/shared/PageHeader'
import { formatDateTime } from '@/lib/formatters'

function CampaignDetailPage() {
  const { campaignId } = Route.useParams()
  const qc = useQueryClient()
  const [confirmClose, setConfirmClose] = useState(false)

  const { data: campaign, isLoading, error } = useQuery({
    queryKey: ['ack-campaign', campaignId],
    queryFn: () => getCampaign(campaignId),
  })
  const { data: report } = useQuery({
    queryKey: ['ack-report', campaignId],
    queryFn: () => getReport(campaignId),
    refetchInterval: 10_000,
    enabled: !!campaign,
  })
  const { data: doc } = useDocument(campaign?.document_id ?? '')

  const close = useMutation({
    mutationFn: () => closeCampaign(campaignId),
    onSuccess: () => {
      toast.success('Campaign closed')
      qc.invalidateQueries({ queryKey: ['ack-campaign', campaignId] })
      qc.invalidateQueries({ queryKey: ['ack-campaigns'] })
      setConfirmClose(false)
    },
    onError: (e: unknown) => {
      const anyErr = e as { response?: { data?: { error?: { message?: string } } } }
      toast.error(anyErr?.response?.data?.error?.message ?? 'Could not close campaign')
    },
  })

  if (isLoading) {
    return (
      <div>
        <PageHeader title="Campaign" description="" />
        <Skeleton className="h-64" />
      </div>
    )
  }

  if (error || !campaign) {
    return (
      <div>
        <PageHeader title="Campaign not found" description="" />
        <Link to="/admin/acknowledgements" className="inline-flex items-center gap-1 text-sm text-[var(--color-primary)] hover:underline">
          <ArrowLeft className="h-3.5 w-3.5" /> Back to campaigns
        </Link>
      </div>
    )
  }

  const isOpen = campaign.status !== 'closed' && campaign.status !== 'archived'
  const dueDate = new Date(campaign.due_at)
  const overdue = isOpen && dueDate.getTime() < Date.now()

  return (
    <div>
      <PageHeader
        title={campaign.title}
        description={`Acknowledgement campaign · ${campaign.status}`}
        actions={
          isOpen ? (
            <Button variant="destructive" onClick={() => setConfirmClose(true)} loading={close.isPending}>
              Close campaign
            </Button>
          ) : undefined
        }
      />

      <div className="mb-6 flex items-center gap-2 text-sm">
        <Link to="/admin/acknowledgements" className="inline-flex items-center gap-1 text-[var(--color-text-secondary)] hover:underline">
          <ArrowLeft className="h-3.5 w-3.5" /> All campaigns
        </Link>
      </div>

      <div className="mb-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Field label="Status">
          <Badge variant={campaign.status}>{campaign.status}</Badge>
        </Field>
        <Field label={overdue ? 'Due (overdue)' : 'Due'}>
          <span className={`text-sm ${overdue ? 'text-red-600 dark:text-red-400' : ''}`}>
            {formatDateTime(campaign.due_at)}
          </span>
        </Field>
        <Field label="Created">
          <span className="text-sm">{formatDateTime(campaign.created_at)}</span>
        </Field>
        <Field label={campaign.closed_at ? 'Closed' : 'Document'}>
          {campaign.closed_at ? (
            <span className="text-sm">{formatDateTime(campaign.closed_at)}</span>
          ) : doc ? (
            <Link
              to="/workspaces/$workspaceId/documents/$documentId"
              params={{ workspaceId: doc.workspace_id, documentId: doc.id }}
              className="inline-flex items-center gap-1 text-sm text-[var(--color-primary)] hover:underline"
            >
              <FileText className="h-3.5 w-3.5" /> {doc.title}
            </Link>
          ) : (
            <span className="text-sm text-[var(--color-text-secondary)]">{campaign.document_id.slice(0, 8)}…</span>
          )}
        </Field>
      </div>

      {report && (
        <section className="mb-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
          <h2 className="mb-3 flex items-center gap-2 text-sm font-semibold">
            <ClipboardCheck className="h-4 w-4" aria-hidden="true" /> Acknowledgement report
            <span className="ml-auto text-[10px] font-normal uppercase tracking-wide text-[var(--color-text-secondary)]">
              live · refreshes every 10s
            </span>
          </h2>
          <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Stat label="Total assigned" value={String(report.total)} />
            <Stat
              label="Acknowledged"
              value={String(report.acknowledged)}
              accent={report.acknowledged === report.total ? 'good' : undefined}
            />
            <Stat
              label="Overdue"
              value={String(report.overdue)}
              accent={report.overdue > 0 ? 'warn' : undefined}
            />
            <Stat
              label="Ack rate"
              value={`${Math.round(report.acknowledgement_rate * 100)}%`}
            />
          </dl>
          {report.escalated > 0 && (
            <p className="mt-3 inline-flex items-center gap-1.5 rounded bg-amber-50 px-2 py-1 text-xs text-amber-900 dark:bg-amber-950/40 dark:text-amber-200">
              <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
              {report.escalated} assignment{report.escalated === 1 ? '' : 's'} escalated to manager.
            </p>
          )}
        </section>
      )}

      {campaign.body_md && (
        <section>
          <h2 className="mb-2 text-sm font-semibold">Policy body</h2>
          <pre className="overflow-x-auto whitespace-pre-wrap rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 font-sans text-sm leading-relaxed">
            {campaign.body_md}
          </pre>
          <p className="mt-1 text-[10px] text-[var(--color-text-secondary)]">
            Markdown shown verbatim — recipients see the rendered version in their inbox.
          </p>
        </section>
      )}

      <p className="mt-6 text-[10px] text-[var(--color-text-secondary)]">
        Per-assignee detail (ip, user-agent, attestation hash) is available
        only via the audit export — the public list-assignments surface is
        intentionally closed to avoid leaking PII.
      </p>

      <ConfirmDialog
        open={confirmClose}
        onOpenChange={setConfirmClose}
        title="Close this campaign?"
        description="No further reminders or escalations will be sent. Already-acknowledged attestations remain valid; pending assignees will be flagged as un-acknowledged in the audit trail."
        confirmLabel="Close campaign"
        destructive
        loading={close.isPending}
        onConfirm={() => close.mutate()}
      />
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3">
      <div className="text-xs uppercase tracking-wide text-[var(--color-text-secondary)]">{label}</div>
      <div className="mt-1">{children}</div>
    </div>
  )
}

function Stat({ label, value, accent }: { label: string; value: string; accent?: 'good' | 'warn' }) {
  const color =
    accent === 'good'
      ? 'text-emerald-700 dark:text-emerald-300'
      : accent === 'warn'
        ? 'text-amber-700 dark:text-amber-300'
        : ''
  return (
    <div>
      <dt className="text-xs text-[var(--color-text-secondary)]">{label}</dt>
      <dd className={`text-lg font-semibold ${color}`}>{value}</dd>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/acknowledgements/$campaignId')({
  component: CampaignDetailPage,
})
