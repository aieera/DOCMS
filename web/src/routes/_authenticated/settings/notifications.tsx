import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { BellOff, Trash2, Clock, Save } from 'lucide-react'

import {
  CHANNELS, EVENT_TYPES,
  getMatrix, putMatrix,
  listSnoozes, deleteSnooze,
  getDND, putDND, deleteDND,
  type Channel, type PrefCell,
} from '@/api/notification-prefs'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Spinner } from '@/components/ui/Spinner'

// /settings/notifications — ADR 0086 unified preferences page.
//
// Three blocks: matrix grid (events × channels with enable +
// digest), DND window, active snoozes. The page lazy-renders
// defaults — the backend stores `is_enabled=false` rows only when
// the user actually saves; until then every cell is "off" except
// in_app, which is the documented default.

export const Route = createFileRoute('/_authenticated/settings/notifications')({
  component: NotificationsSettings,
})

// keyOf builds the stable map key the matrix uses.
const keyOf = (eventType: string, channel: Channel) => `${eventType}::${channel}`

function NotificationsSettings() {
  const qc = useQueryClient()
  const matrix = useQuery({ queryKey: ['notif-matrix'], queryFn: getMatrix })
  const snoozes = useQuery({ queryKey: ['notif-snoozes'], queryFn: listSnoozes })
  const dnd = useQuery({ queryKey: ['notif-dnd'], queryFn: getDND })

  // Local edits to the grid; we save the whole thing on click so
  // the user has a clear "are my changes persisted?" affordance.
  // Initialized once from the server payload.
  const [grid, setGrid] = useState<Record<string, { is_enabled: boolean; digest_enabled: boolean }>>({})
  const [dirty, setDirty] = useState(false)

  useEffect(() => {
    if (!matrix.data) return
    const next: typeof grid = {}
    for (const ev of EVENT_TYPES) {
      for (const ch of CHANNELS) {
        // in_app default = on; everything else default = off.
        next[keyOf(ev.id, ch)] = { is_enabled: ch === 'in_app', digest_enabled: false }
      }
    }
    for (const c of matrix.data) {
      next[keyOf(c.event_type, c.channel)] = { is_enabled: c.is_enabled, digest_enabled: c.digest_enabled }
    }
    setGrid(next)
    setDirty(false)
  }, [matrix.data])

  const saveMatrix = useMutation({
    mutationFn: async () => {
      const cells: PrefCell[] = []
      for (const ev of EVENT_TYPES) {
        for (const ch of CHANNELS) {
          const cell = grid[keyOf(ev.id, ch)]
          if (!cell) continue
          // Skip pure-default rows so we don't bloat the table —
          // matches the ADR's "first edit upserts a concrete row".
          if (!cell.is_enabled && !cell.digest_enabled && ch !== 'in_app') continue
          cells.push({ event_type: ev.id, channel: ch, is_enabled: cell.is_enabled, digest_enabled: cell.digest_enabled })
        }
      }
      await putMatrix(cells)
    },
    onSuccess: () => {
      toast.success('Notification preferences saved')
      qc.invalidateQueries({ queryKey: ['notif-matrix'] })
      setDirty(false)
    },
    onError: () => toast.error('Failed to save preferences'),
  })

  return (
    <div className="mx-auto max-w-5xl p-6">
      <PageHeader
        title="Notifications"
        description="Choose which events ping you and on which channels. Set quiet hours and active snoozes below."
      />

      <section className="mb-8 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4" data-testid="prefs-matrix-section">
        <header className="mb-3 flex items-center justify-between">
          <h2 className="text-lg font-semibold">Event matrix</h2>
          <Button
            size="sm"
            disabled={!dirty}
            loading={saveMatrix.isPending}
            onClick={() => saveMatrix.mutate()}
            data-testid="prefs-matrix-save"
          >
            <Save className="me-1 h-4 w-4" /> Save
          </Button>
        </header>
        {matrix.isLoading ? (
          <Spinner />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-[var(--color-border)]">
                  <th className="py-2 text-start font-medium">Event</th>
                  {CHANNELS.map((ch) => (
                    <th key={ch} className="px-2 py-2 text-center font-medium capitalize">
                      {ch.replace('_', '-')}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {EVENT_TYPES.map((ev) => (
                  <tr key={ev.id} className="border-b border-[var(--color-border)]" data-testid={`prefs-row-${ev.id}`}>
                    <td className="py-2 pe-4">{ev.label}</td>
                    {CHANNELS.map((ch) => {
                      const cell = grid[keyOf(ev.id, ch)] ?? { is_enabled: false, digest_enabled: false }
                      return (
                        <td key={ch} className="px-2 py-2 text-center">
                          <label className="inline-flex items-center gap-1">
                            <input
                              type="checkbox"
                              checked={cell.is_enabled}
                              onChange={(e) => {
                                setGrid((g) => ({ ...g, [keyOf(ev.id, ch)]: { ...cell, is_enabled: e.target.checked } }))
                                setDirty(true)
                              }}
                              data-testid={`prefs-cell-${ev.id}-${ch}`}
                              aria-label={`${ev.label} on ${ch}`}
                            />
                            <button
                              type="button"
                              onClick={() => {
                                setGrid((g) => ({ ...g, [keyOf(ev.id, ch)]: { ...cell, digest_enabled: !cell.digest_enabled } }))
                                setDirty(true)
                              }}
                              className={`rounded px-1 text-[10px] ${cell.digest_enabled ? 'bg-blue-500 text-white' : 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300'}`}
                              title="Bundle into 5-minute digest"
                              aria-label={`Toggle digest for ${ev.label} on ${ch}`}
                              data-testid={`prefs-digest-${ev.id}-${ch}`}
                            >
                              D
                            </button>
                          </label>
                        </td>
                      )
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
            <p className="mt-2 text-xs text-[var(--color-text-secondary)]">
              The <strong>D</strong> badge bundles bursts of the same event into a single digest, sent up to 5 minutes after the first one.
            </p>
          </div>
        )}
      </section>

      <DNDBlock dnd={dnd.data ?? null} loading={dnd.isLoading} />

      <SnoozesBlock snoozes={snoozes.data ?? []} loading={snoozes.isLoading} />
    </div>
  )
}

// ----- Do Not Disturb --------------------------------------------

function DNDBlock({ dnd, loading }: { dnd: { dnd_start: string; dnd_end: string; timezone: string } | null; loading: boolean }) {
  const qc = useQueryClient()
  const browserTZ = useMemo(() => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC', [])
  const [start, setStart] = useState('')
  const [end, setEnd] = useState('')
  const [tz, setTz] = useState(browserTZ)

  useEffect(() => {
    if (dnd) {
      setStart(dnd.dnd_start)
      setEnd(dnd.dnd_end)
      setTz(dnd.timezone || browserTZ)
    }
  }, [dnd, browserTZ])

  const save = useMutation({
    mutationFn: () => putDND({ start, end, timezone: tz }),
    onSuccess: () => {
      toast.success('Do-not-disturb saved')
      qc.invalidateQueries({ queryKey: ['notif-dnd'] })
    },
    onError: () => toast.error('Failed to save'),
  })
  const clear = useMutation({
    mutationFn: () => deleteDND(),
    onSuccess: () => {
      toast.success('Do-not-disturb cleared')
      setStart(''); setEnd('')
      qc.invalidateQueries({ queryKey: ['notif-dnd'] })
    },
  })

  return (
    <section className="mb-8 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4" data-testid="prefs-dnd-section">
      <header className="mb-3 flex items-center justify-between">
        <h2 className="text-lg font-semibold">Do not disturb</h2>
      </header>
      {loading ? <Spinner /> : (
        <div className="flex flex-wrap items-end gap-4">
          <div>
            <label className="block text-xs text-[var(--color-text-secondary)]">Start</label>
            <input type="time" value={start} onChange={(e) => setStart(e.target.value)} data-testid="dnd-start"
              className="mt-1 h-9 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 text-sm" />
          </div>
          <div>
            <label className="block text-xs text-[var(--color-text-secondary)]">End</label>
            <input type="time" value={end} onChange={(e) => setEnd(e.target.value)} data-testid="dnd-end"
              className="mt-1 h-9 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 text-sm" />
          </div>
          <div>
            <label className="block text-xs text-[var(--color-text-secondary)]">Timezone</label>
            <input type="text" value={tz} onChange={(e) => setTz(e.target.value)} data-testid="dnd-tz"
              className="mt-1 h-9 w-56 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 text-sm" />
          </div>
          <Button
            size="sm"
            onClick={() => save.mutate()}
            disabled={!start || !end}
            loading={save.isPending}
            data-testid="dnd-save"
          >
            <Clock className="me-1 h-4 w-4" /> Save
          </Button>
          {dnd && (
            <Button size="sm" variant="outline" onClick={() => clear.mutate()} loading={clear.isPending} data-testid="dnd-clear">
              Clear
            </Button>
          )}
        </div>
      )}
    </section>
  )
}

// ----- Active Snoozes --------------------------------------------

function SnoozesBlock({ snoozes, loading }: { snoozes: { id: string; event_type: string; until_at: string }[]; loading: boolean }) {
  const qc = useQueryClient()
  const cancel = useMutation({
    mutationFn: deleteSnooze,
    onSuccess: () => {
      toast.success('Snooze cancelled')
      qc.invalidateQueries({ queryKey: ['notif-snoozes'] })
    },
  })
  return (
    <section className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4" data-testid="prefs-snoozes-section">
      <header className="mb-3 flex items-center justify-between">
        <h2 className="text-lg font-semibold">Active snoozes</h2>
      </header>
      {loading ? <Spinner /> : snoozes.length === 0 ? (
        <p className="text-sm text-[var(--color-text-secondary)]">
          No active snoozes. Tap <BellOff className="inline h-3 w-3" /> on any notification to mute its event type for 1 hour.
        </p>
      ) : (
        <ul className="divide-y divide-[var(--color-border)]" data-testid="snooze-list">
          {snoozes.map((s) => (
            <li key={s.id} className="flex items-center justify-between py-2 text-sm" data-testid={`snooze-row-${s.id}`}>
              <div>
                <div className="font-medium">{s.event_type === '*' ? 'All events' : s.event_type}</div>
                <div className="text-xs text-[var(--color-text-secondary)]">
                  Until {new Date(s.until_at).toLocaleString()}
                </div>
              </div>
              <Button size="sm" variant="ghost" onClick={() => cancel.mutate(s.id)} loading={cancel.isPending} data-testid={`snooze-cancel-${s.id}`}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
