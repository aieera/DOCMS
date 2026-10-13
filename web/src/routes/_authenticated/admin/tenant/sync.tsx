import { Fragment, useMemo, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Laptop,
  Plus,
  Trash2,
  ChevronDown,
  ChevronRight,
  FolderSync,
  CheckCircle2,
  CircleSlash,
} from 'lucide-react'
import { toast } from 'sonner'

import { useAppMutation } from '@/hooks/useAppMutation'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect } from '@/components/ui/shadcn/select'
import { Card } from '@/components/ui/card'
import { Spinner } from '@/components/ui/Spinner'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { formatDateTime } from '@/lib/formatters'
import {
  listSyncDevices,
  registerSyncDevice,
  setSyncDeviceFolders,
  revokeSyncDevice,
  SYNC_PLATFORMS,
  type SyncDevice,
  type SyncPlatform,
} from '@/api/sync'
import { getWorkspaces, getFolders } from '@/api/workspaces'

// /admin/tenant/sync — "Devices & Sync". Manage the headless
// selective-sync clients that mirror a chosen set of folders to a
// device. This is the current sync surface; the native desktop drive/
// tray client is a separate, deferred effort (see the explainer).

export const Route = createFileRoute('/_authenticated/admin/tenant/sync')({
  component: SyncDevicesPage,
})

const SYNC_KEY = ['admin', 'sync', 'devices'] as const


function SyncDevicesPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const devicesQ = useQuery({ queryKey: SYNC_KEY, queryFn: listSyncDevices })

  const invalidate = () => qc.invalidateQueries({ queryKey: SYNC_KEY })

  return (
    <div className="space-y-6">
      <PageHeader
        noMargin
        title={t('adm.sync.title')}
        description={t('adm.sync.description')}
      />

      <ExplainerBanner />

      <RegisterDeviceCard onDone={invalidate} />

      {devicesQ.isLoading ? (
        <Spinner />
      ) : (
        <DevicesTable devices={devicesQ.data ?? []} onDone={invalidate} />
      )}
    </div>
  )
}

function ExplainerBanner() {
  const { t } = useTranslation('common')
  return (
    <section className="rounded-lg border border-primary/40 bg-primary/5 p-4 text-sm">
      <div className="flex items-start gap-3">
        <FolderSync className="mt-0.5 h-5 w-5 shrink-0 text-primary" />
        {/* Full-width banner, capped prose: uncapped, this paragraph runs
            ~200 characters to the line and the eye loses the return. */}
        <div className="max-w-[85ch] text-muted-foreground">
          <p className="font-semibold text-foreground">{t('adm.sync.explainer_title')}</p>
          <p className="mt-1">
            <Trans i18nKey="adm.sync.explainer" ns="common" components={{ 1: <strong /> }} />
          </p>
        </div>
      </div>
    </section>
  )
}

function RegisterDeviceCard({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation('common')
  const [name, setName] = useState('')
  const [platform, setPlatform] = useState<SyncPlatform>('linux')
  const [workspaceId, setWorkspaceId] = useState('')

  const wsQ = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })
  const workspaceOpts = useMemo(
    () => [
      { value: '__all__', label: t('adm.sync.all_workspaces') },
      ...(wsQ.data ?? []).map((w) => ({ value: w.id, label: w.name })),
    ],
    // `t` belongs in the deps: the memo caches a TRANSLATED label, and
    // the locale resolves after first render (it arrives with /auth/me),
    // so without it the list keeps whatever language was active when the
    // component first mounted. Same bug the breadcrumbs had.
    [wsQ.data, t],
  )

  const create = useAppMutation({
    mutationFn: () =>
      registerSyncDevice({
        name: name.trim(),
        platform,
        workspace_id: workspaceId === '' || workspaceId === '__all__' ? undefined : workspaceId,
      }),
    onSuccess: (device: SyncDevice) => {
      toast.success(`Device "${device.name}" registered`)
      setName('')
      setWorkspaceId('')
      onDone()
    },
    defaultErrorMessage: t('adm.sync.register_failed'),
  })

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Plus className="h-4 w-4" /> {t('adm.sync.register')}
      </div>
      <div className="grid items-end gap-4 sm:grid-cols-2 lg:grid-cols-[repeat(3,minmax(0,1fr))_auto]">
        <div className="space-y-1.5">
          <label className="text-sm font-medium" htmlFor="sync-device-name">
            {t('adm.sync.name')}
          </label>
          <Input
            id="sync-device-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('adm.sync.name_placeholder')}
          />
        </div>
        <LabeledSelect
          label={t('adm.sync.platform')}
          value={platform}
          onValueChange={(v) => setPlatform(v as SyncPlatform)}
          options={SYNC_PLATFORMS.map((p) => ({ value: p, label: t(`adm.sync.p_${p}`) }))}
        />
        <LabeledSelect
          label={t('adm.sync.workspace')}
          value={workspaceId || '__all__'}
          onValueChange={(v) => setWorkspaceId(v)}
          options={workspaceOpts}
          placeholder={wsQ.isLoading ? t('adm.sync.loading') : t('adm.sync.all_workspaces')}
        />
        <Button
          className="sm:col-span-2 sm:justify-self-start lg:col-span-1"
          onClick={() => create.mutate()}
          disabled={create.isPending || name.trim() === ''}
          loading={create.isPending}
        >
          {t('adm.sync.register')}
        </Button>
      </div>
    </Card>
  )
}

function DevicesTable({ devices, onDone }: { devices: SyncDevice[]; onDone: () => void }) {
  const { t } = useTranslation('common')
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [revokeId, setRevokeId] = useState<string | null>(null)

  const revoke = useAppMutation({
    mutationFn: (id: string) => revokeSyncDevice(id),
    onSuccess: () => {
      toast.success(t('adm.sync.revoked'))
      setRevokeId(null)
      onDone()
    },
    defaultErrorMessage: t('adm.sync.revoke_failed'),
  })

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Laptop className="h-4 w-4" /> {t('adm.sync.devices')}
      </div>
      {devices.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t('adm.sync.no_devices')}
        </p>
      ) : (
        <div className="overflow-hidden rounded-lg bg-muted shadow-neu-inset">
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            {/* text-start belongs on the th: the UA stylesheet sets
                `text-align: center` directly on th, which beats a value
                inherited from thead. */}
            <thead className="bg-muted/50 text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-3 py-2" />
                <th className="px-3 py-2 text-start">{t('adm.sync.name')}</th>
                <th className="px-3 py-2 text-start">{t('adm.sync.col_platform')}</th>
                <th className="px-3 py-2 text-start">{t('adm.sync.col_last_seen')}</th>
                <th className="px-3 py-2 text-start">{t('adm.sync.col_status')}</th>
                <th className="px-3 py-2 text-start">{t('adm.sync.col_folders')}</th>
                <th className="px-3 py-2 text-start">{t('adm.sync.col_position')}</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {devices.map((d) => {
                const expanded = expandedId === d.id
                return (
                  <Fragment key={d.id}>
                    <tr>
                      <td className="px-3 py-2">
                        <button
                          type="button"
                          className="text-muted-foreground hover:text-foreground"
                          aria-label={expanded ? t('adm.sync.collapse') : t('adm.sync.expand')}
                          onClick={() => setExpandedId(expanded ? null : d.id)}
                        >
                          {expanded ? (
                            <ChevronDown className="h-4 w-4" />
                          ) : (
                            <ChevronRight className="h-4 w-4" />
                          )}
                        </button>
                      </td>
                      <td className="px-3 py-2 font-medium">{d.name}</td>
                      <td className="px-3 py-2">{d.platform ? t(`adm.sync.p_${d.platform}`, { defaultValue: d.platform }) : '—'}</td>
                      <td dir="auto" className="px-3 py-2 text-muted-foreground">
                        {d.last_seen_at ? formatDateTime(d.last_seen_at) : t('adm.sync.never')}
                      </td>
                      <td className="px-3 py-2">
                        <StatusBadge revoked={d.revoked} />
                      </td>
                      <td className="px-3 py-2 tabular-nums">{d.selective_folders.length}</td>
                      <td className="px-3 py-2">
                        <CursorIndicator cursor={d.cursor} />
                      </td>
                      <td className="px-3 py-2 text-end">
                        <Button
                          variant="ghost"
                          size="sm"
                          className="gap-1 text-destructive hover:text-destructive/80"
                          onClick={() => setRevokeId(d.id)}
                          disabled={d.revoked}
                        >
                          <Trash2 className="h-3.5 w-3.5" /> {t('adm.sync.revoke')}
                        </Button>
                      </td>
                    </tr>
                    {expanded && (
                      <tr>
                        <td colSpan={8} className="bg-muted/20 px-3 py-4">
                          <FolderPicker device={d} onDone={onDone} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                )
              })}
            </tbody>
          </table>
          </div>
        </div>
      )}

      <ConfirmDialog
        open={!!revokeId}
        onOpenChange={(o) => !o && setRevokeId(null)}
        title={t('adm.sync.revoke_title')}
        description={t('adm.sync.revoke_desc')}
        confirmLabel={t('adm.sync.revoke')}
        destructive
        loading={revoke.isPending}
        onConfirm={() => revokeId && revoke.mutate(revokeId)}
      />
    </Card>
  )
}

function StatusBadge({ revoked }: { revoked: boolean }) {
  const { t } = useTranslation('common')
  if (revoked) {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-destructive/10 px-2 py-0.5 text-xs font-medium text-foreground">
        <CircleSlash className="h-3 w-3" /> {t('adm.sync.status_revoked')}
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs font-medium text-foreground">
      <CheckCircle2 className="h-3 w-3" /> {t('adm.sync.status_active')}
    </span>
  )
}

function CursorIndicator({ cursor }: { cursor: string }) {
  const { t } = useTranslation('common')
  if (!cursor) {
    return <span className="text-xs text-muted-foreground">{t('adm.sync.not_synced')}</span>
  }
  // The cursor is opaque; show a short prefix as a stable last-position
  // marker rather than the full (potentially long) token.
  const short = cursor.length > 12 ? `${cursor.slice(0, 12)}…` : cursor
  return (
    <span className="font-mono text-xs text-muted-foreground" title={cursor}>
      {short}
    </span>
  )
}

// FolderPicker — per-device selective-sync editor. Lists folders from
// the workspace/folder API and lets the user pick which ones the device
// mirrors, then saves via setSyncDeviceFolders.
function FolderPicker({ device, onDone }: { device: SyncDevice; onDone: () => void }) {
  const { t } = useTranslation('common')
  const wsQ = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })
  // If the device is pinned to a workspace, only that one is relevant.
  const workspaces = useMemo(() => {
    const all = wsQ.data ?? []
    return device.workspace_id ? all.filter((w) => w.id === device.workspace_id) : all
  }, [wsQ.data, device.workspace_id])

  const foldersQ = useQuery({
    queryKey: ['admin', 'sync', 'folders', workspaces.map((w) => w.id).join(',')],
    queryFn: async () => {
      const lists = await Promise.all(
        workspaces.map(async (w) => {
          const folders = await getFolders(w.id)
          return folders.map((f) => ({ workspaceName: w.name, folder: f }))
        }),
      )
      return lists.flat()
    },
    enabled: workspaces.length > 0,
  })

  const [selected, setSelected] = useState<Set<string>>(() => new Set(device.selective_folders))

  const toggle = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const save = useAppMutation({
    mutationFn: () => setSyncDeviceFolders(device.id, [...selected]),
    onSuccess: () => {
      toast.success(t('adm.sync.folders_saved'))
      onDone()
    },
    defaultErrorMessage: t('adm.sync.save_failed'),
  })

  const rows = foldersQ.data ?? []
  // Folder IDs that are selected but no longer resolve to a listed
  // folder (e.g. deleted, or outside the pinned workspace). Surface
  // them so the count/state stays honest and the user can drop them.
  const orphanIds = useMemo(() => {
    const known = new Set(rows.map((r) => r.folder.id))
    return [...selected].filter((id) => !known.has(id))
  }, [rows, selected])

  return (
    <div>
      <div className="mb-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        {t('adm.sync.picker_title', { device: device.name })}
      </div>
      {wsQ.isLoading || foldersQ.isLoading ? (
        <Spinner />
      ) : rows.length === 0 && orphanIds.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t('adm.sync.no_folders', {
            scope: device.workspace_id ? t('adm.sync.scope_pinned') : '',
          })}
        </p>
      ) : (
        <div className="max-h-64 overflow-y-auto rounded-md bg-muted shadow-neu-inset">
          <ul className="divide-y divide-border">
            {rows.map(({ workspaceName, folder }) => (
              <li key={folder.id}>
                <label className="flex cursor-pointer items-start gap-2 px-3 py-1.5 text-sm hover:bg-accent/40">
                  <input
                    type="checkbox"
                    className="mt-1 shrink-0"
                    checked={selected.has(folder.id)}
                    onChange={() => toggle(folder.id)}
                  />
                  {/* One text block, not two flex items: as siblings in the
                      row each run wrapped as its own box instead of
                      reflowing together as a sentence. */}
                  <span className="min-w-0">
                    <span className="font-medium">{folder.name}</span>{' '}
                    <span className="text-xs text-muted-foreground">
                      {workspaceName}
                      {folder.path ? ` · ${folder.path}` : ''}
                    </span>
                  </span>
                </label>
              </li>
            ))}
            {orphanIds.map((id) => (
              <li key={id}>
                <label className="flex cursor-pointer items-start gap-2 px-3 py-1.5 text-sm hover:bg-accent/40">
                  <input type="checkbox" className="mt-1 shrink-0" checked onChange={() => toggle(id)} />
                  <span className="min-w-0 break-all">
                    <span className="font-mono text-xs">{id}</span>{' '}
                    <span className="text-xs text-warning-strong">{t('adm.sync.unresolved')}</span>
                  </span>
                </label>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="mt-3 flex items-center gap-3">
        <Button
          size="sm"
          onClick={() => save.mutate()}
          disabled={save.isPending || device.revoked}
          loading={save.isPending}
        >
          {t('adm.sync.save_folders')}
        </Button>
        <span className="text-xs text-muted-foreground">{t('adm.sync.selected_count', { count: selected.size })}</span>
      </div>
    </div>
  )
}
