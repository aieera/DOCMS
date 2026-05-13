// /admin/integrations/email — ADR 0087 email-ingestion console.
//
// Three panes:
//   1. Configs — create / list / pause / delete + "run now".
//   2. Status dashboard — totals + pending/failed + next-run countdown.
//   3. OAuth nudge — points at /admin/connectors for the Microsoft +
//      Gmail authorisation flow that this page reuses.
import { useEffect, useMemo, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  AlertCircle, CheckCircle2, ChevronRight, Inbox, Mail, Play, Plus, Server, Settings, Trash2,
} from 'lucide-react'

import {
  createEmailConfig,
  deleteEmailConfig,
  getEmailConfigStats,
  listEmailConfigs,
  patchEmailConfig,
  runEmailConfig,
  type EmailConfig,
  type EmailSource,
} from '@/api/email-ingestion'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Skeleton } from '@/components/ui/Skeleton'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { formatRelativeTime } from '@/lib/formatters'
import { cn } from '@/lib/cn'

const SOURCE_OPTIONS: Array<{ value: EmailSource; label: string; desc: string }> = [
  { value: 'microsoft', label: 'Microsoft 365 (Graph)', desc: 'Exchange Online via the Graph API. OAuth required.' },
  { value: 'gmail',     label: 'Gmail (Google Workspace)', desc: 'OAuth required.' },
  { value: 'imap',      label: 'IMAP (everything else)',   desc: 'Legacy Exchange, ProtonMail bridge, Fastmail, etc.' },
]

function EmailIngestionPage() {
  const qc = useQueryClient()
  const { data: configs, isLoading } = useQuery({
    queryKey: ['email-configs'],
    queryFn: listEmailConfigs,
    refetchInterval: 30_000, // status dashboard auto-refreshes
  })

  const [creating, setCreating] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<EmailConfig | null>(null)

  const create = useMutation({
    mutationFn: createEmailConfig,
    onSuccess: () => {
      toast.success('Config created')
      setCreating(false)
      qc.invalidateQueries({ queryKey: ['email-configs'] })
    },
    onError: () => toast.error('Create failed'),
  })

  const run = useMutation({
    mutationFn: runEmailConfig,
    onSuccess: (r) => {
      toast.success(`Polled — ${r.ingested} new message${r.ingested === 1 ? '' : 's'}`)
      qc.invalidateQueries({ queryKey: ['email-configs'] })
    },
    onError: (e: { response?: { data?: { error?: string } } }) =>
      toast.error(e?.response?.data?.error ?? 'Poll failed'),
  })

  const pause = useMutation({
    mutationFn: (c: EmailConfig) => patchEmailConfig(c.id, { active: !c.active }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['email-configs'] }),
  })

  const remove = useMutation({
    mutationFn: (id: string) => deleteEmailConfig(id),
    onSuccess: () => {
      toast.success('Config disabled')
      setPendingDelete(null)
      qc.invalidateQueries({ queryKey: ['email-configs'] })
    },
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title="Email ingestion"
        description="Pull email into VaultDMS so every message + attachment is filed as a document. Polling cadence configurable per source; ADR 0087."
        actions={
          <Button onClick={() => setCreating(true)} data-testid="email-config-new">
            <Plus className="h-4 w-4" /> New config
          </Button>
        }
      />

      {/* OAuth nudge — Microsoft / Gmail sources need an already-authorised
          connector row. */}
      <Card className="flex items-start gap-3 border-info/40 bg-info/5 p-3 text-xs">
        <Mail className="mt-0.5 h-4 w-4 shrink-0 text-info" />
        <span>
          Microsoft and Gmail sources reuse the OAuth grant stored in{' '}
          <Link to="/admin/connectors" className="font-medium text-foreground underline-offset-4 hover:underline">
            Admin → Connectors
          </Link>
          . Authorise the provider there first, then come back here to map the inbox to a folder.
        </span>
      </Card>

      {creating && (
        <CreateForm
          onCancel={() => setCreating(false)}
          onSubmit={(input) => create.mutate(input)}
          pending={create.isPending}
        />
      )}

      {isLoading ? (
        <Skeleton className="h-32" />
      ) : !configs || configs.length === 0 ? (
        <EmptyState
          icon={<Inbox className="h-6 w-6" />}
          title="No email configs yet"
          description="Create a config to start ingesting Microsoft 365, Gmail, or IMAP mail."
          actionLabel="Create your first config"
          onAction={() => setCreating(true)}
        />
      ) : (
        <ul className="space-y-2">
          {configs.map((c) => (
            <ConfigRow
              key={c.id}
              cfg={c}
              onRun={() => run.mutate(c.id)}
              onTogglePause={() => pause.mutate(c)}
              onDelete={() => setPendingDelete(c)}
              running={run.isPending && run.variables === c.id}
            />
          ))}
        </ul>
      )}

      <ConfirmDialog
        open={!!pendingDelete}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title="Disable config?"
        description={
          pendingDelete
            ? `"${pendingDelete.label}" will stop polling immediately. Previously ingested messages stay where they are — only future polling is paused.`
            : ''
        }
        confirmLabel="Disable"
        destructive
        loading={remove.isPending}
        onConfirm={() => pendingDelete && remove.mutate(pendingDelete.id)}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------

function CreateForm({
  onCancel,
  onSubmit,
  pending,
}: {
  onCancel: () => void
  onSubmit: (input: any) => void
  pending: boolean
}) {
  const [source, setSource] = useState<EmailSource>('microsoft')
  const [label, setLabel] = useState('')
  const [imapHost, setImapHost] = useState('')
  const [imapPort, setImapPort] = useState<number>(993)
  const [imapUser, setImapUser] = useState('')
  const [imapPwd, setImapPwd] = useState('')
  const [workspace, setWorkspace] = useState('')
  const [folder, setFolder] = useState('')
  const [interval, setInterval] = useState(300)

  const sourceDesc = SOURCE_OPTIONS.find((o) => o.value === source)?.desc

  const submit = () => {
    if (!label.trim()) { toast.error('Label is required'); return }
    if (source === 'imap' && (!imapHost.trim() || !imapUser.trim() || !imapPwd)) {
      toast.error('IMAP host, username, and password are required')
      return
    }
    const oauthProvider =
      source === 'microsoft' ? 'microsoft365' : source === 'gmail' ? 'google_workspace' : undefined
    onSubmit({
      source, label: label.trim(),
      oauth_provider: oauthProvider,
      imap_host:    source === 'imap' ? imapHost.trim() : undefined,
      imap_port:    source === 'imap' ? imapPort       : undefined,
      imap_use_tls: source === 'imap' ? true           : undefined,
      imap_username: source === 'imap' ? imapUser.trim() : undefined,
      imap_password: source === 'imap' ? imapPwd       : undefined,
      target_workspace_id: workspace || undefined,
      target_folder_id:    folder    || undefined,
      poll_interval_seconds: interval,
    })
  }

  return (
    <Card className="space-y-3 p-5">
      <h3 className="flex items-center gap-2 text-sm font-semibold">
        <Server className="h-4 w-4" /> New email config
      </h3>
      <Select
        label="Source"
        value={source}
        onValueChange={(v) => setSource(v as EmailSource)}
        options={SOURCE_OPTIONS.map((o) => ({ value: o.value, label: o.label }))}
      />
      {sourceDesc && <p className="text-xs text-muted-foreground">{sourceDesc}</p>}
      <Input
        label="Label"
        placeholder="e.g. 'support@ inbox', 'invoices'"
        value={label}
        onChange={(e) => setLabel(e.target.value)}
        data-testid="email-label"
      />
      {source === 'imap' && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Input label="IMAP host" placeholder="imap.example.com" value={imapHost} onChange={(e) => setImapHost(e.target.value)} data-testid="email-imap-host" />
          <Input label="Port" type="number" value={imapPort} onChange={(e) => setImapPort(Number(e.target.value))} />
          <Input label="Username" placeholder="user@example.com" value={imapUser} onChange={(e) => setImapUser(e.target.value)} data-testid="email-imap-user" />
          <Input label="Password" type="password" autoComplete="off" value={imapPwd} onChange={(e) => setImapPwd(e.target.value)} data-testid="email-imap-pwd" />
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-3">
        <Input label="Target workspace (UUID, optional)" value={workspace} onChange={(e) => setWorkspace(e.target.value)} />
        <Input label="Target folder (UUID, optional)" value={folder} onChange={(e) => setFolder(e.target.value)} />
        <Input
          label="Poll interval (seconds, min 60)"
          type="number"
          min={60}
          value={interval}
          onChange={(e) => setInterval(Math.max(60, Number(e.target.value)))}
        />
      </div>
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onCancel}>Cancel</Button>
        <Button onClick={submit} loading={pending} disabled={pending} data-testid="email-create">Create</Button>
      </div>
    </Card>
  )
}

// ---------------------------------------------------------------------------

function ConfigRow({
  cfg, onRun, onTogglePause, onDelete, running,
}: {
  cfg: EmailConfig
  onRun: () => void
  onTogglePause: () => void
  onDelete: () => void
  running: boolean
}) {
  const [expanded, setExpanded] = useState(false)
  return (
    <li>
      <Card className="overflow-hidden p-0">
        <div className="flex items-start justify-between gap-3 p-3">
          <button type="button" onClick={() => setExpanded((e) => !e)} className="flex min-w-0 flex-1 items-start gap-2 text-left">
            <ChevronRight className={cn('mt-1 h-4 w-4 shrink-0 text-muted-foreground transition-transform', expanded && 'rotate-90')} />
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant={cfg.active ? 'active' : 'archived'}>{cfg.active ? 'Active' : 'Paused'}</Badge>
                <span className="truncate text-sm font-medium">{cfg.label}</span>
                <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]">{cfg.source}</code>
                {cfg.last_error && (
                  <span className="inline-flex items-center gap-1 text-xs text-destructive">
                    <AlertCircle className="h-3.5 w-3.5" /> last error
                  </span>
                )}
                {!cfg.last_error && cfg.last_success_at && (
                  <span className="inline-flex items-center gap-1 text-xs text-success">
                    <CheckCircle2 className="h-3.5 w-3.5" /> last ok {formatRelativeTime(cfg.last_success_at)}
                  </span>
                )}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                {cfg.messages_ingested.toLocaleString()} msg ingested · every {cfg.poll_interval_seconds}s
                {cfg.imap_host && <> · {cfg.imap_username}@{cfg.imap_host}:{cfg.imap_port}</>}
              </div>
            </div>
          </button>
          <div className="flex shrink-0 gap-1">
            <Button variant="outline" size="sm" onClick={onRun} disabled={running} data-testid={`email-run-${cfg.id}`}>
              <Play className={cn('h-4 w-4', running && 'animate-pulse')} /> Run now
            </Button>
            <Button variant="outline" size="sm" onClick={onTogglePause}>
              <Settings className="h-4 w-4" /> {cfg.active ? 'Pause' : 'Resume'}
            </Button>
            <Button variant="ghost" size="sm" onClick={onDelete} aria-label="Disable">
              <Trash2 className="h-4 w-4 text-destructive" />
            </Button>
          </div>
        </div>
        {expanded && <StatusPane configId={cfg.id} lastError={cfg.last_error} />}
      </Card>
    </li>
  )
}

function StatusPane({ configId, lastError }: { configId: string; lastError?: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ['email-config-stats', configId],
    queryFn: () => getEmailConfigStats(configId),
    refetchInterval: 15_000,
  })
  const nextIn = useMemo(() => {
    if (!data?.next_run_at) return null
    const ms = new Date(data.next_run_at).getTime() - Date.now()
    if (ms <= 0) return 'now'
    return `${Math.ceil(ms / 1000)}s`
  }, [data?.next_run_at])

  if (isLoading || !data) {
    return <div className="border-t border-border bg-muted/30 p-4"><Skeleton className="h-16" /></div>
  }

  return (
    <div className="grid gap-3 border-t border-border bg-muted/30 p-4 sm:grid-cols-4">
      <Stat label="Total ingested" value={data.messages_total.toLocaleString()} />
      <Stat label="Pending" value={data.messages_pending.toLocaleString()} accent={data.messages_pending > 0 ? 'warn' : undefined} />
      <Stat label="Failed" value={data.messages_failed.toLocaleString()} accent={data.messages_failed > 0 ? 'error' : undefined} />
      <Stat label="Next run" value={nextIn ?? '—'} />
      {lastError && (
        <div className="sm:col-span-4 rounded-md border border-destructive/40 bg-destructive/5 p-2 text-xs text-destructive" data-testid={`email-error-${configId}`}>
          <strong>Last error:</strong> {lastError}
        </div>
      )}
    </div>
  )
}

function Stat({ label, value, accent }: { label: string; value: string; accent?: 'warn' | 'error' }) {
  return (
    <div className="rounded-md border border-border bg-card p-2">
      <div className="text-[11px] uppercase tracking-wider text-muted-foreground">{label}</div>
      <div className={cn('mt-0.5 text-lg font-semibold tabular-nums',
        accent === 'warn'  && 'text-warning',
        accent === 'error' && 'text-destructive')}>
        {value}
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/integrations/email')({
  component: EmailIngestionPage,
})

// Silence unused-import warnings for utility imports referenced in
// JSX-only branches.
export const _unused = { useEffect }
