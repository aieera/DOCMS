import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { Plus, FolderOpen, Users, FileText, Search, Calendar, Lock } from 'lucide-react'
import { toast } from 'sonner'

import { getWorkspaces, createWorkspace } from '@/api/workspaces'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Skeleton } from '@/components/ui/Skeleton'
import { Card } from '@/components/ui/card'
import { Dialog } from '@/components/ui/Dialog'
import { EmptyState } from '@/components/ui/EmptyState'
import type { Workspace } from '@/types/api'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { useAuthStore } from '@/store/authStore'

function WorkspacesPage() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ['workspaces'],
    queryFn: getWorkspaces,
    staleTime: 60_000,
  })
  const [filter, setFilter] = useState('')
  const [createOpen, setCreateOpen] = useState(false)

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
          <Button onClick={() => setCreateOpen(true)} data-testid="new-workspace">
            <Plus className="h-4 w-4" /> New workspace
          </Button>
        }
      />

      {/* Toolbar — search appears once there's anything to filter. */}
      {!isLoading && (data?.length ?? 0) > 0 && (
        <div className="flex items-center gap-3">
          <div className="relative max-w-xs flex-1">
            <Search className="pointer-events-none absolute inset-y-0 start-3 my-auto h-4 w-4 text-muted-foreground" />
            <Input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter workspaces…"
              className="ps-9"
            />
          </div>
          <span className="text-xs text-muted-foreground">
            {filtered.length} of {data?.length ?? 0}
          </span>
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
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {filtered.map((ws) => (
            <WorkspaceCard key={ws.id} ws={ws} />
          ))}
        </div>
      )}

      <CreateWorkspaceDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  )
}

// ---- Card ----------------------------------------------------------------

function WorkspaceCard({ ws }: { ws: Workspace }) {
  // Backend's GET /workspaces returns every workspace in the tenant
  // (discovery mode) — even ones the caller can't open. Clicking into
  // one you don't belong to surfaces "Access denied" via the inner
  // /documents call, which is a confusing UX. Until the backend ships
  // an `is_member` boolean on each workspace row, approximate access
  // from the two signals we already have:
  //   1) tenant-level admin/owner role  → gateway grants them access
  //      to every workspace in the tenant
  //   2) the caller created this workspace → creator always has access
  // The `member_count` field is NOT a usable signal — it tells us
  // someone is a member, not whether *you* are. Without per-row
  // membership data this heuristic is conservative: it correctly
  // labels workspaces you can't open AND it may label some you CAN
  // open (you were added as a member of someone else's workspace).
  // Replace the heuristic with `ws.is_member` once that field lands.
  const me = useAuthStore((s) => s.user)
  const isAdmin = me?.role === 'owner' || me?.role === 'admin'
  const isCreator = !!me?.id && me.id === ws.created_by
  const likelyHasAccess = isAdmin || isCreator

  const body = (
    <Card
      className={
        likelyHasAccess
          ? 'group h-full p-5 transition-all hover:border-foreground/20 hover:shadow-md'
          : 'h-full p-5 opacity-60'
      }
    >
      <div className="flex items-start justify-between">
        <span className="flex h-10 w-10 items-center justify-center rounded-lg bg-muted text-foreground">
          <FolderOpen className="h-5 w-5" />
        </span>
        {likelyHasAccess ? (
          <DirectionalIcon name="ChevronRight" className="h-4 w-4 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
        ) : (
          <Lock className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        )}
      </div>
      <h3 className="mt-4 truncate text-base font-semibold tracking-tight">{ws.name}</h3>
      <p className="mt-1 line-clamp-2 min-h-[2.5rem] text-sm text-muted-foreground">
        {ws.description ?? 'No description.'}
      </p>
      <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-border pt-3 text-xs text-muted-foreground">
        <span className="flex items-center gap-1">
          <FileText className="h-3.5 w-3.5" />
          {ws.document_count.toLocaleString()} {ws.document_count === 1 ? 'doc' : 'docs'}
        </span>
        <span className="flex items-center gap-1">
          <Users className="h-3.5 w-3.5" />
          {ws.member_count ?? 0} {(ws.member_count ?? 0) === 1 ? 'member' : 'members'}
        </span>
        {likelyHasAccess ? (
          <span className="ms-auto flex items-center gap-1">
            <Calendar className="h-3.5 w-3.5" />
            {new Date(ws.updated_at ?? ws.created_at).toLocaleDateString()}
          </span>
        ) : (
          <span className="ms-auto flex items-center gap-1 text-muted-foreground">
            <Lock className="h-3 w-3" /> No access
          </span>
        )}
      </div>
    </Card>
  )

  // Locked cards become a plain (non-link) container with a helpful
  // tooltip. Letting the user click into a 403-toast loop is worse UX
  // than disabling the click entirely.
  if (!likelyHasAccess) {
    return (
      <div
        className="rounded-lg"
        title="You don't have document access to this workspace. Ask the workspace creator or a tenant admin to add you."
        data-testid={`workspace-card-locked-${ws.id}`}
      >
        {body}
      </div>
    )
  }

  return (
    <Link
      to="/workspaces/$workspaceId"
      params={{ workspaceId: ws.id }}
      className="block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background rounded-lg"
    >
      {body}
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

  const mut = useMutation({
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
        onSubmit={(e) => { e.preventDefault(); if (name.trim()) mut.mutate() }}
        className="space-y-4"
      >
        <Input
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Q4 Contracts"
          required
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
          <Button type="submit" loading={mut.isPending} disabled={!name.trim()}>
            Create workspace
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/')({ component: WorkspacesPage })
