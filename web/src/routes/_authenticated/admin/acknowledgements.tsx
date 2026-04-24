import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Plus, ClipboardCheck, X as XIcon } from 'lucide-react'

import {
  closeCampaign,
  createCampaign,
  getReport,
  listCampaigns,
  type Campaign,
} from '@/api/acknowledgements'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Textarea } from '@/components/ui/Textarea'
import { Skeleton } from '@/components/ui/Skeleton'
import { Badge } from '@/components/ui/Badge'

function AdminAckPage() {
  const [creating, setCreating] = useState(false)
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['ack-campaigns'],
    queryFn: () => listCampaigns(),
  })

  const closeMutation = useMutation({
    mutationFn: (id: string) => closeCampaign(id),
    onSuccess: () => {
      toast.success('Campaign closed')
      qc.invalidateQueries({ queryKey: ['ack-campaigns'] })
    },
    onError: () => toast.error('Could not close campaign'),
  })

  return (
    <div>
      <PageHeader
        title="Acknowledgement campaigns"
        description="Distribute a policy and require attested acknowledgement."
        actions={
          <Button onClick={() => setCreating(true)} aria-label="Create campaign">
            <Plus className="mr-1 h-4 w-4" aria-hidden="true" />
            New campaign
          </Button>
        }
      />

      {creating && (
        <CreateCampaignForm
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false)
            qc.invalidateQueries({ queryKey: ['ack-campaigns'] })
          }}
        />
      )}

      {isLoading && (
        <div role="status" aria-live="polite" className="space-y-2">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      )}

      {!isLoading && (!data || data.length === 0) && (
        <EmptyState
          icon={<ClipboardCheck className="h-10 w-10" />}
          title="No campaigns yet"
          description="Create one to start tracking policy acknowledgements."
        />
      )}

      {!isLoading && data && data.length > 0 && (
        <ul className="space-y-3" aria-label="Acknowledgement campaigns">
          {data.map((c) => (
            <CampaignRow
              key={c.id}
              campaign={c}
              onClose={() => closeMutation.mutate(c.id)}
              busy={closeMutation.isPending}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

function CampaignRow({
  campaign,
  onClose,
  busy,
}: {
  campaign: Campaign
  onClose: () => void
  busy: boolean
}) {
  const { data: report } = useQuery({
    queryKey: ['ack-report', campaign.id],
    queryFn: () => getReport(campaign.id),
    // Reports are cheap to compute and operators want them live; 10 s
    // poll is a middle ground.
    refetchInterval: 10_000,
  })
  const status = campaign.status
  return (
    <li className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h3 className="truncate text-base font-medium text-[var(--color-text)]">
              {campaign.title}
            </h3>
            <Badge variant={status}>{status}</Badge>
          </div>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
            Due {new Date(campaign.due_at).toLocaleDateString()} · Document{' '}
            <code className="rounded bg-slate-100 px-1 py-0.5 text-xs dark:bg-slate-800">
              {campaign.document_id.slice(0, 8)}
            </code>
          </p>
          {report && (
            <dl className="mt-2 grid grid-cols-4 gap-2 text-xs">
              <Stat label="Total" value={String(report.total)} />
              <Stat label="Acknowledged" value={String(report.acknowledged)} />
              <Stat label="Overdue" value={String(report.overdue)} />
              <Stat
                label="Ack rate"
                value={`${Math.round(report.acknowledgement_rate * 100)}%`}
              />
            </dl>
          )}
        </div>
        {status !== 'closed' && status !== 'archived' && (
          <Button
            variant="ghost"
            onClick={onClose}
            loading={busy}
            aria-label={`Close campaign ${campaign.title}`}
          >
            Close
          </Button>
        )}
      </div>
    </li>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-[var(--color-text-secondary)]">{label}</dt>
      <dd className="font-medium">{value}</dd>
    </div>
  )
}

function CreateCampaignForm({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: () => void
}) {
  const [documentId, setDocumentId] = useState('')
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [dueAt, setDueAt] = useState(defaultDue())
  const [users, setUsers] = useState('')

  const create = useMutation({
    mutationFn: () =>
      createCampaign({
        document_id: documentId.trim(),
        title: title.trim(),
        body_md: body,
        due_at: new Date(dueAt).toISOString(),
        recipient_policy: {
          users: users.split(/[\s,]+/).filter(Boolean),
        },
        activate: true,
      }),
    onSuccess: () => {
      toast.success('Campaign created')
      onCreated()
    },
    onError: (err: unknown) => {
      const msg =
        err && typeof err === 'object' && 'response' in err
          ? // eslint-disable-next-line @typescript-eslint/no-explicit-any
            (err as any).response?.data?.error?.message ?? 'Could not create'
          : 'Could not create'
      toast.error(msg)
    },
  })

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        create.mutate()
      }}
      className="mb-4 space-y-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
      aria-label="Create campaign"
    >
      <div className="flex items-center justify-between">
        <h3 className="text-base font-medium">New campaign</h3>
        <button
          type="button"
          onClick={onClose}
          className="rounded-md p-1 hover:bg-slate-100 dark:hover:bg-slate-800"
          aria-label="Cancel"
        >
          <XIcon className="h-4 w-4" aria-hidden="true" />
        </button>
      </div>
      <Input label="Document ID" value={documentId} onChange={(e) => setDocumentId(e.target.value)} required />
      <Input label="Title" value={title} onChange={(e) => setTitle(e.target.value)} required />
      <Textarea
        label="Body (Markdown)"
        value={body}
        onChange={(e) => setBody(e.target.value)}
        rows={4}
      />
      <Input
        type="datetime-local"
        label="Due at"
        value={dueAt}
        onChange={(e) => setDueAt(e.target.value)}
        required
      />
      <Textarea
        label="Recipient user IDs (comma or whitespace separated)"
        value={users}
        onChange={(e) => setUsers(e.target.value)}
        rows={3}
        required
      />
      <div className="flex justify-end gap-2">
        <Button variant="ghost" type="button" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" loading={create.isPending}>
          Create & activate
        </Button>
      </div>
    </form>
  )
}

function defaultDue(): string {
  const d = new Date()
  d.setDate(d.getDate() + 14)
  return d.toISOString().slice(0, 16)
}

export const Route = createFileRoute('/_authenticated/admin/acknowledgements')({
  component: AdminAckPage,
})
