// /admin/integrations/email — ADR 0087 email-ingestion console.
//
// Three panes:
//   1. Configs — create / list / pause / delete + "run now".
//   2. Status dashboard — totals + pending/failed + next-run countdown.
//   3. OAuth nudge — points at /admin/connectors for the Microsoft +
//      Gmail authorisation flow that this page reuses.
import { PasswordInput } from '@/components/ui/PasswordInput'
import { useEffect, useMemo, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { AlertCircle, CheckCircle2, Inbox, Mail, Play, Plus, Server, Settings, Trash2 } from 'lucide-react'
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
import { getWorkspaces, getFolders } from '@/api/workspaces'
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
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

// Canonical source list and order; the label/description come from the
// catalogue as adm.mail.s_<value> / _desc at render time.
const SOURCE_OPTIONS: readonly EmailSource[] = ['microsoft', 'gmail', 'imap']

export function EmailIngestionPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const { data: configs, isLoading } = useQuery({
    queryKey: ['email-configs'],
    queryFn: listEmailConfigs,
    refetchInterval: 30_000, // status dashboard auto-refreshes
  })

  const [creating, setCreating] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<EmailConfig | null>(null)

  const create = useAppMutation({
    mutationFn: createEmailConfig,
    onSuccess: () => {
      toast.success(t('adm.mail.created'))
      setCreating(false)
      qc.invalidateQueries({ queryKey: ['email-configs'] })
    },
    onError: () => toast.error(t('adm.mail.err_create')),
  })

  const run = useAppMutation({
    mutationFn: runEmailConfig,
    onSuccess: (r) => {
      toast.success(t('adm.mail.polled', { count: r.ingested }))
      qc.invalidateQueries({ queryKey: ['email-configs'] })
    },
    onError: (e: { response?: { data?: { error?: string } } }) =>
      toast.error(e?.response?.data?.error ?? t('adm.mail.err_poll')),
  })

  const pause = useAppMutation({
    mutationFn: (c: EmailConfig) => patchEmailConfig(c.id, { active: !c.active }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['email-configs'] }),
  })

  const remove = useAppMutation({
    mutationFn: (id: string) => deleteEmailConfig(id),
    onSuccess: () => {
      toast.success(t('adm.mail.disabled'))
      setPendingDelete(null)
      qc.invalidateQueries({ queryKey: ['email-configs'] })
    },
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('adm.mail.title')}
        description={t('adm.mail.description')}
        actions={
          <Button onClick={() => setCreating(true)} data-testid="email-config-new">
            <Plus className="h-4 w-4" /> {t('adm.mail.new')}
          </Button>
        }
      />

      {/* OAuth nudge — Microsoft / Gmail sources need an already-authorised
          connector row. */}
      <Card className="flex items-start gap-3 border-info/40 bg-info/5 p-3 text-xs">
        <Mail className="mt-0.5 h-4 w-4 shrink-0 text-info" />
        <span>
          {/* <Trans>: the link sits mid-sentence and Arabic reorders the
              clauses around it. */}
          <Trans
            i18nKey="adm.mail.oauth_nudge"
            ns="common"
            components={[
              <Link key="l" to="/admin/connectors" className="font-medium text-foreground underline-offset-4 hover:underline" />,
            ]}
          />
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
          title={t('adm.mail.empty')}
          description={t('adm.mail.empty_desc')}
          actionLabel={t('adm.mail.empty_action')}
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
        title={t('adm.mail.disable_title')}
        description={
          pendingDelete
            ? t('adm.mail.disable_desc', { label: pendingDelete.label })
            : ''
        }
        confirmLabel={t('adm.mail.disable')}
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
  const { t } = useTranslation('common')
  const [source, setSource] = useState<EmailSource>('microsoft')
  const [label, setLabel] = useState('')
  const [imapHost, setImapHost] = useState('')
  const [imapPort, setImapPort] = useState<number>(993)
  const [imapUser, setImapUser] = useState('')
  const [imapPwd, setImapPwd] = useState('')
  // Real workspace picker (was a raw-UUID text box). 'none' sentinel:
  // Radix Select rejects empty-string values.
  const [workspace, setWorkspace] = useState('none')
  const [folder, setFolder] = useState('none')
  const [interval, setInterval] = useState(300)
  const wsQ = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces, staleTime: 60_000 })
  // Destination is REQUIRED by the ingest pipeline (it files each mail
  // body + attachment as a real document, which needs a home). The form
  // used to call both fields "optional" and take a raw folder UUID, so
  // configs were happily created that then failed on EVERY message with
  // "target workspace + folder required" — mail was fetched, nothing was
  // filed, and the only clue was a `last error` chip.
  const foldersQ = useQuery({
    queryKey: ['folders', workspace],
    queryFn: () => getFolders(workspace),
    enabled: workspace !== 'none',
    staleTime: 60_000,
  })

  const sourceDesc = t(`adm.mail.s_${source}_desc`)

  const submit = () => {
    if (!label.trim()) { toast.error(t('adm.mail.err_label')); return }
    if (source === 'imap' && (!imapHost.trim() || !imapUser.trim() || !imapPwd)) {
      toast.error(t('adm.mail.err_imap'))
      return
    }
    if (workspace === 'none' || folder === 'none') {
      toast.error(t('adm.mail.err_target'))
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
      target_workspace_id: workspace,
      target_folder_id:    folder,
      poll_interval_seconds: interval,
    })
  }

  return (
    <Card className="space-y-3 p-5">
      <h3 className="flex items-center gap-2 text-sm font-semibold">
        <Server className="h-4 w-4" /> {t('adm.mail.form_title')}
      </h3>
      <Select
        label={t('adm.mail.source')}
        value={source}
        onValueChange={(v) => setSource(v as EmailSource)}
        options={SOURCE_OPTIONS.map((v) => ({ value: v, label: t(`adm.mail.s_${v}`) }))}
      />
      {sourceDesc && <p className="text-xs text-muted-foreground">{sourceDesc}</p>}
      <Input
        label={t('adm.mail.label')}
        placeholder={t('adm.mail.label_ph')}
        value={label}
        onChange={(e) => setLabel(e.target.value)}
        data-testid="email-label"
      />
      {source === 'imap' && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Input label={t('adm.mail.imap_host')} dir="ltr" placeholder={t('adm.mail.imap_host_ph')} value={imapHost} onChange={(e) => setImapHost(e.target.value)} data-testid="email-imap-host" />
          <Input label={t('adm.mail.port')} type="number" value={imapPort} onChange={(e) => setImapPort(Number(e.target.value))} />
          <Input label={t('adm.mail.username')} dir="ltr" placeholder={t('adm.mail.username_ph')} value={imapUser} onChange={(e) => setImapUser(e.target.value)} data-testid="email-imap-user" />
          <PasswordInput label={t('adm.mail.password')} autoComplete="off" value={imapPwd} onChange={(e) => setImapPwd(e.target.value)} data-testid="email-imap-pwd" />
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-3">
        <Select
          label={t('adm.mail.into_workspace')}
          value={workspace}
          onValueChange={(v) => { setWorkspace(v); setFolder('none') }}
          options={[
            { value: 'none', label: t('adm.mail.pick_workspace') },
            ...(wsQ.data ?? []).map((w) => ({ value: w.id, label: w.name })),
          ]}
        />
        <Select
          label={t('adm.mail.into_folder')}
          value={folder}
          onValueChange={setFolder}
          disabled={workspace === 'none'}
          options={[
            {
              value: 'none',
              label:
                workspace === 'none'
                  ? t('adm.mail.ws_first')
                  : foldersQ.isLoading
                    ? t('adm.mail.loading_folders')
                    : (foldersQ.data ?? []).length === 0
                      ? t('adm.mail.no_folders')
                      : t('adm.mail.pick_folder'),
            },
            ...(foldersQ.data ?? []).map((f) => ({ value: f.id, label: f.name })),
          ]}
        />
        <Input
          label={t('adm.mail.poll_interval')}
          type="number"
          min={60}
          value={interval}
          onChange={(e) => setInterval(Math.max(60, Number(e.target.value)))}
        />
      </div>
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onCancel}>{t('adm.mail.cancel')}</Button>
        <Button onClick={submit} loading={pending} disabled={pending} data-testid="email-create">{t('adm.mail.create')}</Button>
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
  const { t } = useTranslation('common')
  const [expanded, setExpanded] = useState(false)
  return (
    <li>
      <Card className="overflow-hidden p-0">
        <div className="flex items-start justify-between gap-3 p-3">
          <button type="button" onClick={() => setExpanded((e) => !e)} className="flex min-w-0 flex-1 items-start gap-2 text-start">
            <DirectionalIcon name="ChevronRight" className={cn('mt-1 h-4 w-4 shrink-0 text-muted-foreground transition-transform', expanded && 'rotate-90')} />
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant={cfg.active ? 'active' : 'archived'}>{cfg.active ? t('adm.mail.st_active') : t('adm.mail.st_paused')}</Badge>
                <span className="truncate text-sm font-medium">{cfg.label}</span>
                <code dir="ltr" className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]">{cfg.source}</code>
                {cfg.last_error && (
                  <span className="inline-flex items-center gap-1 text-xs text-destructive">
                    <AlertCircle className="h-3.5 w-3.5" /> {t('adm.mail.last_error_chip')}
                  </span>
                )}
                {!cfg.last_error && cfg.last_success_at && (
                  <span className="inline-flex items-center gap-1 text-xs text-success">
                    <CheckCircle2 className="h-3.5 w-3.5" /> {t('adm.mail.last_ok', { rel: formatRelativeTime(cfg.last_success_at) })}
                  </span>
                )}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                {t('adm.mail.ingested', { count: cfg.messages_ingested, seconds: cfg.poll_interval_seconds })}
                {cfg.imap_host && <span dir="ltr"> · {cfg.imap_username}@{cfg.imap_host}:{cfg.imap_port}</span>}
              </div>
            </div>
          </button>
          <div className="flex shrink-0 gap-1">
            <Button variant="outline" size="sm" onClick={onRun} disabled={running} data-testid={`email-run-${cfg.id}`}>
              <Play className={cn('h-4 w-4', running && 'animate-pulse')} /> {t('adm.mail.run_now')}
            </Button>
            <Button variant="outline" size="sm" onClick={onTogglePause}>
              <Settings className="h-4 w-4" /> {cfg.active ? t('adm.mail.pause') : t('adm.mail.resume')}
            </Button>
            <Button variant="ghost" size="sm" onClick={onDelete} aria-label={t('adm.mail.disable')}>
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
  const { t } = useTranslation('common')
  const { data, isLoading } = useQuery({
    queryKey: ['email-config-stats', configId],
    queryFn: () => getEmailConfigStats(configId),
    refetchInterval: 15_000,
  })
  const nextIn = useMemo(() => {
    if (!data?.next_run_at) return null
    const ms = new Date(data.next_run_at).getTime() - Date.now()
    if (ms <= 0) return t('adm.mail.now')
    return t('adm.mail.in_seconds', { count: Math.ceil(ms / 1000) })
    // `t` in the deps: the memo caches TRANSLATED text and the locale
    // resolves after first render.
  }, [data?.next_run_at, t])

  if (isLoading || !data) {
    return <div className="border-t border-border bg-muted/30 p-4"><Skeleton className="h-16" /></div>
  }

  return (
    <div className="grid gap-3 border-t border-border bg-muted/30 p-4 sm:grid-cols-4">
      <Stat label={t('adm.mail.total_ingested')} value={data.messages_total.toLocaleString()} />
      <Stat label={t('adm.mail.pending')} value={data.messages_pending.toLocaleString()} accent={data.messages_pending > 0 ? 'warn' : undefined} />
      <Stat label={t('adm.mail.failed')} value={data.messages_failed.toLocaleString()} accent={data.messages_failed > 0 ? 'error' : undefined} />
      <Stat label={t('adm.mail.next_run')} value={nextIn ?? '—'} />
      {lastError && (
        <div className="sm:col-span-4 rounded-md border border-destructive/40 bg-destructive/5 p-2 text-xs text-foreground" data-testid={`email-error-${configId}`}>
          <strong>{t('adm.mail.last_error')}</strong> {lastError}
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
        accent === 'warn'  && 'text-warning-strong',
        accent === 'error' && 'text-destructive')}>
        {value}
      </div>
    </div>
  )
}

// The route is a redirect alias into the canonical
// /admin/integrations?tab=email. EmailIngestionPage is exported above
// so the canonical page can render it inside its 'email' tab.
import { redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_authenticated/admin/integrations/email')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/integrations', search: { tab: 'email' }, replace: true })
  },
})

// Silence unused-import warnings for utility imports referenced in
// JSX-only branches.
export const _unused = { useEffect }
