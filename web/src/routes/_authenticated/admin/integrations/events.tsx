// /admin/integrations/events — ADR 0077 event-streaming console.
//
// Three panes:
//   1. Tokens — issue / revoke / list (bearer + NATS creds shown once).
//   2. Connection samples — copy-pasteable Go / Python / JS / curl.
//   3. Live tail — SSE stream of the tenant's own events, capped at 60s
//      windows so an abandoned browser tab doesn't pin a JetStream
//      consumer forever.
import { copyText } from '@/lib/clipboard'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import {
  AlertTriangle, Antenna, Copy, Download, Key, Play, Square, Trash2, Zap,
} from 'lucide-react'

import {
  issueEventStreamToken,
  listEventStreamTokens,
  revokeEventStreamToken,
  type EventStreamToken,
} from '@/api/event-stream'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/shadcn/input'
import { Skeleton } from '@/components/ui/Skeleton'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { formatDate, formatRelativeTime } from '@/lib/formatters'
import { cn } from '@/lib/cn'

// Subject prefixes are the NATS taxonomy and stay Latin; the prose comes
// from the catalogue as adm.evs.ref_<domain>.
const EVENT_TYPE_REFERENCE = [
  'document', 'version', 'workspace', 'signature', 'hold',
  'audit', 'user', 'sharelink', 'workflow', 'billing',
] as const

export function EventStreamPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const { data: tokens, isLoading } = useQuery({
    queryKey: ['event-stream-tokens'],
    queryFn: listEventStreamTokens,
  })

  const [label, setLabel] = useState('')
  const [issued, setIssued] = useState<EventStreamToken | null>(null)
  const [pendingRevoke, setPendingRevoke] = useState<EventStreamToken | null>(null)
  const [tab, setTab] = useState<keyof typeof SAMPLES>('go')

  const issue = useAppMutation({
    mutationFn: () => issueEventStreamToken(label.trim()),
    onSuccess: (tok) => {
      toast.success(t('adm.evs.issued'))
      setIssued(tok)
      setLabel('')
      qc.invalidateQueries({ queryKey: ['event-stream-tokens'] })
    },
    onError: () => toast.error(t('adm.evs.err_issue')),
  })

  const revoke = useAppMutation({
    mutationFn: (id: string) => revokeEventStreamToken(id),
    onSuccess: () => {
      toast.success(t('adm.evs.revoked_toast'))
      setPendingRevoke(null)
      qc.invalidateQueries({ queryKey: ['event-stream-tokens'] })
    },
  })

  const copy = (txt: string) =>
    copyText(txt).then(() => toast.success(t('adm.evs.copied')))
  const downloadCreds = (filename: string, content: string) => {
    const blob = new Blob([content], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('adm.evs.title')}
        description={t('adm.evs.description')}
      />

      {/* One-shot reveal of bearer + creds. */}
      {issued && (
        <Card className="border-warning/40 bg-warning/5 p-4" data-testid="issued-token-banner">
          <div className="flex items-start gap-3">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
            <div className="min-w-0 flex-1 space-y-3">
              <p className="text-sm font-medium text-foreground">
                {t('adm.evs.issued_banner', { label: issued.label })}
              </p>
              <div>
                <p className="mb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.evs.polling_bearer')}</p>
                <div className="flex items-center gap-2">
                  <code className="flex-1 truncate rounded bg-background px-2 py-1.5 font-mono text-xs">
                    {issued.bearer_token}
                  </code>
                  <Button variant="outline" size="sm" onClick={() => copy(issued.bearer_token || '')}>
                    <Copy className="h-4 w-4" /> {t('adm.evs.copy')}
                  </Button>
                </div>
              </div>
              <div>
                <p className="mb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.evs.nats_creds')}</p>
                <pre className="max-h-32 overflow-auto rounded bg-background p-2 font-mono text-[10px] leading-tight">
{issued.nats_creds_file}
                </pre>
                <div className="mt-1 flex gap-2">
                  <Button variant="outline" size="sm" onClick={() => copy(issued.nats_creds_file || '')}>
                    <Copy className="h-4 w-4" /> {t('adm.evs.copy')}
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => downloadCreds(`vaultdms-${issued.label}.creds`, issued.nats_creds_file || '')}
                    data-testid="creds-download"
                  >
                    <Download className="h-4 w-4" /> {t('adm.evs.download')}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => setIssued(null)}>{t('adm.evs.dismiss')}</Button>
                </div>
              </div>
            </div>
          </div>
        </Card>
      )}

      {/* Issue + list pane. */}
      <Card className="space-y-3 p-5">
        <h3 className="flex items-center gap-2 text-sm font-semibold">
          <Key className="h-4 w-4" /> {t('adm.evs.tokens')}
        </h3>
        <div className="flex gap-2">
          <Input
            placeholder={t('adm.evs.label_ph')}
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            data-testid="token-label"
          />
          <Button
            onClick={() => {
              if (!label.trim()) { toast.error(t('adm.evs.err_label')); return }
              issue.mutate()
            }}
            disabled={issue.isPending}
            loading={issue.isPending}
            data-testid="token-issue"
          >
            {t('adm.evs.issue')}
          </Button>
        </div>
        {isLoading ? (
          <Skeleton className="h-20" />
        ) : !tokens || tokens.length === 0 ? (
          <p className="rounded-md border border-dashed border-border bg-muted/30 p-4 text-center text-sm text-muted-foreground">
            {t('adm.evs.no_tokens')}
          </p>
        ) : (
          <ul className="divide-y divide-border rounded-md border border-border">
            {tokens.map((tok) => (
              <li key={tok.id} className="flex items-center justify-between gap-3 p-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="truncate text-sm font-medium">{tok.label}</span>
                    <Badge variant={tok.revoked ? 'archived' : 'active'}>{tok.revoked ? t('adm.evs.st_revoked') : t('adm.evs.st_active')}</Badge>
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {t('adm.evs.created_at', { rel: formatRelativeTime(tok.created_at), date: formatDate(tok.created_at) })}
                    {tok.expires_at && t('adm.evs.expires', { rel: formatRelativeTime(tok.expires_at) })}
                    {tok.last_used_at && t('adm.evs.last_used', { rel: formatRelativeTime(tok.last_used_at) })}
                  </div>
                </div>
                {!tok.revoked && (
                  <Button variant="ghost" size="sm" onClick={() => setPendingRevoke(tok)} aria-label={t('adm.evs.revoke')}>
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </Card>

      {/* Connection samples. */}
      <Card className="p-5">
        <h3 className="mb-3 flex items-center gap-2 text-sm font-semibold">
          <Zap className="h-4 w-4" /> {t('adm.evs.samples')}
        </h3>
        <div className="mb-2 flex gap-1">
          {(Object.keys(SAMPLES) as Array<keyof typeof SAMPLES>).map((k) => (
            <button
              key={k}
              type="button"
              onClick={() => setTab(k)}
              data-testid={`sample-lang-${k}`}
              className={cn(
                'rounded px-2 py-1 font-mono text-[11px] uppercase tracking-wider transition-colors',
                tab === k
                  ? 'bg-foreground text-background'
                  : 'bg-muted text-muted-foreground hover:text-foreground',
              )}
            >
              {k}
            </button>
          ))}
        </div>
        <div className="relative">
          <pre className="max-h-72 overflow-auto rounded-md border border-border bg-background p-3 font-mono text-[11px] leading-relaxed">
{SAMPLES[tab]}
          </pre>
          <Button
            variant="outline"
            size="sm"
            onClick={() => copy(SAMPLES[tab])}
            className="absolute end-2 top-2"
          >
            <Copy className="h-3 w-3" /> {t('adm.evs.copy')}
          </Button>
        </div>
      </Card>

      {/* Live tail. */}
      <LiveTail />

      {/* Event-type reference. */}
      <Card className="p-5">
        <h3 className="mb-3 flex items-center gap-2 text-sm font-semibold">
          <Antenna className="h-4 w-4" /> {t('adm.evs.reference')}
        </h3>
        <ul className="space-y-1.5">
          {EVENT_TYPE_REFERENCE.map((domain) => (
            <li key={domain} className="grid grid-cols-[200px_1fr] gap-3 text-sm">
              <code dir="ltr" className="font-mono text-xs text-foreground">{`dms.${domain}.*`}</code>
              <span className="text-muted-foreground">{t(`adm.evs.ref_${domain}`)}</span>
            </li>
          ))}
        </ul>
      </Card>

      <ConfirmDialog
        open={!!pendingRevoke}
        onOpenChange={(o) => !o && setPendingRevoke(null)}
        title={t('adm.evs.revoke_title')}
        description={
          pendingRevoke
            ? t('adm.evs.revoke_desc', { label: pendingRevoke.label })
            : ''
        }
        confirmLabel={t('adm.evs.revoke')}
        destructive
        loading={revoke.isPending}
        onConfirm={() => pendingRevoke && revoke.mutate(pendingRevoke.id)}
      />
    </div>
  )
}

// LiveTail opens an EventSource against the admin SSE endpoint and
// shows the last 50 events in reverse-chronological order. The
// backend caps each window at 60 s; the UI auto-reconnects unless the
// operator explicitly clicked Stop.
function LiveTail() {
  const { t } = useTranslation('common')
  const [running, setRunning] = useState(false)
  const [events, setEvents] = useState<Array<{ ts: string; subject: string; data: string }>>([])
  const esRef = useRef<EventSource | null>(null)
  const wantRef = useRef(false)

  const start = () => {
    wantRef.current = true
    setRunning(true)
    open()
  }
  const stop = () => {
    wantRef.current = false
    esRef.current?.close()
    esRef.current = null
    setRunning(false)
  }
  const open = () => {
    if (!wantRef.current) return
    // axios baseURL is /api/v1; EventSource doesn't use axios so we
    // build the URL the same way. Cookies are sent because the
    // EventSource defaults to same-origin credentials.
    const es = new EventSource('/api/v1/admin/event-stream/tail', { withCredentials: true })
    esRef.current = es
    es.onmessage = (e) => {
      try {
        const ev = JSON.parse(e.data)
        setEvents((prev) =>
          [{ ts: ev.occurred_at ?? new Date().toISOString(), subject: ev.subject, data: JSON.stringify(ev.data) }, ...prev].slice(0, 50),
        )
      } catch {
        // Ignore parse failures — we never want a malformed event to
        // poison the whole pane.
      }
    }
    es.onerror = () => {
      es.close()
      esRef.current = null
      if (wantRef.current) setTimeout(open, 1000)
    }
  }
  useEffect(() => () => esRef.current?.close(), [])

  return (
    <Card className="p-5">
      <div className="mb-3 flex items-center justify-between">
        <h3 className="flex items-center gap-2 text-sm font-semibold">
          <Antenna className="h-4 w-4" /> {t('adm.evs.live_tail')}
        </h3>
        {running ? (
          <Button variant="outline" size="sm" onClick={stop} data-testid="tail-stop">
            <Square className="h-4 w-4" /> {t('adm.evs.stop')}
          </Button>
        ) : (
          <Button variant="outline" size="sm" onClick={start} data-testid="tail-start">
            <Play className="h-4 w-4" /> {t('adm.evs.start')}
          </Button>
        )}
      </div>
      {events.length === 0 ? (
        <p className="rounded-md border border-dashed border-border bg-muted/30 p-4 text-center text-xs text-muted-foreground">
          {running ? t('adm.evs.listening') : t('adm.evs.tail_hint')}
        </p>
      ) : (
        <ul className="max-h-72 space-y-1 overflow-y-auto rounded-md border border-border bg-background p-2 font-mono text-[11px]">
          {events.map((e, i) => (
            <li key={`${e.ts}-${i}`} className="flex gap-2">
              <span className="shrink-0 text-muted-foreground">{e.ts.slice(11, 19)}</span>
              <span className="shrink-0 text-foreground">{e.subject}</span>
              <span className="truncate text-muted-foreground">{e.data}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}

const SAMPLES = {
  go: `// Go — github.com/nats-io/nats.go
package main

import (
  "log"
  "github.com/nats-io/nats.go"
)

func main() {
  nc, err := nats.Connect("tls://stream.vaultdms.io:4222",
    nats.UserCredentials("./vaultdms.creds"))
  if err != nil { log.Fatal(err) }
  defer nc.Drain()

  // Subjects are scoped to your tenant; the token won't let you
  // sub anywhere else.
  _, err = nc.Subscribe("tenant.<your-tenant>.events.>", func(m *nats.Msg) {
    log.Printf("%s — %s", m.Subject, string(m.Data))
  })
  if err != nil { log.Fatal(err) }
  select {} // block forever
}`,
  python: `# Python — pip install nats-py
import asyncio, nats

async def main():
    nc = await nats.connect(
        "tls://stream.vaultdms.io:4222",
        user_credentials="./vaultdms.creds",
    )
    async def cb(msg):
        print(msg.subject, "->", msg.data.decode())
    await nc.subscribe("tenant.<your-tenant>.events.>", cb=cb)
    await asyncio.Event().wait()  # block forever

asyncio.run(main())`,
  js: `// JavaScript / Node — npm i @nats-io/nats-core nkeys-js
import { connect, credsAuthenticator } from "@nats-io/nats-core"
import fs from "node:fs"

const nc = await connect({
  servers: "tls://stream.vaultdms.io:4222",
  authenticator: credsAuthenticator(fs.readFileSync("./vaultdms.creds")),
})

const sub = nc.subscribe("tenant.<your-tenant>.events.>")
for await (const m of sub) {
  console.log(m.subject, "->", new TextDecoder().decode(m.data))
}`,
  curl: `# Polling fallback — works through firewalls that block long-lived NATS.
curl -s 'https://api.vaultdms.io/api/v1/events?since=2026-05-13T00:00:00Z&limit=100' \\
  -H "Authorization: Bearer $SEDOC_EVENT_TOKEN" | jq

# Resume from the cursor on next call:
curl -s 'https://api.vaultdms.io/api/v1/events?cursor=seq:1234&limit=100' \\
  -H "Authorization: Bearer $SEDOC_EVENT_TOKEN" | jq`,
} as const

// The route is a redirect alias into the canonical
// /admin/integrations?tab=events. Older deep-links and external docs
// still resolve to the same content. EventStreamPage is exported
// above so the canonical page can render it inside its 'events' tab.
import { redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_authenticated/admin/integrations/events')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/integrations', search: { tab: 'events' }, replace: true })
  },
})
