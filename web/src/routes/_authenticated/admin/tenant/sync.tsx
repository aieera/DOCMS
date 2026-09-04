import { Fragment, useMemo, useState } from 'react'
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

const PLATFORM_OPTS = SYNC_PLATFORMS.map((p) => ({ value: p, label: p }))

function SyncDevicesPage() {
  const qc = useQueryClient()
  const devicesQ = useQuery({ queryKey: SYNC_KEY, queryFn: listSyncDevices })

  const invalidate = () => qc.invalidateQueries({ queryKey: SYNC_KEY })

  return (
    <div className="mx-auto max-w-4xl p-6">
      <PageHeader
        title="Devices & Sync"
        description="Register and manage the headless clients that mirror folders to a device via selective sync."
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
  return (
    <section className="mb-6 rounded-lg border border-primary/40 bg-primary/5 p-4 text-sm">
      <div className="flex items-start gap-3">
        <FolderSync className="mt-0.5 h-5 w-5 shrink-0 text-primary" />
        <div className="text-muted-foreground">
          <p className="font-semibold text-foreground">Selective-sync devices</p>
          <p className="mt-1">
            Each device here is a <strong>headless sync client</strong> (the selective-sync agent)
            that mirrors a chosen set of folders down to a machine using a per-device cursor. This
            is the current sync surface. The native desktop drive / tray client — a mounted virtual
            drive with on-demand hydration — is a separate, deferred client and is not managed from
            this page. Register a device, pick the folders it should mirror, and revoke it to cut off
            access if the machine is lost or rotated.
          </p>
        </div>
      </div>
    </section>
  )
}

function RegisterDeviceCard({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState('')
  const [platform, setPlatform] = useState<SyncPlatform>('linux')
  const [workspaceId, setWorkspaceId] = useState('')

  const wsQ = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })
  const workspaceOpts = useMemo(
    () => [
      { value: '__all__', label: 'All workspaces' },
      ...(wsQ.data ?? []).map((w) => ({ value: w.id, label: w.name })),
    ],
    [wsQ.data],
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
    defaultErrorMessage: 'Could not register device',
  })

  return (
    <Card className="mb-6 p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Plus className="h-4 w-4" /> Register device
      </div>
      <div className="grid gap-4 sm:grid-cols-3">
        <div className="space-y-1.5">
          <label className="text-sm font-medium" htmlFor="sync-device-name">
            Name
          </label>
          <Input
            id="sync-device-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. ops-laptop-01"
          />
        </div>
        <LabeledSelect
          label="Platform"
          value={platform}
          onValueChange={(v) => setPlatform(v as SyncPlatform)}
          options={PLATFORM_OPTS}
        />
        <LabeledSelect
          label="Workspace (optional)"
          value={workspaceId || '__all__'}
          onValueChange={(v) => setWorkspaceId(v)}
          options={workspaceOpts}
          placeholder={wsQ.isLoading ? 'Loading…' : 'All workspaces'}
        />
      </div>
      <div className="mt-4">
        <Button
          onClick={() => create.mutate()}
          disabled={create.isPending || name.trim() === ''}
          loading={create.isPending}
        >
          Register device
        </Button>
      </div>
    </Card>
  )
}

function DevicesTable({ devices, onDone }: { devices: SyncDevice[]; onDone: () => void }) {
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [revokeId, setRevokeId] = useState<string | null>(null)

  const revoke = useAppMutation({
    mutationFn: (id: string) => revokeSyncDevice(id),
    onSuccess: () => {
      toast.success('Device revoked')
      setRevokeId(null)
      onDone()
    },
    defaultErrorMessage: 'Could not revoke device',
  })

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Laptop className="h-4 w-4" /> Devices
      </div>
      {devices.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No devices registered. Register one above to start mirroring folders to a machine.
        </p>
      ) : (
        <div className="overflow-hidden rounded-lg bg-muted shadow-neu-inset">
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-start text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-3 py-2" />
                <th className="px-3 py-2">Name</th>
                <th className="px-3 py-2">Platform</th>
                <th className="px-3 py-2">Last seen</th>
                <th className="px-3 py-2">Status</th>
                <th className="px-3 py-2">Folders</th>
                <th className="px-3 py-2">Sync position</th>
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
                          aria-label={expanded ? 'Collapse' : 'Expand folder picker'}
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
                      <td className="px-3 py-2">{d.platform || '—'}</td>
                      <td className="px-3 py-2 text-muted-foreground">
                        {d.last_seen_at ? formatDateTime(d.last_seen_at) : 'never'}
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
                          <Trash2 className="h-3.5 w-3.5" /> Revoke
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
        title="Revoke device?"
        description="The device will stop syncing immediately and any locally-cached mirror is orphaned. This cannot be undone; the device must be re-registered to sync again."
        confirmLabel="Revoke"
        destructive
        loading={revoke.isPending}
        onConfirm={() => revokeId && revoke.mutate(revokeId)}
      />
    </Card>
  )
}

function StatusBadge({ revoked }: { revoked: boolean }) {
  if (revoked) {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-destructive/10 px-2 py-0.5 text-xs font-medium text-destructive">
        <CircleSlash className="h-3 w-3" /> Revoked
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs font-medium text-success">
      <CheckCircle2 className="h-3 w-3" /> Active
    </span>
  )
}

function CursorIndicator({ cursor }: { cursor: string }) {
  if (!cursor) {
    return <span className="text-xs text-muted-foreground">not yet synced</span>
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
      toast.success('Selective-sync folders saved')
      onDone()
    },
    defaultErrorMessage: 'Could not save folders',
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
        Selective-sync folders for {device.name}
      </div>
      {wsQ.isLoading || foldersQ.isLoading ? (
        <Spinner />
      ) : rows.length === 0 && orphanIds.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No folders available{device.workspace_id ? ' in the pinned workspace' : ''}. Create a
          folder first, then return here to select it.
        </p>
      ) : (
        <div className="max-h-64 overflow-y-auto rounded-md bg-muted shadow-neu-inset">
          <ul className="divide-y divide-border">
            {rows.map(({ workspaceName, folder }) => (
              <li key={folder.id}>
                <label className="flex cursor-pointer items-center gap-2 px-3 py-1.5 text-sm hover:bg-accent/40">
                  <input
                    type="checkbox"
                    checked={selected.has(folder.id)}
                    onChange={() => toggle(folder.id)}
                  />
                  <span className="font-medium">{folder.name}</span>
                  <span className="text-xs text-muted-foreground">
                    {workspaceName}
                    {folder.path ? ` · ${folder.path}` : ''}
                  </span>
                </label>
              </li>
            ))}
            {orphanIds.map((id) => (
              <li key={id}>
                <label className="flex cursor-pointer items-center gap-2 px-3 py-1.5 text-sm hover:bg-accent/40">
                  <input type="checkbox" checked onChange={() => toggle(id)} />
                  <span className="font-mono text-xs">{id}</span>
                  <span className="text-xs text-warning-strong">unresolved — uncheck to remove</span>
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
          Save folders
        </Button>
        <span className="text-xs text-muted-foreground">{selected.size} selected</span>
      </div>
    </div>
  )
}
