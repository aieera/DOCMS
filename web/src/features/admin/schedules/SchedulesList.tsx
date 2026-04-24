// Admin surface for the workflow service's Temporal schedules.
// Sourced from GET /api/v1/platform/schedules (admin-only, role-gated
// server-side). One row per schedule id; the UI never calls Temporal
// directly.
//
// The PR-16 Wave-15.4 SignatureProfileOrphanSweeper schedule lives
// under the id `signature-profile-orphan-sweep-<tenant>`; this view
// surfaces it alongside the existing password-expiry-* and
// ack-reminders-* entries so operators can see a single pane of glass.

import { useQuery } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Clock, PauseCircle, PlayCircle, RefreshCw } from 'lucide-react'

import { listSchedules, type ScheduleView } from '@/api/platform'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Badge } from '@/components/ui/Badge'

// Groups schedules by id prefix so the UI can render friendly section
// headings ("Password Expiry", "Acknowledgement Reminders", etc.)
// rather than a flat list.
//
// Any id that doesn't match a known prefix falls into "Other".
const KNOWN_PREFIXES: Array<{ prefix: string; label: string }> = [
  { prefix: 'password-expiry-', label: 'Password Expiry' },
  { prefix: 'ack-reminders-', label: 'Acknowledgement Reminders' },
  { prefix: 'signature-profile-orphan-sweep-', label: 'Signature Profile Orphan Sweeper' },
]

function sectionFor(id: string): string {
  for (const p of KNOWN_PREFIXES) {
    if (id.startsWith(p.prefix)) return p.label
  }
  return 'Other'
}

// tenantSuffix strips the known prefix from a schedule id, leaving
// the tenant uuid (or whatever tail Temporal has). Used as a row
// label so operators don't have to scan the id prefix twice.
function tenantSuffix(id: string): string {
  for (const p of KNOWN_PREFIXES) {
    if (id.startsWith(p.prefix)) return id.slice(p.prefix.length)
  }
  return id
}

function formatRelative(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const deltaMs = d.getTime() - Date.now()
  const absS = Math.abs(Math.round(deltaMs / 1000))
  if (absS < 60) return `${deltaMs > 0 ? 'in ' : ''}${absS}s${deltaMs < 0 ? ' ago' : ''}`
  const m = Math.round(absS / 60)
  if (m < 60) return `${deltaMs > 0 ? 'in ' : ''}${m}m${deltaMs < 0 ? ' ago' : ''}`
  const h = Math.round(m / 60)
  if (h < 24) return `${deltaMs > 0 ? 'in ' : ''}${h}h${deltaMs < 0 ? ' ago' : ''}`
  const days = Math.round(h / 24)
  return `${deltaMs > 0 ? 'in ' : ''}${days}d${deltaMs < 0 ? ' ago' : ''}`
}

// failureRate derives "did a recent run miss" — num_missed > 0
// indicates Temporal skipped catchup runs. Success count is
// approximated as (num_actions - num_missed) since the Describe API
// doesn't split taken-and-failed from taken-and-succeeded; a
// follow-up can wire per-workflow describe lookups when operators
// need that granularity. Matches the ledger's T-D-4 workflow-level
// signal.
function missedTone(missed: number | undefined): 'red' | 'amber' | 'green' {
  const n = missed ?? 0
  if (n === 0) return 'green'
  if (n < 5) return 'amber'
  return 'red'
}

function toneClass(tone: 'red' | 'amber' | 'green') {
  switch (tone) {
    case 'red':
      return 'bg-red-100 text-red-900 dark:bg-red-900/40 dark:text-red-100'
    case 'amber':
      return 'bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-100'
    default:
      return 'bg-green-100 text-green-900 dark:bg-green-900/40 dark:text-green-100'
  }
}

export function SchedulesList() {
  const q = useQuery({
    queryKey: ['platform-schedules'],
    queryFn: listSchedules,
    refetchInterval: 30_000, // auto-refresh every 30s while the tab is open
  })

  const onRefresh = () => {
    q.refetch()
      .then(() => toast.success('Schedules refreshed'))
      .catch(() => toast.error('Failed to refresh schedules'))
  }

  // Group by known prefix.
  const grouped = new Map<string, ScheduleView[]>()
  for (const s of q.data ?? []) {
    const sec = sectionFor(s.id)
    const arr = grouped.get(sec) ?? []
    arr.push(s)
    grouped.set(sec, arr)
  }

  return (
    <section
      aria-labelledby="schedules-title"
      data-testid="admin-schedules"
      className="space-y-6"
    >
      <header className="flex items-center justify-between">
        <div>
          <h2 id="schedules-title" className="text-sm font-semibold">
            Temporal Schedules
          </h2>
          <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
            Live view of the workflow service's registered schedules. Read-only; to
            pause/modify use <code className="font-mono">temporal schedule</code> CLI.
          </p>
        </div>
        <Button
          variant="ghost"
          size="sm"
          onClick={onRefresh}
          aria-label="Refresh schedules"
          data-testid="schedules-refresh"
        >
          <RefreshCw className="mr-1 h-4 w-4" aria-hidden="true" />
          Refresh
        </Button>
      </header>

      {q.isError ? (
        <div
          role="alert"
          className="rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-900 dark:border-red-900 dark:bg-red-950/40 dark:text-red-100"
        >
          Failed to load schedules — check the workflow service logs.
        </div>
      ) : null}

      {q.isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : (
        [...grouped.entries()].map(([section, rows]) => (
          <div key={section} data-testid={`schedules-section-${section}`} className="space-y-2">
            <h3 className="text-xs font-semibold uppercase text-[var(--color-text-secondary)]">
              {section}
              <span className="ml-2 font-normal">({rows.length})</span>
            </h3>
            <div className="overflow-hidden rounded-xl border border-[var(--color-border)]">
              <table className="w-full text-sm" aria-label={`${section} schedules`}>
                <thead className="bg-[var(--color-bg-secondary)] text-left text-xs text-[var(--color-text-secondary)]">
                  <tr>
                    <th className="px-3 py-2 font-medium">Tenant / id</th>
                    <th className="px-3 py-2 font-medium">State</th>
                    <th className="px-3 py-2 font-medium">Next run</th>
                    <th className="px-3 py-2 font-medium">Last run</th>
                    <th className="px-3 py-2 font-medium">Actions</th>
                    <th className="px-3 py-2 font-medium">Missed</th>
                    <th className="px-3 py-2 font-medium">Running</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((s) => (
                    <tr
                      key={s.id}
                      data-testid={`schedule-row-${s.id}`}
                      className="border-t border-[var(--color-border)]"
                    >
                      <td className="px-3 py-2 font-mono text-xs" title={s.id}>
                        {tenantSuffix(s.id)}
                      </td>
                      <td className="px-3 py-2">
                        {s.paused ? (
                          <Badge variant="outline" className="inline-flex items-center gap-1">
                            <PauseCircle className="h-3 w-3" aria-hidden="true" />
                            Paused
                          </Badge>
                        ) : (
                          <Badge variant="outline" className="inline-flex items-center gap-1">
                            <PlayCircle className="h-3 w-3" aria-hidden="true" />
                            Active
                          </Badge>
                        )}
                      </td>
                      <td className="px-3 py-2 font-mono text-xs">
                        <span
                          className="inline-flex items-center gap-1"
                          title={s.next_run ?? 'no scheduled run'}
                        >
                          <Clock className="h-3 w-3 opacity-60" aria-hidden="true" />
                          {formatRelative(s.next_run)}
                        </span>
                      </td>
                      <td className="px-3 py-2 font-mono text-xs" title={s.last_run ?? 'never run'}>
                        {formatRelative(s.last_run)}
                      </td>
                      <td
                        className="px-3 py-2 font-mono text-xs"
                        data-testid={`schedule-actions-${s.id}`}
                      >
                        {s.num_actions.toLocaleString()}
                      </td>
                      <td className="px-3 py-2 text-xs">
                        <span
                          className={`rounded-md px-2 py-0.5 font-mono ${toneClass(missedTone(s.num_missed))}`}
                          data-testid={`schedule-missed-${s.id}`}
                        >
                          {(s.num_missed ?? 0).toLocaleString()}
                        </span>
                      </td>
                      <td className="px-3 py-2 font-mono text-xs">
                        {s.running_count.toLocaleString()}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        ))
      )}

      {q.data && q.data.length === 0 ? (
        <p className="text-sm text-[var(--color-text-secondary)]">
          No schedules registered yet. Workflows create them on first boot;
          check the workflow service logs if this stays empty.
        </p>
      ) : null}
    </section>
  )
}
