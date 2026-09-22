import { useQuery } from '@tanstack/react-query'
import { Bell, CheckSquare, FileText, Stamp } from 'lucide-react'

import { getUnreadCount } from '@/api/notifications'
import { listMyTasks, taskKeys } from '@/api/tasks'
import { getWorkspaces } from '@/api/workspaces'
import { ErrorState } from '@/components/ui/ErrorState'
import { KpiTile } from './KpiTile'
import { taskStats } from './metrics'
import { useDashboardMetrics } from './useDashboardMetrics'

/**
 * The four headline figures. Each is sourced from a query the dashboard
 * already ran before this redesign, except the document total, which
 * sums Workspace.document_count — Postgres-authoritative, unlike the
 * search index, which lags ingestion.
 *
 * There is deliberately no "storage used" tile: no tenant-wide byte
 * total is readable by a non-admin (storage_by_region lives behind
 * /admin/compliance/overview), and a fabricated one is worse than none.
 */
export function KpiStrip() {
  const workspaces = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces, staleTime: 60_000 })
  const tasks = useQuery({ queryKey: taskKeys.mine(), queryFn: () => listMyTasks(false), staleTime: 30_000 })
  const unread = useQuery({
    queryKey: ['notifications', 'unread-count'], queryFn: getUnreadCount, staleTime: 30_000,
  })
  const metrics = useDashboardMetrics()

  // C1: `document_count` is a proto3 int64, which the gateway's protojson
  // marshaller emits as a JSON STRING ("4") — types/api.ts says `number`,
  // and it lies. A bare `+` concatenated "0"+"4"+"12" into "0412", so
  // coerce each count; if any is not a finite number the total is
  // UNAVAILABLE (rendered as the failed tile), never a confident 0.
  const docTotal = workspaces.data?.reduce<number>((sum, w) => sum + Number(w.document_count ?? 0), 0)
  const docTotalValid = docTotal !== undefined && Number.isFinite(docTotal)
  const stats = taskStats(tasks.data)

  // C2: gate every value on `isPending`, not `isLoading`. In react-query v5
  // `isLoading` is `isPending && isFetching`, so a query that starts
  // offline (fetchStatus 'paused') is `isLoading: false, isError: false,
  // data: undefined` — and used to render a confident 0. `isPending` stays
  // true until data exists and is already false after an error.
  // L105: hint strings are built only from settled, successful data; the
  // static "unable to load" copy is the failed state's own text.
  const docsSettled = !workspaces.isPending && !workspaces.isError
  const docsReady = docsSettled && docTotalValid
  const docsFailed = workspaces.isError || (docsSettled && !docTotalValid)
  const tasksReady = !tasks.isPending && !tasks.isError
  const unreadReady = !unread.isPending && !unread.isError
  const wsCount = workspaces.data?.length ?? 0

  // Fix round 1 — additional requirement: this replaces the previous
  // dashboard's KpiRow.allError card, which the redesign had dropped.
  // Only when every value-bearing query has failed do we replace the
  // whole strip; a single query failing is handled per-tile below via
  // KpiTile's `isError` state so the other three figures stay usable.
  const allFailed = workspaces.isError && tasks.isError && unread.isError
  const retryAll = () => {
    void workspaces.refetch()
    void tasks.refetch()
    void unread.refetch()
  }

  if (allFailed) {
    return (
      <div className="dash-rise rounded-lg bg-card p-5 shadow-neu" data-testid="kpi-strip-error">
        <ErrorState size="sm" message="Couldn't load the dashboard overview." onRetry={retryAll} />
      </div>
    )
  }

  return (
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <KpiTile
        icon={FileText}
        label="Documents"
        value={docsReady ? docTotal : undefined}
        isError={docsFailed}
        hint={
          docsFailed ? 'unable to load'
            : docsReady ? `across ${wsCount.toLocaleString()} ${wsCount === 1 ? 'workspace' : 'workspaces'}`
            : undefined
        }
        href="/workspaces"
        // The only KPI with a genuine historical series behind it.
        sparkline={metrics.activity}
        delayIndex={0}
      />
      <KpiTile
        icon={CheckSquare}
        label="Open tasks"
        value={tasksReady ? stats.open : undefined}
        isError={tasks.isError}
        hint={
          tasks.isError ? 'unable to load'
            : !tasksReady ? undefined
            : stats.overdue > 0 ? `${stats.overdue.toLocaleString()} overdue`
            : 'assigned to you'
        }
        hintTone={tasksReady && stats.overdue > 0 ? 'alert' : 'muted'}
        href="/tasks"
        delayIndex={1}
      />
      <KpiTile
        icon={Stamp}
        label="Awaiting approval"
        value={tasksReady ? stats.awaitingApproval : undefined}
        isError={tasks.isError}
        hint={
          tasks.isError ? 'unable to load'
            : !tasksReady ? undefined
            : stats.oldestWaitingDays === null ? 'nothing pending'
            // M2: "oldest waiting 0d" read as a glitch for a task created today.
            : stats.oldestWaitingDays === 0 ? 'waiting since today'
            : `oldest waiting ${stats.oldestWaitingDays.toLocaleString()}d`
        }
        href="/tasks"
        delayIndex={2}
      />
      <KpiTile
        icon={Bell}
        label="Unread"
        value={unreadReady ? unread.data ?? 0 : undefined}
        isError={unread.isError}
        hint={unread.isError ? 'unable to load' : unreadReady ? 'notifications' : undefined}
        href="/notifications"
        delayIndex={3}
      />
    </div>
  )
}
