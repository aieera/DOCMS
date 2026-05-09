import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Bell, BellOff, Edit, Play, Trash2, UserPlus } from 'lucide-react'

import {
  listSavedSearches,
  updateSavedSearch,
  deleteSavedSearch,
  subscribeSavedSearch,
  unsubscribeSavedSearch,
  type SavedSearch,
} from '@/api/savedSearches'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Badge } from '@/components/ui/Badge'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
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
        <div className="rounded-lg border border-dashed border-[var(--color-border)] p-8 text-center text-sm text-[var(--color-text-secondary)]">
          You haven&apos;t saved any searches yet. Click <strong>Save</strong> on the search page to get started.
        </div>
      )}

      <div className="space-y-3" data-testid="saved-search-list">
        {(rows ?? []).map((s) => (
          <div
            key={s.id}
            className="flex items-start justify-between rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
            data-testid={`saved-search-row-${s.id}`}
          >
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="font-medium">{s.name}</span>
                {s.notify ? (
                  <Badge variant="info" data-testid={`alert-badge-${s.id}`}>
                    <Bell className="mr-1 h-3 w-3 inline" /> Alert
                  </Badge>
                ) : null}
                {(s.subscriber_count ?? 0) > 0 && (
                  <span className="text-xs text-[var(--color-text-secondary)]">
                    {s.subscriber_count} subscriber{s.subscriber_count === 1 ? '' : 's'}
                  </span>
                )}
              </div>
              <p className="mt-1 truncate text-sm text-[var(--color-text-secondary)]">
                {s.query || <em>(no query — filters only)</em>}
              </p>
              {s.last_run_at && (
                <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
                  Last run {new Date(s.last_run_at).toLocaleString()}
                </p>
              )}
            </div>

            <div className="ml-4 flex shrink-0 items-center gap-1">
              <Button variant="ghost" onClick={() => handleRun(s)} data-testid={`run-${s.id}`}>
                <Play className="h-4 w-4" /> Run
              </Button>
              <Button variant="ghost" onClick={() => setEditing(s)} data-testid={`edit-${s.id}`}>
                <Edit className="h-4 w-4" /> Edit
              </Button>
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
              <Button
                variant="ghost"
                onClick={() => {
                  if (confirm(`Delete "${s.name}"?`)) deleteMut.mutate(s.id)
                }}
                disabled={deleteMut.isPending}
                data-testid={`delete-${s.id}`}
              >
                <Trash2 className="h-4 w-4 text-red-500" />
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
    </div>
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
        <div className="rounded-md border border-[var(--color-border)] p-3">
          <p className="mb-2 text-xs font-medium">Current subscribers</p>
          {(ss.subscribers ?? []).length === 0 ? (
            <p className="text-sm text-[var(--color-text-secondary)]">
              Only the owner is subscribed (implicit).
            </p>
          ) : (
            <ul className="space-y-1" data-testid="subscriber-list">
              {(ss.subscribers ?? []).map((sub) => (
                <li key={sub.user_id} className="flex items-center justify-between text-sm">
                  <span>
                    {sub.user_id}
                    <span className="ml-2 text-xs text-[var(--color-text-secondary)]">
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
