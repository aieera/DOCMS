import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useMemo, useState } from 'react'
import { Plus, FolderOpen, Users, FileText, Search, Calendar, X } from 'lucide-react'
import { toast } from 'sonner'

import { getWorkspaces, createWorkspace } from '@/api/workspaces'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { ViewModeToggle } from '@/components/ui/ViewModeToggle'
import { useUIStore } from '@/store/uiStore'
import { Skeleton } from '@/components/ui/Skeleton'
import { Card } from '@/components/ui/card'
import { Dialog } from '@/components/ui/Dialog'
import { EmptyState } from '@/components/ui/EmptyState'
import type { Workspace } from '@/types/api'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { cn } from '@/lib/cn'
import { WorkspaceAccessBadge } from '@/components/workspaces/WorkspaceAccessBadge'
import { formatDate } from '@/lib/formatters'

function WorkspacesPage() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ['workspaces'],
    queryFn: getWorkspaces,
    staleTime: 60_000,
  })
  const [filter, setFilter] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const viewMode = useUIStore((s) => s.workspacesViewMode)
  const setViewMode = useUIStore((s) => s.setWorkspacesViewMode)

  const filtered = useMemo(() => {
    const list = (data ?? []) as Workspace[]
    if (!filter.trim()) return list
    const q = filter.trim().toLowerCase()
    return list.filter(
      (w) => w.name.toLowerCase().includes(q) || (w.description ?? '').toLowerCase().includes(q),
    )
  }, [data, filter])

  return (
    <div className="space-y-6">
      <PageHeader
        title="Workspaces"
        description="Organize documents by team or project. Each workspace is permission-scoped."
        actions={
          <Button
            onClick={() => setCreateOpen(true)}
            data-testid="new-workspace"
            className="shadow-neu-sm transition-[box-shadow,transform] duration-150 hover:-translate-y-px hover:shadow-neu"
          >
            <Plus className="h-4 w-4" /> New workspace
          </Button>
        }
      />

      {/* Toolbar — search appears once there's anything to filter. */}
      {!isLoading && (data?.length ?? 0) > 0 && (
        <div className="flex items-center justify-between gap-3">
          <div className="relative w-full max-w-sm">
            <Search className="pointer-events-none absolute inset-y-0 start-3 my-auto h-4 w-4 text-muted-foreground" />
            <Input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter workspaces…"
              // The placeholder is not an accessible name — it disappears
              // the moment anything is typed.
              aria-label="Filter workspaces"
              className={cn('ps-9', filter && 'pe-9')}
            />
            {filter && (
              <button
                type="button"
                onClick={() => setFilter('')}
                aria-label="Clear filter"
                className="absolute inset-y-0 end-0 flex items-center pe-3 text-muted-foreground transition-colors hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            )}
          </div>
          <ViewModeToggle value={viewMode} onChange={setViewMode} className="shrink-0" />
        </div>
      )}

      {isLoading ? (
        <WorkspacesSkeleton />
      ) : isError ? (
        <EmptyState
          icon={<FolderOpen />}
          title="Couldn't load workspaces"
          description="Something went wrong reaching the document service. Try again in a moment."
        />
      ) : (data?.length ?? 0) === 0 ? (
        <EmptyState
          icon={<FolderOpen className="h-8 w-8" />}
          title="No workspaces yet"
          description="Workspaces hold the documents and folders your team collaborates on. Create one to get started."
          actionLabel="Create your first workspace"
          onAction={() => setCreateOpen(true)}
        />
      ) : filtered.length === 0 ? (
        <EmptyState
          icon={<Search />}
          title="No matches"
          description={`No workspace name or description matches "${filter}".`}
        />
      ) : viewMode === 'grid' ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {filtered.map((ws) => (
            <WorkspaceCard key={ws.id} ws={ws} />
          ))}
        </div>
      ) : (
        <div className="flex flex-col divide-y divide-border overflow-hidden rounded-2xl border border-border bg-card">
          {filtered.map((ws) => (
            <WorkspaceRow key={ws.id} ws={ws} />
          ))}
        </div>
      )}

      <CreateWorkspaceDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  )
}

// ---- Card ----------------------------------------------------------------

// Deterministic hash → palette index. djb2-lite over the workspace
// name so the same name always lands on the same accent, giving each
// workspace card a small but persistent visual identity. Pure
// presentation; the order is meaningless beyond "consistent across
// renders". 8 hues covers the realistic per-tenant workspace count
// without two cards looking identical.
// Desaturated, cream-safe accents. The previous 500-stop palette
// produced jewel-tone fills that fought the warm theme — these are
// muted enough to sit on cream without shouting and preserve dark-
// mode legibility via dark: variants.
const WORKSPACE_ACCENTS = [
  'border-s-violet-400/70 bg-violet-100/40 text-violet-700 dark:bg-violet-500/10 dark:text-violet-300',
  'border-s-sky-400/70 bg-sky-100/40 text-sky-700 dark:bg-sky-500/10 dark:text-sky-300',
  'border-s-emerald-400/70 bg-emerald-100/40 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300',
  'border-s-amber-400/70 bg-amber-100/40 text-amber-700 dark:bg-amber-500/10 dark:text-amber-300',
  'border-s-rose-400/70 bg-rose-100/40 text-rose-700 dark:bg-rose-500/10 dark:text-rose-300',
  'border-s-cyan-400/70 bg-cyan-100/40 text-cyan-700 dark:bg-cyan-500/10 dark:text-cyan-300',
  'border-s-indigo-400/70 bg-indigo-100/40 text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300',
  'border-s-teal-400/70 bg-teal-100/40 text-teal-700 dark:bg-teal-500/10 dark:text-teal-300',
] as const
function accentFor(name: string): string {
  let h = 5381
  for (let i = 0; i < name.length; i++) h = ((h << 5) + h + name.charCodeAt(i)) | 0
  return WORKSPACE_ACCENTS[Math.abs(h) % WORKSPACE_ACCENTS.length]
}

// Backend filters /workspaces server-side: tenant owner/admin sees
// every active workspace; everyone else sees only ones they created
// or are members of (see services/document/internal/repository/
// workspace_repo.go). The frontend just renders whatever lands — no
// locked-card or "No access" branching needed.
function WorkspaceCard({ ws }: { ws: Workspace }) {
  const accent = accentFor(ws.name)
  return (
    <Link
      to="/workspaces/$workspaceId"
      params={{ workspaceId: ws.id }}
      className="block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background rounded-2xl"
    >
      <Card className={cn('group h-full border-s-[3px] rounded-2xl p-5 transition-all motion-reduce:transition-none hover:-translate-y-0.5 hover:border-foreground/20', accent.split(' ')[0])}>
        <div className="flex items-start justify-between gap-2">
          <span className={cn('flex h-10 w-10 items-center justify-center rounded-lg', accent.split(' ').slice(1).join(' '))}>
            <FolderOpen className="h-5 w-5" />
          </span>
          {/* Top-right is the card's highest-scan position and was carrying
              only a chevron — the access state earns it. */}
          <span className="flex items-center gap-2">
            <WorkspaceAccessBadge
              sharedWithCount={ws.shared_with_count}
              memberCount={ws.member_count}
            />
            <DirectionalIcon name="ChevronRight" className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
          </span>
        </div>
        {/* QA SD-17: h2, not h3 — the page's h1 is the PageHeader and
            nothing sits between, so h3 skipped a level. */}
        <h2 className="mt-4 truncate text-base font-semibold tracking-tight">{ws.name}</h2>
        <p className="mt-1 line-clamp-2 min-h-[2.5rem] text-sm">
          {ws.description?.trim()
            ? <span className="text-muted-foreground">{ws.description}</span>
            : <span className="italic text-muted-foreground/60">No description</span>}
        </p>
        <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-border pt-3 text-xs text-muted-foreground">
          <span className="flex items-center gap-1">
            <FileText className="h-3.5 w-3.5" />
            {Number(ws.document_count).toLocaleString()} {Number(ws.document_count) === 1 ? 'doc' : 'docs'}
          </span>
          <span className="flex items-center gap-1">
            <Users className="h-3.5 w-3.5" />
            {Number(ws.member_count ?? 0).toLocaleString()} {Number(ws.member_count ?? 0) === 1 ? 'member' : 'members'}
          </span>
          <span className="ms-auto flex items-center gap-1">
            <Calendar className="h-3.5 w-3.5" />
            {formatDate(ws.updated_at ?? ws.created_at)}
          </span>
        </div>
      </Card>
    </Link>
  )
}

// Compact list-mode counterpart of WorkspaceCard — same accent, same
// meta, one row per workspace.
function WorkspaceRow({ ws }: { ws: Workspace }) {
  const accent = accentFor(ws.name)
  return (
    <Link
      to="/workspaces/$workspaceId"
      params={{ workspaceId: ws.id }}
      data-testid={`workspace-row-${ws.id}`}
      className="group flex items-center gap-4 px-4 py-3 transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:bg-muted/40 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
    >
      <span className={cn('flex h-9 w-9 shrink-0 items-center justify-center rounded-lg', accent.split(' ').slice(1).join(' '))}>
        <FolderOpen className="h-[18px] w-[18px]" />
      </span>
      <span className="min-w-0 flex-1">
        {/* Beside the name, not in the meta group: that group is hidden
            below sm, and access state must survive to mobile. */}
        <span className="flex min-w-0 items-center gap-2">
          <span className="truncate text-sm font-semibold tracking-tight">{ws.name}</span>
          <WorkspaceAccessBadge
            sharedWithCount={ws.shared_with_count}
            memberCount={ws.member_count}
            className="shrink-0"
          />
        </span>
        <span className="block truncate text-xs text-muted-foreground">
          {ws.description?.trim() || <span className="italic text-muted-foreground/60">No description</span>}
        </span>
      </span>
      <span className="hidden shrink-0 items-center gap-4 text-xs text-muted-foreground sm:flex">
        <span className="flex items-center gap-1 tabular-nums">
          <FileText className="h-3.5 w-3.5" />
          {Number(ws.document_count).toLocaleString()} {Number(ws.document_count) === 1 ? 'doc' : 'docs'}
        </span>
        <span className="flex items-center gap-1 tabular-nums">
          <Users className="h-3.5 w-3.5" />
          {Number(ws.member_count ?? 0).toLocaleString()} {Number(ws.member_count ?? 0) === 1 ? 'member' : 'members'}
        </span>
        <span className="flex w-20 items-center justify-end gap-1 tabular-nums">
          <Calendar className="h-3.5 w-3.5" />
          {formatDate(ws.updated_at ?? ws.created_at)}
        </span>
      </span>
      <DirectionalIcon name="ChevronRight" className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
    </Link>
  )
}

function WorkspacesSkeleton() {
  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {Array.from({ length: 6 }).map((_, i) => (
        <Card key={i} className="space-y-4 p-5">
          <Skeleton className="h-10 w-10 rounded-lg" />
          <Skeleton className="h-5 w-2/3" />
          <Skeleton className="h-4 w-full" />
          <div className="flex gap-3 border-t border-border pt-3">
            <Skeleton className="h-3 w-12" />
            <Skeleton className="h-3 w-16" />
          </div>
        </Card>
      ))}
    </div>
  )
}

// ---- Create dialog -------------------------------------------------------

function CreateWorkspaceDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [touched, setTouched] = useState(false)
  const trimmed = name.trim()

  const mut = useAppMutation({
    mutationFn: () => createWorkspace(name.trim(), description.trim() || undefined),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspaces'] })
      toast.success(`Created "${name.trim()}"`)
      setName(''); setDescription('')
      onOpenChange(false)
    },
    onError: (e: unknown) => {
      const m = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? 'Could not create workspace')
    },
  })

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="New workspace"
      description="A workspace is a permission scope for a team or project."
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          setTouched(true)
          if (!trimmed) return
          mut.mutate()
        }}
        className="space-y-4"
      >
        <Input
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Q4 Contracts"
          // Validated inline rather than by a toast fired from the far
          // corner of the screen, and the submit stays disabled while the
          // name is empty — matching the New-folder dialog.
          onBlur={() => setTouched(true)}
          error={touched && !trimmed ? 'Enter a name.' : undefined}
          autoFocus
          maxLength={120}
        />
        <Input
          label="Description (optional)"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="What lives here?"
          maxLength={280}
        />
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="ghost" onClick={() => onOpenChange(false)} disabled={mut.isPending}>
            Cancel
          </Button>
          <Button type="submit" loading={mut.isPending} disabled={mut.isPending || !trimmed}>
            Create workspace
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/')({ component: WorkspacesPage })
