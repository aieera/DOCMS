import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { ChevronRight, Plus, Trash2, UserMinus, UserPlus, Users } from 'lucide-react'

import {
  addGroupMember,
  createGroup,
  deleteGroup,
  getGroup,
  listGroups,
  removeGroupMember,
  updateGroup,
} from '@/api/groups'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/Skeleton'
import { Avatar } from '@/components/ui/Avatar'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { formatRelativeTime } from '@/lib/formatters'
import { cn } from '@/lib/cn'

function GroupsPage() {
  const qc = useQueryClient()
  const { data: groups, isLoading } = useQuery({ queryKey: ['admin', 'groups'], queryFn: listGroups })

  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [newName, setNewName] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [memberInput, setMemberInput] = useState('')

  const detail = useQuery({
    queryKey: ['admin', 'groups', selectedId],
    queryFn: () => (selectedId ? getGroup(selectedId) : Promise.resolve(null)),
    enabled: !!selectedId,
  })

  const create = useMutation({
    mutationFn: () => createGroup({ name: newName.trim(), description: newDesc.trim() || undefined }),
    onSuccess: (g) => {
      toast.success(`Created "${g.name}"`)
      setNewName(''); setNewDesc(''); setCreateOpen(false)
      setSelectedId(g.id)
      qc.invalidateQueries({ queryKey: ['admin', 'groups'] })
    },
    onError: () => toast.error('Create failed'),
  })

  const remove = useMutation({
    mutationFn: (id: string) => deleteGroup(id),
    onSuccess: () => {
      toast.success('Group deleted')
      setSelectedId(null); setConfirmDelete(false)
      qc.invalidateQueries({ queryKey: ['admin', 'groups'] })
    },
  })

  const rename = useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) => updateGroup(id, { name }),
    onSuccess: (_d, v) => {
      toast.success('Renamed')
      qc.invalidateQueries({ queryKey: ['admin', 'groups'] })
      qc.invalidateQueries({ queryKey: ['admin', 'groups', v.id] })
    },
  })

  const addMember = useMutation({
    mutationFn: ({ id, userId }: { id: string; userId: string }) => addGroupMember(id, userId),
    onSuccess: (_d, v) => {
      toast.success('Member added')
      setMemberInput('')
      qc.invalidateQueries({ queryKey: ['admin', 'groups', v.id] })
      qc.invalidateQueries({ queryKey: ['admin', 'groups'] })
    },
    onError: () => toast.error('Add failed — check the user ID'),
  })

  const removeMember = useMutation({
    mutationFn: ({ id, userId }: { id: string; userId: string }) => removeGroupMember(id, userId),
    onSuccess: (_d, v) => {
      toast.success('Member removed')
      qc.invalidateQueries({ queryKey: ['admin', 'groups', v.id] })
      qc.invalidateQueries({ queryKey: ['admin', 'groups'] })
    },
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title="Groups"
        description="Bundle members into reusable permission scopes. Groups can be granted access on workspaces, folders, and individual documents."
        actions={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" /> New group
          </Button>
        }
      />

      {isLoading ? (
        <div className="grid gap-4 lg:grid-cols-[280px_minmax(0,1fr)]">
          <Skeleton className="h-72" />
          <Skeleton className="h-72" />
        </div>
      ) : !groups || groups.length === 0 ? (
        <EmptyState
          icon={<Users className="h-6 w-6" />}
          title="No groups yet"
          description="Create a group to organize team members and scope permissions."
          actionLabel="Create your first group"
          onAction={() => setCreateOpen(true)}
        />
      ) : (
        <div className="grid gap-4 lg:grid-cols-[280px_minmax(0,1fr)]">
          {/* Group list — left rail */}
          <Card className="overflow-hidden p-0">
            <ul className="divide-y divide-border">
              {groups.map((g) => {
                const active = selectedId === g.id
                return (
                  <li key={g.id}>
                    <button
                      type="button"
                      onClick={() => setSelectedId(g.id)}
                      className={cn(
                        'flex w-full items-center gap-2 px-3 py-2.5 text-left transition-colors',
                        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                        active ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/50',
                      )}
                    >
                      <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-muted text-foreground">
                        <Users className="h-3.5 w-3.5" />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-medium">{g.name}</span>
                        <span className="block text-xs text-muted-foreground">
                          {g.member_count} member{g.member_count === 1 ? '' : 's'} · {formatRelativeTime(g.created_at)}
                        </span>
                      </span>
                      <ChevronRight className={cn('h-4 w-4 text-muted-foreground transition-opacity', active ? 'opacity-100' : 'opacity-30')} />
                    </button>
                  </li>
                )
              })}
            </ul>
          </Card>

          {/* Detail pane — right rail */}
          {selectedId && detail.data ? (
            <Card className="space-y-4 p-5">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1">
                  <input
                    className="w-full rounded-md bg-transparent px-1 -mx-1 text-lg font-semibold tracking-tight focus:outline-none focus:ring-2 focus:ring-ring"
                    defaultValue={detail.data.name}
                    onBlur={(e) => {
                      const v = e.target.value.trim()
                      if (v && v !== detail.data!.name) rename.mutate({ id: selectedId, name: v })
                    }}
                  />
                  {detail.data.description && (
                    <p className="mt-0.5 text-sm text-muted-foreground">{detail.data.description}</p>
                  )}
                </div>
                <Button variant="outline" size="sm" onClick={() => setConfirmDelete(true)}>
                  <Trash2 className="h-4 w-4" /> Delete
                </Button>
              </div>

              <div className="border-t border-border pt-4">
                <h4 className="mb-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                  Members
                </h4>
                <div className="mb-3 flex gap-2">
                  <Input
                    className="flex-1"
                    placeholder="User UUID to add…"
                    value={memberInput}
                    onChange={(e) => setMemberInput(e.target.value)}
                  />
                  <Button
                    onClick={() => addMember.mutate({ id: selectedId, userId: memberInput.trim() })}
                    disabled={!memberInput.trim() || addMember.isPending}
                    loading={addMember.isPending}
                  >
                    <UserPlus className="h-4 w-4" /> Add
                  </Button>
                </div>

                {(detail.data.members?.length ?? 0) === 0 ? (
                  <p className="rounded-md border border-dashed border-border p-4 text-center text-sm text-muted-foreground">
                    No members yet. Paste a user UUID above to add the first one.
                  </p>
                ) : (
                  <ul className="space-y-1.5">
                    {(detail.data.members ?? []).map((m) => (
                      <li
                        key={m.user_id}
                        className="flex items-center justify-between rounded-md border border-border bg-muted/40 px-3 py-2"
                      >
                        <div className="flex min-w-0 items-center gap-2">
                          <Avatar name={m.display_name || m.email} size="sm" />
                          <div className="min-w-0">
                            <div className="truncate text-sm font-medium">{m.display_name || m.email}</div>
                            <div className="truncate text-xs text-muted-foreground">{m.email}</div>
                          </div>
                        </div>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => removeMember.mutate({ id: selectedId, userId: m.user_id })}
                          disabled={removeMember.isPending}
                          aria-label="Remove member"
                        >
                          <UserMinus className="h-4 w-4" />
                        </Button>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </Card>
          ) : detail.isLoading ? (
            <Card className="space-y-4 p-5">
              <Skeleton className="h-7 w-1/3" />
              <Skeleton className="h-4 w-2/3" />
              <Skeleton className="h-32 w-full" />
            </Card>
          ) : (
            <Card className="flex h-full items-center justify-center border-dashed p-8 text-center text-sm text-muted-foreground">
              Select a group to see its members.
            </Card>
          )}
        </div>
      )}

      {/* Create group dialog uses the existing ConfirmDialog pattern
          isn't a fit here — we need a real form, so an inline-rendered
          Dialog (like /admin/users) would be cleaner. Keeping it
          inline-form here for now to avoid blowing up the page count;
          refactor in a follow-up. */}
      {createOpen && (
        <CreateGroupForm
          name={newName} setName={setNewName}
          desc={newDesc} setDesc={setNewDesc}
          isPending={create.isPending}
          onCancel={() => { setCreateOpen(false); setNewName(''); setNewDesc('') }}
          onSubmit={() => create.mutate()}
        />
      )}

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Delete group"
        description={`This removes "${detail.data?.name}" and unscopes any permissions granted to it. Members keep their direct grants.`}
        confirmLabel="Delete"
        destructive
        onConfirm={() => selectedId && remove.mutate(selectedId)}
      />
    </div>
  )
}

function CreateGroupForm({
  name, setName, desc, setDesc, isPending, onCancel, onSubmit,
}: {
  name: string; setName: (v: string) => void
  desc: string; setDesc: (v: string) => void
  isPending: boolean
  onCancel: () => void
  onSubmit: () => void
}) {
  // Renders inline above the list as a Card form panel — simpler than
  // a Dialog given users want to see the new group land in the rail.
  return (
    <Card className="space-y-3 p-4">
      <h3 className="flex items-center gap-2 text-sm font-semibold">
        <Plus className="h-4 w-4" /> New group
      </h3>
      <form
        onSubmit={(e) => { e.preventDefault(); if (name.trim()) onSubmit() }}
        className="grid gap-2 sm:grid-cols-[1fr_2fr_auto]"
      >
        <Input placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        <Input placeholder="Description (optional)" value={desc} onChange={(e) => setDesc(e.target.value)} />
        <div className="flex gap-2">
          <Button type="button" variant="ghost" onClick={onCancel} disabled={isPending}>Cancel</Button>
          <Button type="submit" loading={isPending} disabled={!name.trim()}>Create</Button>
        </div>
      </form>
    </Card>
  )
}

export const Route = createFileRoute('/_authenticated/admin/groups')({ component: GroupsPage })
