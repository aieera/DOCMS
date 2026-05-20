import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ChevronDown, Code2, Copy, RefreshCw, Send, Trash2, Webhook, AlertTriangle, Beaker } from 'lucide-react'
import {
  createWebhook,
  deleteWebhook,
  listDeliveries,
  listWebhooks,
  redeliverDelivery,
  rotateWebhookSecret,
  sendTestWebhook,
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
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

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

  const test = useMutation({
    mutationFn: (id: string) => sendTestWebhook(id),
    onSuccess: (_d, id) => {
      toast.success('Test delivery queued — check the delivery log')
      qc.invalidateQueries({ queryKey: ['webhook-deliveries', id] })
    },
    onError: () => toast.error('Test send failed'),
  })

  const toggleEvent = (e: string) =>
    setEvents((prev) => (prev.includes(e) ? prev.filter((x) => x !== e) : [...prev, e]))

  const copy = (txt: string) => navigator.clipboard.writeText(txt).then(() => toast.success('Copied'))

  return (
    <div className="space-y-6">
      <PageHeader
        title="Webhooks"
        description="Subscribe external systems to domain events. Each delivery is signed HMAC-SHA256 (X-DMS-Signature + X-DMS-Timestamp); failed deliveries retry 5s / 30s / 2m / 15m / 1h / 6h then dead-letter."
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
          <Button
            onClick={() => {
              if (!url?.trim()) { toast.error('URL is required'); return }
              if (!/^https?:\/\//.test(url.trim())) { toast.error('URL must start with http:// or https://'); return }
              if (events.length === 0) { toast.error('Select at least one event'); return }
              create.mutate()
            }}
            disabled={create.isPending}
            loading={create.isPending}
          >
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
              onTest={() => test.mutate(wh.id)}
              onDelete={() => setPendingDelete(wh)}
              rotating={rotate.isPending}
              testing={test.isPending && test.variables === wh.id}
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
  wh, expanded, onToggle, onRotate, onTest, onDelete, rotating, testing,
}: {
  wh: WebhookT
  expanded: boolean
  onToggle: () => void
  onRotate: () => void
  onTest: () => void
  onDelete: () => void
  rotating: boolean
  testing: boolean
}) {
  const [showSnippet, setShowSnippet] = useState(false)
  return (
    <li>
      <Card className="overflow-hidden p-0">
        <div className="flex items-start justify-between gap-3 p-3">
          <button type="button" onClick={onToggle} className="flex min-w-0 flex-1 items-start gap-2 text-start">
            {expanded ? <ChevronDown className="mt-1 h-4 w-4 shrink-0 text-muted-foreground" /> : <DirectionalIcon name="ChevronRight" className="mt-1 h-4 w-4 shrink-0 text-muted-foreground" />}
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
            <Button variant="outline" size="sm" onClick={onTest} disabled={testing} data-testid={`webhook-test-${wh.id}`}>
              <Beaker className={cn('h-4 w-4', testing && 'animate-pulse')} /> Test send
            </Button>
            <Button variant="outline" size="sm" onClick={() => setShowSnippet((s) => !s)} data-testid={`webhook-verify-${wh.id}`}>
              <Code2 className="h-4 w-4" /> Verify
            </Button>
            <Button variant="outline" size="sm" onClick={onRotate} disabled={rotating}>
              <RefreshCw className={cn('h-4 w-4', rotating && 'animate-spin')} /> Rotate
            </Button>
            <Button variant="ghost" size="sm" onClick={onDelete} aria-label="Delete">
              <Trash2 className="h-4 w-4 text-destructive" />
            </Button>
          </div>
        </div>
        {showSnippet && <SignatureSamples />}
        {expanded && <DeliveryLog webhookId={wh.id} />}
      </Card>
    </li>
  )
}

// SignatureSamples renders the HMAC verification snippet across the
// languages our customers are most likely to land on. The signing
// input is `<timestamp>.<raw-body>` — same shape Stripe popularized.
// Keep the snippets in sync with services/connector/internal/webhook
// delivery.go::signPayload.
function SignatureSamples() {
  const [lang, setLang] = useState<keyof typeof SAMPLES>('node')
  const copy = (txt: string) => navigator.clipboard.writeText(txt).then(() => toast.success('Copied'))
  return (
    <div className="border-t border-border bg-muted/30 p-4">
      <div className="mb-2 flex items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          Verify <code className="font-mono">X-DMS-Signature</code> against{' '}
          <code className="font-mono">{'<timestamp>.<raw-body>'}</code>. Reject the request if the
          timestamp is more than 5 minutes old (replay protection).
        </p>
        <div className="flex shrink-0 gap-1">
          {(Object.keys(SAMPLES) as Array<keyof typeof SAMPLES>).map((k) => (
            <button
              key={k}
              type="button"
              onClick={() => setLang(k)}
              className={cn(
                'rounded px-2 py-1 font-mono text-[11px] uppercase tracking-wider transition-colors',
                lang === k
                  ? 'bg-foreground text-background'
                  : 'bg-background text-muted-foreground hover:text-foreground',
              )}
              data-testid={`verify-lang-${k}`}
            >
              {k}
            </button>
          ))}
        </div>
      </div>
      <div className="relative">
        <pre className="max-h-72 overflow-auto rounded-md border border-border bg-background p-3 font-mono text-[11px] leading-relaxed">
{SAMPLES[lang]}
        </pre>
        <Button
          variant="outline"
          size="sm"
          onClick={() => copy(SAMPLES[lang])}
          className="absolute end-2 top-2"
        >
          <Copy className="h-3 w-3" /> Copy
        </Button>
      </div>
    </div>
  )
}

const SAMPLES = {
  node: `// Node.js (Express)
import crypto from 'node:crypto'

app.post('/webhooks/vaultdms', express.raw({ type: 'application/json' }), (req, res) => {
  const sig = req.header('X-DMS-Signature') || ''
  const ts  = req.header('X-DMS-Timestamp') || ''
  if (Math.abs(Date.now() / 1000 - Number(ts)) > 300) return res.status(401).end()
  const expected = 'sha256=' + crypto
    .createHmac('sha256', process.env.VAULTDMS_WEBHOOK_SECRET)
    .update(ts + '.' + req.body.toString('utf8'))
    .digest('hex')
  if (!crypto.timingSafeEqual(Buffer.from(sig), Buffer.from(expected))) return res.status(401).end()
  res.status(204).end()
})`,
  python: `# Python (FastAPI)
import hmac, hashlib, time
from fastapi import FastAPI, Header, HTTPException, Request

SECRET = os.environ['VAULTDMS_WEBHOOK_SECRET'].encode()

@app.post('/webhooks/vaultdms')
async def hook(req: Request,
               x_dms_signature: str = Header(...),
               x_dms_timestamp: str = Header(...)):
    if abs(time.time() - int(x_dms_timestamp)) > 300:
        raise HTTPException(401, 'stale')
    body = await req.body()
    expected = 'sha256=' + hmac.new(SECRET, x_dms_timestamp.encode() + b'.' + body, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(expected, x_dms_signature):
        raise HTTPException(401, 'bad signature')
    return {'ok': True}`,
  go: `// Go (net/http)
import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "io"
    "net/http"
    "strconv"
    "time"
)

func handle(w http.ResponseWriter, r *http.Request) {
    sig := r.Header.Get("X-DMS-Signature")
    ts, _ := strconv.ParseInt(r.Header.Get("X-DMS-Timestamp"), 10, 64)
    if d := time.Now().Unix() - ts; d > 300 || d < -300 {
        http.Error(w, "stale", 401); return
    }
    body, _ := io.ReadAll(r.Body)
    mac := hmac.New(sha256.New, []byte(os.Getenv("VAULTDMS_WEBHOOK_SECRET")))
    mac.Write([]byte(strconv.FormatInt(ts, 10) + "."))
    mac.Write(body)
    expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
    if !hmac.Equal([]byte(expected), []byte(sig)) {
        http.Error(w, "bad signature", 401); return
    }
    w.WriteHeader(204)
}`,
  ruby: `# Ruby (Rails)
require 'openssl'
require 'rack/utils'

def verify
  ts  = request.headers['X-DMS-Timestamp']
  sig = request.headers['X-DMS-Signature']
  return head :unauthorized if (Time.now.to_i - ts.to_i).abs > 300
  body = request.raw_post
  expected = 'sha256=' + OpenSSL::HMAC.hexdigest('SHA256', ENV['VAULTDMS_WEBHOOK_SECRET'], "#{ts}.#{body}")
  return head :unauthorized unless Rack::Utils.secure_compare(expected, sig)
  head :no_content
end`,
  curl: `# Manual verification with curl + openssl
# (replay this with a captured X-DMS-Timestamp + body)
TS="$X_DMS_TIMESTAMP"
SECRET="$VAULTDMS_WEBHOOK_SECRET"
BODY="$(cat payload.json)"

EXPECTED="sha256=$(printf '%s.%s' "$TS" "$BODY" \\
    | openssl dgst -sha256 -hmac "$SECRET" -hex \\
    | awk '{print $2}')"

echo "got:      $X_DMS_SIGNATURE"
echo "expected: $EXPECTED"`,
} as const

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
          <tr className="text-start">
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
              <td className="px-3 py-2 text-end">
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
