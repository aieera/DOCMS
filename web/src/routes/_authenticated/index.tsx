import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  Bell,
  CheckSquare,
  ChevronRight,
  Clock,
  FolderOpen,
  Plus,
  Search,
  Sparkles,
  Upload,
  Workflow,
  type LucideIcon,
} from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { useAuthStore } from '@/store/authStore'
import { listMyTasks, type Task } from '@/api/tasks'
import { getWorkspaces } from '@/api/workspaces'
import { getUnreadCount, getNotifications } from '@/api/notifications'
import { cn } from '@/lib/cn'

function DashboardPage() {
  const user = useAuthStore((s) => s.user)
  const tenantId = useAuthStore((s) => s.tenantId)
  const greeting = greet(user?.display_name?.split(' ')[0])

  return (
    <div className="space-y-8">
      <PageHeader
        title={greeting}
        description={
          tenantId ? (
            <>
              Workspace overview ·{' '}
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{tenantId.slice(0, 8)}</code>
            </>
          ) : (
            'Workspace overview'
          )
        }
      />

      <KpiRow />
      <QuickActions />

      <div className="grid gap-6 lg:grid-cols-2">
        <OpenTasksCard />
        <RecentActivityCard />
      </div>
    </div>
  )
}

function greet(name: string | undefined): string {
  const hour = new Date().getHours()
  const window = hour < 5 ? 'Up late' : hour < 12 ? 'Good morning' : hour < 18 ? 'Good afternoon' : 'Good evening'
  return name ? `${window}, ${name}` : window
}

// Tiny relative-time formatter to avoid pulling in date-fns just
// for the dashboard's two timestamp surfaces. "in 3 hours", "5 min
// ago", "2 days ago", etc. Falls back to the locale date once
// past 30 days where relative units stop being intuitive.
function relTime(iso: string, opts: { addSuffix?: boolean } = {}): string {
  const ms = new Date(iso).getTime() - Date.now()
  const past = ms < 0
  const abs = Math.abs(ms)
  const min = 60_000, hr = 60 * min, day = 24 * hr, wk = 7 * day, mo = 30 * day
  let value: number, unit: string
  if (abs < min) { value = Math.round(abs / 1000); unit = 'sec' }
  else if (abs < hr) { value = Math.round(abs / min); unit = 'min' }
  else if (abs < day) { value = Math.round(abs / hr); unit = 'hour' }
  else if (abs < wk) { value = Math.round(abs / day); unit = 'day' }
  else if (abs < mo) { value = Math.round(abs / wk); unit = 'wk' }
  else return new Date(iso).toLocaleDateString()
  const plural = value === 1 ? '' : 's'
  if (!opts.addSuffix) return `${value} ${unit}${plural}`
  return past ? `${value} ${unit}${plural} ago` : `in ${value} ${unit}${plural}`
}

// ---- KPI cards -----------------------------------------------------------

function KpiRow() {
  const workspaces = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces, staleTime: 60_000 })
  const tasks = useQuery({ queryKey: ['my-tasks'], queryFn: () => listMyTasks(false), staleTime: 30_000 })
  const unread = useQuery({ queryKey: ['notif-count'], queryFn: getUnreadCount, staleTime: 30_000 })

  const wsCount = (workspaces.data as { items?: unknown[] } | undefined)?.items?.length
                ?? (Array.isArray(workspaces.data) ? workspaces.data.length : undefined)
  const openTaskCount = (tasks.data ?? []).filter((t) => t.status === 'open' || t.status === 'in_progress').length
  const overdue = (tasks.data ?? []).filter((t) => t.due_at && new Date(t.due_at) < new Date() && t.status !== 'done' && t.status !== 'cancelled').length

  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <KpiCard
        icon={FolderOpen}
        label="Workspaces"
        value={workspaces.isLoading ? undefined : wsCount ?? 0}
        hint={workspaces.isError ? 'unable to load' : 'tenant total'}
        href="/workspaces"
      />
      <KpiCard
        icon={CheckSquare}
        label="Open tasks"
        value={tasks.isLoading ? undefined : openTaskCount}
        hint={overdue > 0 ? `${overdue} overdue` : tasks.isError ? 'unable to load' : 'assigned to you'}
        hintTone={overdue > 0 ? 'warning' : 'muted'}
        href="/tasks"
      />
      <KpiCard
        icon={Bell}
        label="Unread notifications"
        value={unread.isLoading ? undefined : unread.data ?? 0}
        hint={unread.isError ? 'unable to load' : 'across all channels'}
        href="/notifications"
      />
    </div>
  )
}

interface KpiCardProps {
  icon: LucideIcon
  label: string
  value: number | string | undefined
  hint?: string
  hintTone?: 'muted' | 'warning'
  href?: string
}

function KpiCard({ icon: Icon, label, value, hint, hintTone = 'muted', href }: KpiCardProps) {
  const inner = (
    <Card className="group relative overflow-hidden p-5 transition-shadow hover:shadow-md">
      <div className="flex items-start justify-between">
        <span className="flex h-9 w-9 items-center justify-center rounded-md bg-muted text-foreground">
          <Icon className="h-[1.1rem] w-[1.1rem]" />
        </span>
        {href && (
          <ChevronRight className="h-4 w-4 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
        )}
      </div>
      <p className="mt-4 text-sm font-medium text-muted-foreground">{label}</p>
      <p className="mt-1 text-3xl font-semibold tracking-tight">
        {value === undefined ? <Skeleton className="h-8 w-16" /> : value}
      </p>
      {hint && (
        <p
          className={cn(
            'mt-1 text-xs',
            hintTone === 'warning' ? 'text-warning' : 'text-muted-foreground',
          )}
        >
          {hint}
        </p>
      )}
    </Card>
  )
  return href ? <Link to={href} className="block">{inner}</Link> : inner
}

// ---- Quick actions -------------------------------------------------------

const QUICK_ACTIONS = [
  { icon: Search, label: 'Search documents', href: '/search', description: 'Full-text + semantic across the tenant', kbd: '⌘K' },
  { icon: Sparkles, label: 'Ask the corpus', href: '/ask', description: 'RAG over the documents you can see' },
  { icon: Upload, label: 'Upload', href: '/workspaces', description: 'Drag a file into a workspace' },
  { icon: Workflow, label: 'Design a workflow', href: '/workflows/designer', description: 'Approval chains + signature steps' },
] as const

function QuickActions() {
  return (
    <section aria-labelledby="quick-actions-heading">
      <h2 id="quick-actions-heading" className="mb-3 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        Quick actions
      </h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {QUICK_ACTIONS.map(({ icon: Icon, label, href, description, kbd }) => (
          <Link
            key={label}
            to={href}
            className="group flex items-start gap-3 rounded-lg border border-border bg-card p-4 transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-background text-foreground transition-colors group-hover:bg-foreground group-hover:text-background">
              <Icon className="h-[1.1rem] w-[1.1rem]" />
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex items-center justify-between gap-2">
                <p className="text-sm font-medium">{label}</p>
                {kbd && (
                  <kbd className="pointer-events-none inline-flex h-5 select-none items-center gap-0.5 rounded border border-border bg-muted px-1.5 font-mono text-[10px] font-medium text-muted-foreground">
                    {kbd}
                  </kbd>
                )}
              </div>
              <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
            </div>
          </Link>
        ))}
      </div>
    </section>
  )
}

// ---- Open tasks ----------------------------------------------------------

function OpenTasksCard() {
  const navigate = useNavigate()
  const { data, isLoading, isError } = useQuery({
    queryKey: ['my-tasks'],
    queryFn: () => listMyTasks(false),
    staleTime: 30_000,
  })
  const open = (data ?? []).filter((t) => t.status === 'open' || t.status === 'in_progress')

  return (
    <Card>
      <div className="flex items-center justify-between border-b border-border p-4">
        <div>
          <h2 className="text-sm font-semibold">My open tasks</h2>
          <p className="text-xs text-muted-foreground">Approval steps + to-dos assigned to you.</p>
        </div>
        <Button variant="ghost" size="sm" onClick={() => navigate({ to: '/tasks' })}>
          View all
          <ChevronRight className="ml-1 h-3.5 w-3.5" />
        </Button>
      </div>
      <div className="divide-y divide-border">
        {isLoading && (
          <div className="space-y-2 p-4">
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-4 w-1/2" />
            <Skeleton className="h-4 w-3/5" />
          </div>
        )}
        {isError && (
          <p className="p-6 text-center text-sm text-muted-foreground">Couldn't load tasks. Try refreshing.</p>
        )}
        {!isLoading && !isError && open.length === 0 && (
          <div className="flex flex-col items-center gap-2 p-8 text-center">
            <span className="flex h-10 w-10 items-center justify-center rounded-full bg-muted text-muted-foreground">
              <CheckSquare className="h-5 w-5" />
            </span>
            <p className="text-sm font-medium">Inbox zero</p>
            <p className="text-xs text-muted-foreground">No open tasks. New work will land here.</p>
          </div>
        )}
        {!isLoading && open.slice(0, 5).map((t) => <TaskRow key={t.id} task={t} />)}
      </div>
    </Card>
  )
}

function TaskRow({ task }: { task: Task }) {
  const overdue = task.due_at && new Date(task.due_at) < new Date()
  return (
    <Link
      to="/tasks"
      className="flex items-start gap-3 p-4 transition-colors hover:bg-accent/40"
    >
      <span
        className={cn(
          'mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-md',
          task.priority === 'urgent' || task.priority === 'high'
            ? 'bg-warning/15 text-warning'
            : 'bg-muted text-muted-foreground',
        )}
        aria-hidden
      >
        <CheckSquare className="h-3.5 w-3.5" />
      </span>
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{task.title}</p>
        <div className="mt-0.5 flex items-center gap-3 text-xs text-muted-foreground">
          <span className="capitalize">{task.priority}</span>
          {task.due_at && (
            <span className={cn('flex items-center gap-1', overdue && 'text-destructive')}>
              <Clock className="h-3 w-3" />
              {overdue ? 'Overdue · ' : ''}
              {relTime(task.due_at, { addSuffix: true })}
            </span>
          )}
          {task.source === 'workflow' && (
            <span className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] uppercase tracking-wide">workflow</span>
          )}
        </div>
      </div>
    </Link>
  )
}

// ---- Recent activity (notifications) -------------------------------------

interface NotificationLite {
  id: string
  title?: string
  body?: string
  created_at?: string
  read_at?: string | null
}

function RecentActivityCard() {
  const navigate = useNavigate()
  const { data, isLoading, isError } = useQuery({
    queryKey: ['notif-recent'],
    queryFn: () => getNotifications({ limit: '8' }),
    staleTime: 30_000,
  })
  const items = ((data as { items?: NotificationLite[] } | undefined)?.items ?? []).slice(0, 6)

  return (
    <Card>
      <div className="flex items-center justify-between border-b border-border p-4">
        <div>
          <h2 className="text-sm font-semibold">Recent activity</h2>
          <p className="text-xs text-muted-foreground">Latest notifications across all channels.</p>
        </div>
        <Button variant="ghost" size="sm" onClick={() => navigate({ to: '/notifications' })}>
          View all
          <ChevronRight className="ml-1 h-3.5 w-3.5" />
        </Button>
      </div>
      <div className="divide-y divide-border">
        {isLoading && (
          <div className="space-y-2 p-4">
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-4 w-1/2" />
            <Skeleton className="h-4 w-2/3" />
          </div>
        )}
        {isError && (
          <p className="p-6 text-center text-sm text-muted-foreground">Couldn't load activity. Try refreshing.</p>
        )}
        {!isLoading && !isError && items.length === 0 && (
          <div className="flex flex-col items-center gap-2 p-8 text-center">
            <span className="flex h-10 w-10 items-center justify-center rounded-full bg-muted text-muted-foreground">
              <Bell className="h-5 w-5" />
            </span>
            <p className="text-sm font-medium">No activity yet</p>
            <p className="text-xs text-muted-foreground">As work happens in your workspaces it'll show up here.</p>
          </div>
        )}
        {items.map((n) => (
          <div key={n.id} className="flex items-start gap-3 p-4">
            <span
              className={cn(
                'mt-1.5 h-2 w-2 shrink-0 rounded-full',
                n.read_at ? 'bg-muted-foreground/40' : 'bg-info',
              )}
              aria-hidden
            />
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium">{n.title ?? 'Update'}</p>
              {n.body && <p className="line-clamp-2 text-xs text-muted-foreground">{n.body}</p>}
              {n.created_at && (
                <p className="mt-1 text-[11px] text-muted-foreground">
                  {relTime(n.created_at, { addSuffix: true })}
                </p>
              )}
            </div>
          </div>
        ))}
      </div>
    </Card>
  )
}

export const Route = createFileRoute('/_authenticated/')({ component: DashboardPage })
