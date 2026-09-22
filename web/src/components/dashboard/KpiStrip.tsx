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

  const docTotal = workspaces.data?.reduce((sum, w) => sum + (w.document_count ?? 0), 0)
  const stats = taskStats(tasks.data)

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
        value={workspaces.isLoading || workspaces.isError ? undefined : docTotal ?? 0}
        isError={workspaces.isError}
        hint={workspaces.isError ? 'unable to load' : `across ${workspaces.data?.length ?? 0} workspaces`}
        href="/workspaces"
        // The only KPI with a genuine historical series behind it.
        sparkline={metrics.activity}
        delayIndex={0}
      />
      <KpiTile
        icon={CheckSquare}
        label="Open tasks"
        value={tasks.isLoading || tasks.isError ? undefined : stats.open}
        isError={tasks.isError}
        hint={
          tasks.isError ? 'unable to load'
            : stats.overdue > 0 ? `${stats.overdue} overdue`
            : 'assigned to you'
        }
        hintTone={stats.overdue > 0 ? 'alert' : 'muted'}
        href="/tasks"
        delayIndex={1}
      />
      <KpiTile
        icon={Stamp}
        label="Awaiting approval"
        value={tasks.isLoading || tasks.isError ? undefined : stats.awaitingApproval}
        isError={tasks.isError}
        hint={
          tasks.isError ? 'unable to load'
            : stats.oldestWaitingDays !== null
              ? `oldest waiting ${stats.oldestWaitingDays}d`
              : 'nothing pending'
        }
        href="/tasks"
        delayIndex={2}
      />
      <KpiTile
        icon={Bell}
        label="Unread"
        value={unread.isLoading || unread.isError ? undefined : unread.data ?? 0}
        isError={unread.isError}
        hint={unread.isError ? 'unable to load' : 'notifications'}
        href="/notifications"
        delayIndex={3}
      />
    </div>
  )
}
