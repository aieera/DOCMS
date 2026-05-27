import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Bell, BellOff, Edit, Globe, Lock, Play, Sparkles, Trash2, Users, UserPlus } from 'lucide-react'

import {
  listSavedSearches,
  updateSavedSearch,
  deleteSavedSearch,
  subscribeSavedSearch,
  unsubscribeSavedSearch,
  promoteSmartFolder,
  demoteSmartFolder,
  type SavedSearch,
  type TreeVisibility,
} from '@/api/savedSearches'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import { useAuthStore } from '@/store/authStore'

// ADR 0085 — saved-searches management page.
//
// Lists every saved search the user owns. Per row:
//   Run             → /search rehydrated with the saved query+filters
//   Edit            → opens the edit dialog (name + query + frequency)
//   Convert         → flips notify; converts a saved search into an
//                     alert (or back). The 60s reconciler in the
//                     workflow worker creates/deletes the Temporal
//                     Schedule within a minute.
//   Subscribe       → admin can add other users to the alert; the
//                     owner is implicitly subscribed when notify=true.
//   Delete          → drops the row + cascades subscribers + cancels
//                     the schedule.
//
// Channel preferences (in_app / email / digest) live on the
// subscribe dialog — each subscriber picks their own.

const CHANNELS = ['in_app', 'email', 'digest'] as const

function SavedSearchesPage() {
  const navigate = useNavigate({ from: '/saved-searches' })
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role)
  const isAdmin = role === 'owner' || role === 'admin'

  const { data: rows, isLoading } = useQuery({
    queryKey: ['saved-searches'],
    queryFn: listSavedSearches,
  })

  const [editing, setEditing] = useState<SavedSearch | null>(null)
  const [subscribing, setSubscribing] = useState<SavedSearch | null>(null)
  const [promoting, setPromoting] = useState<SavedSearch | null>(null)

  // ADR 0100 — promote/demote a saved search to a smart folder (pinned
  // to the sidebar). Invalidates both queries because the sidebar reads
  // `smart-folders` while this page reads `saved-searches`.
  const demoteMut = useMutation({
    mutationFn: demoteSmartFolder,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['saved-searches'] })
      qc.invalidateQueries({ queryKey: ['smart-folders'] })
      toast.success('Removed from sidebar')
    },
    onError: () => toast.error('Could not unpin'),
  })

  const patchMut = useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: Parameters<typeof updateSavedSearch>[1] }) =>
      updateSavedSearch(id, patch),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['saved-searches'] })
      setEditing(null)
      toast.success('Saved')
    },
    onError: () => toast.error('Could not save'),
  })
  const deleteMut = useMutation({
    mutationFn: deleteSavedSearch,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['saved-searches'] })
      toast.success('Deleted')
    },
    onError: () => toast.error('Could not delete'),
  })

  const handleRun = (s: SavedSearch) => {
    const f = (s.filters ?? {}) as Record<string, unknown>
    navigate({
      to: '/search',
      search: () => ({
        q: s.query || undefined,
        tag:             arrayOf(f.tags),
        classification:  arrayOf(f.document_class),
        lifecycle_state: arrayOf(f.lifecycle_state),
        mime_type:       arrayOf(f.mime_type),
        author:          arrayOf(f.created_by_name),
        region_pin:      arrayOf(f.region_pin),
      }),
    })
  }

  const handleConvert = (s: SavedSearch) => {
    patchMut.mutate({ id: s.id, patch: { notify: !s.notify } })
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  return (
    <div>
      <PageHeader
        title="Saved searches"
        description="Saved queries + alerts. Toggle 'alert' to get notified when new docs match."
      />

      {(rows ?? []).length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
          You haven&apos;t saved any searches yet. Click <strong>Save</strong> on the search page to get started.
        </div>
      )}

      <div className="space-y-3" data-testid="saved-search-list">
        {(rows ?? []).map((s) => (
          <div
            key={s.id}
            className="flex items-start justify-between rounded-lg border border-border bg-card p-4"
            data-testid={`saved-search-row-${s.id}`}
          >
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="font-medium">{s.name}</span>
                {s.notify ? (
                  <span
                    className="inline-flex items-center gap-1 rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[11px] font-semibold text-amber-700 dark:text-amber-300"
                    data-testid={`alert-badge-${s.id}`}
                    title="An alert fires when new matches show up"
                  >
                    <Bell className="h-3 w-3" /> Alerting
                  </span>
                ) : null}
                {(s.subscriber_count ?? 0) > 0 && (
                  <span className="text-xs text-muted-foreground">
                    {s.subscriber_count} subscriber{s.subscriber_count === 1 ? '' : 's'}
                  </span>
                )}
              </div>
              <p className="mt-1 truncate text-sm text-muted-foreground">
                {s.query || <em>(no query — filters only)</em>}
              </p>
              {s.last_run_at && (
                <p className="mt-1 text-xs text-muted-foreground">
                  Last run {new Date(s.last_run_at).toLocaleString()}
                </p>
              )}
            </div>

            <div className="ms-4 flex shrink-0 items-center gap-2">
              <Button variant="ghost" onClick={() => handleRun(s)} data-testid={`run-${s.id}`}>
                <Play className="h-4 w-4" /> Run
              </Button>
              <Button variant="ghost" onClick={() => setEditing(s)} data-testid={`edit-${s.id}`}>
                <Edit className="h-4 w-4" /> Edit
              </Button>
              {s.is_smart_folder ? (
                <Button
                  variant="ghost"
                  onClick={() => {
                    if (confirm(`Remove "${s.name}" from the sidebar?`)) demoteMut.mutate(s.id)
                  }}
                  disabled={demoteMut.isPending}
                  data-testid={`demote-${s.id}`}
                  title="Remove from sidebar"
                >
                  <Sparkles className="h-4 w-4 text-violet-500" /> Unpin
                </Button>
              ) : (
                <Button
                  variant="ghost"
                  onClick={() => setPromoting(s)}
                  data-testid={`promote-${s.id}`}
                  title="Pin to sidebar as a smart folder"
                >
                  <Sparkles className="h-4 w-4" /> Pin
                </Button>
              )}
              <Button
                variant="ghost"
                onClick={() => handleConvert(s)}
                disabled={patchMut.isPending}
                data-testid={`convert-${s.id}`}
              >
                {s.notify ? <BellOff className="h-4 w-4" /> : <Bell className="h-4 w-4" />}
                {s.notify ? 'Stop alert' : 'Make alert'}
              </Button>
              {isAdmin && s.notify && (
                <Button
                  variant="ghost"
                  onClick={() => setSubscribing(s)}
                  data-testid={`subscribe-${s.id}`}
                >
                  <UserPlus className="h-4 w-4" /> Subscribe
                </Button>
              )}
              {/* Destructive action separated by a thin divider so it
                  reads as a different concern from the routine row
                  actions above. */}
              <span aria-hidden className="mx-1 h-5 w-px bg-border" />
              <Button
                variant="ghost"
                onClick={() => {
                  if (confirm(`Delete"${s.name}"?`)) deleteMut.mutate(s.id)
                }}
                disabled={deleteMut.isPending}
                data-testid={`delete-${s.id}`}
                aria-label={`Delete saved search: ${s.name}`}
                title={`Delete saved search: ${s.name}`}
                className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              >
                <Trash2 className="h-4 w-4" aria-hidden="true" />
                <span className="sr-only">Delete {s.name}</span>
              </Button>
            </div>
          </div>
        ))}
      </div>

      {editing && (
        <EditDialog
          ss={editing}
          onClose={() => setEditing(null)}
          onSave={(patch) => patchMut.mutate({ id: editing.id, patch })}
          saving={patchMut.isPending}
        />
      )}
      {subscribing && (
        <SubscribeDialog
          ss={subscribing}
          onClose={() => setSubscribing(null)}
          onChanged={() => {
            qc.invalidateQueries({ queryKey: ['saved-searches'] })
          }}
        />
      )}
      {promoting && (
        <PromoteSmartFolderDialog
          ss={promoting}
          onClose={() => setPromoting(null)}
          onPromoted={() => {
            qc.invalidateQueries({ queryKey: ['saved-searches'] })
            qc.invalidateQueries({ queryKey: ['smart-folders'] })
            setPromoting(null)
          }}
          isAdmin={isAdmin}
        />
      )}
    </div>
  )
}

// PromoteSmartFolderDialog — ADR 0100. Promotes a saved search to a
// sidebar-pinned smart folder. `tree_visibility` decides who else can
// see it: 'private' (owner only), 'workspace' (everyone in this
// workspace; admin-only), 'public' (everyone in the tenant; admin-only).
// Workspace visibility requires a workspace_id; for Phase 1 we only
// expose the saved search's existing workspace_id, which the backend
// already validated when the search was created.
function PromoteSmartFolderDialog({
  ss,
  onClose,
  onPromoted,
  isAdmin,
}: {
  ss: SavedSearch
  onClose: () => void
  onPromoted: () => void
  isAdmin: boolean
}) {
  const [visibility, setVisibility] = useState<TreeVisibility>('private')
  const [icon, setIcon] = useState('sparkles')

  const mut = useMutation({
    mutationFn: () => promoteSmartFolder(ss.id, {
      tree_visibility: visibility,
      workspace_id:    visibility === 'workspace' ? ss.workspace_id ?? null : null,
      icon,
    }),
    onSuccess: () => {
      toast.success(`Pinned "${ss.name}" to the sidebar`)
      onPromoted()
    },
    onError: () => toast.error('Pin failed'),
  })

  return (
    <Dialog
      open
      onOpenChange={(o) => { if (!o) onClose() }}
      title={`Pin "${ss.name}" to the sidebar`}
      description="Smart folders are saved searches that live in the sidebar. Anyone who can see the folder runs the underlying query when they click it."
    >
      <div className="space-y-3">
        <div className="space-y-1">
          <p className="text-xs font-semibold text-muted-foreground">Visibility</p>
          <VisibilityOption
            current={visibility}
            value="private"
            label="Only me"
            description="Just appears in your sidebar."
            icon={Lock}
            onSelect={setVisibility}
          />
          <VisibilityOption
            current={visibility}
            value="workspace"
            label="Everyone in this workspace"
            description={ss.workspace_id
              ? 'Visible in the sidebar for every workspace member.'
              : 'This saved search has no workspace — pick another visibility.'}
            icon={Users}
            disabled={!isAdmin || !ss.workspace_id}
            onSelect={setVisibility}
          />
          <VisibilityOption
            current={visibility}
            value="public"
            label="Everyone in the tenant"
            description="Visible in the sidebar for every user in your organization."
            icon={Globe}
            disabled={!isAdmin}
            onSelect={setVisibility}
          />
        </div>

        <Input
          label="Icon (lucide name)"
          value={icon}
          onChange={(e) => setIcon(e.target.value)}
          placeholder="sparkles, folder, star…"
        />

        <div className="flex items-center justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose} disabled={mut.isPending}>Cancel</Button>
          <Button onClick={() => mut.mutate()} disabled={mut.isPending}>
            {mut.isPending ? <Spinner className="h-4 w-4" /> : <Sparkles className="me-1 h-4 w-4" />}
            Pin to sidebar
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function VisibilityOption({
  current,
  value,
  label,
  description,
  icon: Icon,
  disabled,
  onSelect,
}: {
  current: TreeVisibility
  value: TreeVisibility
  label: string
  description: string
  icon: typeof Lock
  disabled?: boolean
  onSelect: (v: TreeVisibility) => void
}) {
  const active = current === value
  return (
    <button
      type="button"
      onClick={() => onSelect(value)}
      disabled={disabled}
      className={`flex w-full items-start gap-3 rounded-md border p-2 text-start text-sm transition-colors ${
        active
          ? 'border-violet-500/50 bg-violet-50/40 dark:bg-violet-950/15'
          : 'border-border bg-card hover:bg-accent'
      } ${disabled ? 'cursor-not-allowed opacity-50' : ''}`}
    >
      <Icon className={`mt-0.5 h-4 w-4 ${active ? 'text-violet-500' : 'text-muted-foreground'}`} />
      <div className="min-w-0 flex-1">
        <div className="font-medium">{label}</div>
        <div className="text-xs text-muted-foreground">{description}</div>
      </div>
    </button>
  )
}

function EditDialog({
  ss, onClose, onSave, saving,
}: {
  ss: SavedSearch
  onClose: () => void
  onSave: (patch: Parameters<typeof updateSavedSearch>[1]) => void
  saving: boolean
}) {
  const [name, setName] = useState(ss.name)
  const [query, setQuery] = useState(ss.query)
  const [interval, setInterval] = useState(String(ss.notify_interval_minutes ?? 15))
  const [cron, setCron] = useState(ss.alert_frequency_cron ?? '')

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Edit saved search">
      <div className="space-y-3">
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} data-testid="edit-name" />
        <Input label="Query" value={query} onChange={(e) => setQuery(e.target.value)} data-testid="edit-query" />
        <Input
          label="Notify every (minutes; ignored if cron set)"
          type="number"
          min={1}
          value={interval}
          onChange={(e) => setInterval(e.target.value)}
          data-testid="edit-interval"
        />
        <Input
          label="Cron (optional, e.g. '0 9 * * 1-5' for 9am weekdays)"
          value={cron}
          onChange={(e) => setCron(e.target.value)}
          placeholder="leave blank to use interval"
          data-testid="edit-cron"
        />
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button
            onClick={() => onSave({
              name,
              query,
              notify_interval_minutes: Number(interval),
              alert_frequency_cron: cron,
            })}
            disabled={saving}
            data-testid="edit-save"
          >
            {saving ? <Spinner className="h-4 w-4" /> : null}
            Save
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function SubscribeDialog({
  ss, onClose, onChanged,
}: {
  ss: SavedSearch
  onClose: () => void
  onChanged: () => void
}) {
  const [userId, setUserId] = useState('')
  const [channels, setChannels] = useState<string[]>(['in_app'])

  const subMut = useMutation({
    mutationFn: () => subscribeSavedSearch(ss.id, { user_id: userId || undefined, channels }),
    onSuccess: () => {
      toast.success('Subscribed')
      onChanged()
      setUserId('')
    },
    onError: () => toast.error('Could not subscribe'),
  })
  const unsubMut = useMutation({
    mutationFn: (uid: string) => unsubscribeSavedSearch(ss.id, uid),
    onSuccess: () => {
      toast.success('Unsubscribed')
      onChanged()
    },
    onError: () => toast.error('Could not unsubscribe'),
  })

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={`Subscribers — ${ss.name}`} size="lg">
      <div className="space-y-3">
        <div className="rounded-md border border-border p-3">
          <p className="mb-2 text-xs font-medium">Current subscribers</p>
          {(ss.subscribers ?? []).length === 0 ? (
            <p className="text-sm text-muted-foreground">
              Only the owner is subscribed (implicit).
            </p>
          ) : (
            <ul className="space-y-1" data-testid="subscriber-list">
              {(ss.subscribers ?? []).map((sub) => (
                <li key={sub.user_id} className="flex items-center justify-between text-sm">
                  <span>
                    {sub.user_id}
                    <span className="ms-2 text-xs text-muted-foreground">
                      ({sub.channels.join(', ')})
                    </span>
                  </span>
                  <Button variant="ghost" onClick={() => unsubMut.mutate(sub.user_id)}>
                    Remove
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </div>

        <Input
          label="Add subscriber (user id)"
          value={userId}
          onChange={(e) => setUserId(e.target.value)}
          data-testid="subscribe-user-id"
        />

        <div>
          <p className="mb-1 text-sm font-medium">Channels</p>
          <div className="flex gap-3">
            {CHANNELS.map((c) => (
              <label key={c} className="flex items-center gap-1 text-sm">
                <input
                  type="checkbox"
                  checked={channels.includes(c)}
                  onChange={(e) => {
                    setChannels((prev) =>
                      e.target.checked ? [...prev, c] : prev.filter((x) => x !== c),
                    )
                  }}
                  data-testid={`channel-${c}`}
                />
                {c}
              </label>
            ))}
          </div>
        </div>

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>Close</Button>
          <Button
            onClick={() => subMut.mutate()}
            disabled={!userId || channels.length === 0 || subMut.isPending}
            data-testid="subscribe-confirm"
          >
            {subMut.isPending ? <Spinner className="h-4 w-4" /> : null}
            Subscribe
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function arrayOf(v: unknown): string[] | undefined {
  if (!v) return undefined
  if (Array.isArray(v)) return v.length ? v.map(String) : undefined
  return [String(v)]
}

export const Route = createFileRoute('/_authenticated/saved-searches')({
  component: SavedSearchesPage,
})
