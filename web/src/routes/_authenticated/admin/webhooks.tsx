import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { ChevronDown, ChevronRight, Copy, RefreshCw, Send, Trash2, Webhook, AlertTriangle } from 'lucide-react'

import {
  createWebhook,
  deleteWebhook,
  listDeliveries,
  listWebhooks,
  redeliverDelivery,
  rotateWebhookSecret,
  type Webhook as WebhookT,
} from '@/api/webhooks'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/shadcn/input'
import { Skeleton } from '@/components/ui/Skeleton'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { formatDate, formatRelativeTime } from '@/lib/formatters'
import { cn } from '@/lib/cn'

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
  const [pendingDelete, setPendingDelete] = useState<WebhookT | null>(null)

  const create = useMutation({
    mutationFn: () => createWebhook({ url, events }),
    onSuccess: (wh) => {
      toast.success('Webhook created')
      setUrl(''); setEvents([])
      if (wh.secret) setNewSecret({ id: wh.id, secret: wh.secret })
      qc.invalidateQueries({ queryKey: ['webhooks'] })
    },
    onError: () => toast.error('Create failed'),
  })

  const remove = useMutation({
    mutationFn: (id: string) => deleteWebhook(id),
    onSuccess: () => {
      toast.success('Webhook deleted')
      setPendingDelete(null)
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

  const copy = (txt: string) => navigator.clipboard.writeText(txt).then(() => toast.success('Copied'))

  return (
    <div className="space-y-6">
      <PageHeader
        title="Webhooks"
        description="Subscribe external systems to domain events. Each delivery is signed with HMAC-SHA256 using the per-webhook secret; failed deliveries retry with exponential backoff and dead-letter after 5 attempts."
      />

      {newSecret && (
        <Card className="border-warning/40 bg-warning/5 p-4">
          <div className="flex items-start gap-3">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
            <div className="min-w-0 flex-1 space-y-2">
              <p className="text-sm font-medium text-foreground">New secret — copy it now, it won't be shown again.</p>
              <div className="flex items-center gap-2">
                <code className="flex-1 truncate rounded bg-background px-2 py-1.5 font-mono text-xs">{newSecret.secret}</code>
                <Button variant="outline" size="sm" onClick={() => copy(newSecret.secret)}>
                  <Copy className="h-4 w-4" /> Copy
                </Button>
                <Button variant="ghost" size="sm" onClick={() => setNewSecret(null)}>Dismiss</Button>
              </div>
            </div>
          </div>
        </Card>
      )}

      <Card className="space-y-3 p-5">
        <h3 className="flex items-center gap-2 text-sm font-semibold">
          <Webhook className="h-4 w-4" /> Subscribe a new endpoint
        </h3>
        <Input
          label="Endpoint URL"
          placeholder="https://your-service.example.com/hooks/vaultdms"
          type="url"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
        />
        <div>
          <label className="mb-1.5 block text-sm font-medium">Events</label>
          <div className="flex flex-wrap gap-2">
            {EVENT_PRESETS.map((e) => {
              const on = events.includes(e)
              return (
                <button
                  key={e}
                  type="button"
                  onClick={() => toggleEvent(e)}
                  className={cn(
                    'inline-flex items-center gap-1 rounded-md border px-2 py-1 font-mono text-[11px] transition-colors',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                    on
                      ? 'border-foreground bg-foreground text-background'
                      : 'border-border bg-background text-muted-foreground hover:border-foreground/50 hover:text-foreground',
                  )}
                >
                  {e}
                </button>
              )
            })}
          </div>
        </div>
        <div className="flex justify-end">
          <Button onClick={() => create.mutate()} disabled={!url || events.length === 0} loading={create.isPending}>
            Create webhook
          </Button>
        </div>
      </Card>

      {isLoading ? (
        <Skeleton className="h-32" />
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Webhook className="h-6 w-6" />}
          title="No webhooks configured"
          description="Subscribe an endpoint above to start receiving HTTP callbacks for the events you choose."
        />
      ) : (
        <ul className="space-y-2">
          {data.map((wh) => (
            <WebhookRow
              key={wh.id}
              wh={wh}
              expanded={expanded === wh.id}
              onToggle={() => setExpanded(expanded === wh.id ? null : wh.id)}
              onRotate={() => rotate.mutate(wh.id)}
              onDelete={() => setPendingDelete(wh)}
              rotating={rotate.isPending}
            />
          ))}
        </ul>
      )}

      <ConfirmDialog
        open={!!pendingDelete}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title="Delete webhook?"
        description={pendingDelete ? `${pendingDelete.url} will stop receiving deliveries immediately.` : ''}
        confirmLabel="Delete webhook"
        destructive
        loading={remove.isPending}
        onConfirm={() => pendingDelete && remove.mutate(pendingDelete.id)}
      />
    </div>
  )
}

function WebhookRow({
  wh, expanded, onToggle, onRotate, onDelete, rotating,
}: {
  wh: WebhookT
  expanded: boolean
  onToggle: () => void
  onRotate: () => void
  onDelete: () => void
  rotating: boolean
}) {
  return (
    <li>
      <Card className="overflow-hidden p-0">
        <div className="flex items-start justify-between gap-3 p-3">
          <button type="button" onClick={onToggle} className="flex min-w-0 flex-1 items-start gap-2 text-left">
            {expanded ? <ChevronDown className="mt-1 h-4 w-4 shrink-0 text-muted-foreground" /> : <ChevronRight className="mt-1 h-4 w-4 shrink-0 text-muted-foreground" />}
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span className="truncate text-sm font-medium">{wh.url}</span>
                <Badge variant={wh.active ? 'active' : 'archived'}>{wh.active ? 'Active' : 'Inactive'}</Badge>
              </div>
              <div className="mt-1 flex flex-wrap gap-1">
                {wh.events.map((e) => (
                  <code key={e} className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]">{e}</code>
                ))}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                Created {formatRelativeTime(wh.created_at)} ({formatDate(wh.created_at)})
              </div>
            </div>
          </button>
          <div className="flex shrink-0 gap-1">
            <Button variant="outline" size="sm" onClick={onRotate} disabled={rotating}>
              <RefreshCw className={cn('h-4 w-4', rotating && 'animate-spin')} /> Rotate
            </Button>
            <Button variant="ghost" size="sm" onClick={onDelete} aria-label="Delete">
              <Trash2 className="h-4 w-4 text-destructive" />
            </Button>
          </div>
        </div>
        {expanded && <DeliveryLog webhookId={wh.id} />}
      </Card>
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

  if (isLoading) return <div className="border-t border-border p-3"><Skeleton className="h-12" /></div>
  if (!data || data.length === 0) {
    return (
      <div className="border-t border-border bg-muted/30 p-4 text-center text-xs text-muted-foreground">
        No deliveries yet.
      </div>
    )
  }

  return (
    <div className="overflow-x-auto border-t border-border">
      <table className="w-full text-xs">
        <thead className="bg-muted/40">
          <tr className="text-left">
            <th className="px-3 py-2 font-medium uppercase tracking-wider text-muted-foreground">Event</th>
            <th className="px-3 py-2 font-medium uppercase tracking-wider text-muted-foreground">Status</th>
            <th className="px-3 py-2 font-medium uppercase tracking-wider text-muted-foreground">Attempts</th>
            <th className="px-3 py-2 font-medium uppercase tracking-wider text-muted-foreground">When</th>
            <th className="px-3 py-2"></th>
          </tr>
        </thead>
        <tbody>
          {data.map((d) => (
            <tr key={d.id} className="border-t border-border">
              <td className="px-3 py-2 font-mono">{d.event_type}</td>
              <td className="px-3 py-2">
                <Badge variant={deliveryVariant(d.status_code, d.dead_lettered)}>
                  {d.dead_lettered ? 'DLQ' : d.delivered_at ? d.status_code || 200 : d.status_code || 'pending'}
                </Badge>
              </td>
              <td className="px-3 py-2">{d.attempts}</td>
              <td className="px-3 py-2 text-muted-foreground">{formatRelativeTime(d.created_at)}</td>
              <td className="px-3 py-2 text-right">
                <Button variant="ghost" size="sm" onClick={() => redeliver.mutate(d.id)} disabled={redeliver.isPending}>
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
