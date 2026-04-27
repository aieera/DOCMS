import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Webhook, Copy, Trash2, RefreshCw, Send, ChevronDown, ChevronRight, Pencil, Pause, Play } from 'lucide-react'

import {
  createWebhook,
  deleteWebhook,
  listDeliveries,
  listWebhooks,
  patchWebhook,
  redeliverDelivery,
  rotateWebhookSecret,
  type Webhook as WebhookT,
} from '@/api/webhooks'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatDate, formatRelativeTime } from '@/lib/formatters'

const EVENT_PRESETS = [
  'dms.document.created.v1',
  'dms.document.updated.v1',
  'dms.document.deleted.v1',
  'dms.version.uploaded.v1',
  'dms.review.approved.v1',
  'dms.review.rejected.v1',
  'dms.signature.completed.v1',
  'dms.hold.applied.v1',
  'dms.dsr.completed.v1',
]

function WebhooksPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({ queryKey: ['webhooks'], queryFn: listWebhooks })

  const [url, setUrl] = useState('')
  const [events, setEvents] = useState<string[]>([])
  const [expanded, setExpanded] = useState<string | null>(null)
  const [newSecret, setNewSecret] = useState<{ id: string; secret: string } | null>(null)
  const [editing, setEditing] = useState<WebhookT | null>(null)

  const patch = useMutation({
    mutationFn: ({ id, body }: { id: string; body: { url?: string; events?: string[]; active?: boolean } }) =>
      patchWebhook(id, body),
    onSuccess: () => {
      toast.success('Webhook updated')
      qc.invalidateQueries({ queryKey: ['webhooks'] })
      setEditing(null)
    },
    onError: (err: unknown) => {
      const anyErr = err as { response?: { data?: { error?: string } }; message?: string }
      toast.error(anyErr?.response?.data?.error ?? anyErr?.message ?? 'Update failed')
    },
  })

  const create = useMutation({
    mutationFn: () => createWebhook({ url, events }),
    onSuccess: (wh) => {
      toast.success('Webhook created')
      setUrl('')
      setEvents([])
      if (wh.secret) setNewSecret({ id: wh.id, secret: wh.secret })
      qc.invalidateQueries({ queryKey: ['webhooks'] })
    },
    onError: () => toast.error('Create failed'),
  })

  const remove = useMutation({
    mutationFn: (id: string) => deleteWebhook(id),
    onSuccess: () => {
      toast.success('Webhook deleted')
      qc.invalidateQueries({ queryKey: ['webhooks'] })
    },
  })

  const rotate = useMutation({
    mutationFn: (id: string) => rotateWebhookSecret(id),
    onSuccess: (wh) => {
      if (wh.secret) setNewSecret({ id: wh.id, secret: wh.secret })
      toast.success('Secret rotated — copy it now')
      qc.invalidateQueries({ queryKey: ['webhooks'] })
    },
  })

  const toggleEvent = (e: string) =>
    setEvents((prev) => (prev.includes(e) ? prev.filter((x) => x !== e) : [...prev, e]))

  const copy = (txt: string) => {
    navigator.clipboard.writeText(txt).then(() => toast.success('Copied'))
  }

  return (
    <div>
      <PageHeader title="Webhooks" description="Subscribe external systems to domain events." />

      {newSecret && (
        <div className="mb-4 rounded-lg border border-amber-400 bg-amber-50 p-3 dark:bg-amber-950/30">
          <div className="mb-1 text-sm font-medium">New secret — shown once. Store it now.</div>
          <div className="flex items-center gap-2">
            <code className="flex-1 truncate rounded bg-[var(--color-bg)] px-2 py-1 font-mono text-xs">
              {newSecret.secret}
            </code>
            <Button onClick={() => copy(newSecret.secret)}>
              <Copy className="h-4 w-4" /> Copy
            </Button>
            <Button onClick={() => setNewSecret(null)}>Dismiss</Button>
          </div>
        </div>
      )}

      <div className="mb-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
        <h3 className="mb-3 flex items-center gap-2 font-medium">
          <Webhook className="h-4 w-4" /> New webhook
        </h3>
        <input
          className="mb-2 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
          placeholder="https://your-service.example.com/hooks/vaultdms"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
        />
        <div className="mb-2 flex flex-wrap gap-2">
          {EVENT_PRESETS.map((e) => (
            <label key={e} className="flex items-center gap-1 text-xs">
              <input
                type="checkbox"
                checked={events.includes(e)}
                onChange={() => toggleEvent(e)}
              />
              <span>{e}</span>
            </label>
          ))}
        </div>
        <Button
          onClick={() => create.mutate()}
          disabled={!url || events.length === 0 || create.isPending}
        >
          Create
        </Button>
      </div>

      {isLoading ? (
        <Skeleton className="h-24" />
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Webhook className="h-12 w-12" />}
          title="No webhooks configured"
          description="Subscribe to events to receive real-time HTTP callbacks."
        />
      ) : (
        <>
        <EditWebhookDialog
          webhook={editing}
          onClose={() => setEditing(null)}
          onSubmit={(body) => editing && patch.mutate({ id: editing.id, body })}
          submitting={patch.isPending}
        />
        <ul className="space-y-2">
          {data.map((wh) => (
            <WebhookRow
              key={wh.id}
              wh={wh}
              expanded={expanded === wh.id}
              onToggle={() => setExpanded(expanded === wh.id ? null : wh.id)}
              onRotate={() => rotate.mutate(wh.id)}
              onToggleActive={() => patch.mutate({ id: wh.id, body: { active: !wh.active } })}
              onEdit={() => setEditing(wh)}
              onDelete={() => {
                if (window.confirm(`Delete webhook ${wh.url}?`)) remove.mutate(wh.id)
              }}
              rotating={rotate.isPending}
              patching={patch.isPending}
              deleting={remove.isPending}
            />
          ))}
        </ul>
        </>
      )}
    </div>
  )
}

function EditWebhookDialog({
  webhook,
  onClose,
  onSubmit,
  submitting,
}: {
  webhook: WebhookT | null
  onClose: () => void
  onSubmit: (body: { url?: string; events?: string[] }) => void
  submitting: boolean
}) {
  const [url, setUrl] = useState('')
  const [events, setEvents] = useState<string[]>([])
  const open = webhook !== null
  // Re-seed local state each time a different webhook is opened. Using
  // `webhook?.id` as the dependency avoids fighting React Query when
  // a refetch returns a new object identity for the same row.
  useEffect(() => {
    if (webhook) {
      setUrl(webhook.url)
      setEvents(webhook.events)
    }
  }, [webhook?.id])

  if (!webhook) return null

  // Only send fields that actually changed; an empty events array would
  // otherwise unsubscribe the webhook from everything in one click.
  const submit = () => {
    const body: { url?: string; events?: string[] } = {}
    if (url !== webhook.url) body.url = url
    if (
      events.length !== webhook.events.length ||
      events.some((e) => !webhook.events.includes(e))
    ) {
      body.events = events
    }
    if (Object.keys(body).length === 0) {
      toast('No changes to save', { icon: 'ℹ️' })
      return
    }
    onSubmit(body)
  }

  const toggle = (e: string) =>
    setEvents((prev) => (prev.includes(e) ? prev.filter((x) => x !== e) : [...prev, e]))

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()} title="Edit webhook" size="md">
      <div className="space-y-3">
        <Input label="URL" value={url} onChange={(e) => setUrl(e.target.value)} />
        <div>
          <label className="mb-1 block text-xs font-medium">Events</label>
          <div className="flex flex-wrap gap-2">
            {EVENT_PRESETS.map((e) => (
              <label key={e} className="flex items-center gap-1 text-xs">
                <input type="checkbox" checked={events.includes(e)} onChange={() => toggle(e)} />
                <span>{e}</span>
              </label>
            ))}
          </div>
          {events.length === 0 && (
            <p className="mt-1 text-xs text-amber-600 dark:text-amber-400">
              Saving with zero events will unsubscribe the webhook from every topic.
            </p>
          )}
        </div>
        <div className="flex justify-end gap-2 pt-3">
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={submitting} onClick={submit}>Save changes</Button>
        </div>
      </div>
    </Dialog>
  )
}

function WebhookRow({
  wh,
  expanded,
  onToggle,
  onRotate,
  onToggleActive,
  onEdit,
  onDelete,
  rotating,
  patching,
  deleting,
}: {
  wh: WebhookT
  expanded: boolean
  onToggle: () => void
  onRotate: () => void
  onToggleActive: () => void
  onEdit: () => void
  onDelete: () => void
  rotating: boolean
  patching: boolean
  deleting: boolean
}) {
  return (
    <li className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)]">
      <div className="flex items-start justify-between p-3">
        <button onClick={onToggle} className="flex min-w-0 flex-1 items-start gap-2 text-left">
          {expanded ? <ChevronDown className="mt-1 h-4 w-4" /> : <ChevronRight className="mt-1 h-4 w-4" />}
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="truncate font-medium">{wh.url}</span>
              <Badge variant={wh.active ? 'active' : 'archived'}>
                {wh.active ? 'Active' : 'Inactive'}
              </Badge>
            </div>
            <div className="mt-1 flex flex-wrap gap-1">
              {wh.events.map((e) => (
                <code key={e} className="rounded bg-[var(--color-bg)] px-1.5 py-0.5 text-xs">
                  {e}
                </code>
              ))}
            </div>
            <div className="mt-1 text-xs text-[var(--color-text-secondary)]">
              Created {formatRelativeTime(wh.created_at)} on {formatDate(wh.created_at)}
            </div>
          </div>
        </button>
        <div className="flex shrink-0 gap-2">
          <Button
            onClick={onToggleActive}
            disabled={patching}
            title={wh.active ? 'Pause deliveries — config and history are preserved' : 'Resume deliveries'}
          >
            {wh.active ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4" />}
            {wh.active ? 'Pause' : 'Resume'}
          </Button>
          <Button onClick={onEdit} disabled={patching}>
            <Pencil className="h-4 w-4" /> Edit
          </Button>
          <Button onClick={onRotate} disabled={rotating}>
            <RefreshCw className="h-4 w-4" /> Rotate
          </Button>
          <Button onClick={onDelete} disabled={deleting}>
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </div>
      {expanded && <DeliveryLog webhookId={wh.id} />}
    </li>
  )
}

function DeliveryLog({ webhookId }: { webhookId: string }) {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['webhook-deliveries', webhookId],
    queryFn: () => listDeliveries(webhookId),
  })
  const redeliver = useMutation({
    mutationFn: (deliveryId: string) => redeliverDelivery(webhookId, deliveryId),
    onSuccess: () => {
      toast.success('Queued for redelivery')
      qc.invalidateQueries({ queryKey: ['webhook-deliveries', webhookId] })
    },
    onError: () => toast.error('Redelivery failed'),
  })

  if (isLoading) return <div className="border-t border-[var(--color-border)] p-3"><Skeleton className="h-12" /></div>
  if (!data || data.length === 0) {
    return (
      <div className="border-t border-[var(--color-border)] p-3 text-sm text-[var(--color-text-secondary)]">
        No deliveries yet.
      </div>
    )
  }

  return (
    <div className="border-t border-[var(--color-border)]">
      <table className="w-full text-xs">
        <thead className="bg-slate-50 dark:bg-slate-800/50">
          <tr className="text-left">
            <th className="px-3 py-1.5">Event</th>
            <th className="px-3 py-1.5">Status</th>
            <th className="px-3 py-1.5">Attempts</th>
            <th className="px-3 py-1.5">When</th>
            <th className="px-3 py-1.5"></th>
          </tr>
        </thead>
        <tbody>
          {data.map((d) => (
            <tr key={d.id} className="border-t border-[var(--color-border)]">
              <td className="px-3 py-1.5 font-mono">{d.event_type}</td>
              <td className="px-3 py-1.5">
                <Badge variant={deliveryVariant(d.status_code, d.dead_lettered)}>
                  {d.dead_lettered ? 'DLQ' : d.delivered_at ? d.status_code || 200 : d.status_code || 'pending'}
                </Badge>
              </td>
              <td className="px-3 py-1.5">{d.attempts}</td>
              <td className="px-3 py-1.5">{formatRelativeTime(d.created_at)}</td>
              <td className="px-3 py-1.5 text-right">
                <Button onClick={() => redeliver.mutate(d.id)} disabled={redeliver.isPending}>
                  <Send className="h-3 w-3" /> Redeliver
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function deliveryVariant(status: number, dlq: boolean): string {
  if (dlq) return 'disposed'
  if (status === 0) return 'in_review'
  if (status >= 200 && status < 300) return 'active'
  return 'disposed'
}

export const Route = createFileRoute('/_authenticated/admin/webhooks')({ component: WebhooksPage })
