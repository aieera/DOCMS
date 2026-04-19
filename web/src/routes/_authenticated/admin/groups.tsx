import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Users, Plus, Trash2, UserMinus, UserPlus } from 'lucide-react'

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
import { Skeleton } from '@/components/ui/Skeleton'
import { formatRelativeTime } from '@/lib/formatters'

function GroupsPage() {
  const qc = useQueryClient()
  const { data: groups, isLoading } = useQuery({ queryKey: ['admin', 'groups'], queryFn: listGroups })

  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [newName, setNewName] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [memberInput, setMemberInput] = useState('')

  const detail = useQuery({
    queryKey: ['admin', 'groups', selectedId],
    queryFn: () => (selectedId ? getGroup(selectedId) : Promise.resolve(null)),
    enabled: !!selectedId,
  })

  const create = useMutation({
    mutationFn: () => createGroup({ name: newName, description: newDesc || undefined }),
    onSuccess: (g) => {
      toast.success('Group created')
      setNewName('')
      setNewDesc('')
      setSelectedId(g.id)
      qc.invalidateQueries({ queryKey: ['admin', 'groups'] })
    },
    onError: () => toast.error('Create failed'),
  })

  const remove = useMutation({
    mutationFn: (id: string) => deleteGroup(id),
    onSuccess: () => {
      toast.success('Group deleted')
      setSelectedId(null)
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
    onError: () => toast.error('Add failed'),
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
    <div>
      <PageHeader title="Groups" description="Manage team groups and member assignment" />

      <div className="mb-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
        <h3 className="mb-3 flex items-center gap-2 font-medium">
          <Plus className="h-4 w-4" /> New group
        </h3>
        <div className="grid grid-cols-[1fr_2fr_auto] items-center gap-2">
          <input
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
            placeholder="Name"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
          />
          <input
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
            placeholder="Description (optional)"
            value={newDesc}
            onChange={(e) => setNewDesc(e.target.value)}
          />
          <Button onClick={() => create.mutate()} disabled={!newName || create.isPending}>
            Create
          </Button>
        </div>
      </div>

      {isLoading ? (
        <Skeleton className="h-40" />
      ) : !groups || groups.length === 0 ? (
        <EmptyState
          icon={<Users className="h-12 w-12" />}
          title="No groups yet"
          description="Create a group above to organize team members and scope permissions."
        />
      ) : (
        <div className="grid grid-cols-[280px_1fr] gap-4">
          <ul className="space-y-1">
            {groups.map((g) => (
              <li key={g.id}>
                <button
                  onClick={() => setSelectedId(g.id)}
                  className={`w-full rounded-md px-3 py-2 text-left text-sm ${
                    selectedId === g.id
                      ? 'bg-[var(--color-accent)] text-[var(--color-primary)]'
                      : 'hover:bg-[var(--color-bg-secondary)]'
                  }`}
                >
                  <div className="font-medium">{g.name}</div>
                  <div className="text-xs text-[var(--color-text-secondary)]">
                    {g.member_count} member{g.member_count === 1 ? '' : 's'} · created{' '}
                    {formatRelativeTime(g.created_at)}
                  </div>
                </button>
              </li>
            ))}
          </ul>

          <div>
            {selectedId && detail.data ? (
              <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
                <div className="mb-3 flex items-start justify-between">
                  <div>
                    <input
                      className="text-lg font-semibold bg-transparent focus:outline-none focus:ring-1 focus:ring-[var(--color-primary)] rounded px-1"
                      defaultValue={detail.data.name}
                      onBlur={(e) => {
                        const v = e.target.value.trim()
                        if (v && v !== detail.data!.name) rename.mutate({ id: selectedId, name: v })
                      }}
                    />
                    {detail.data.description && (
                      <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
                        {detail.data.description}
                      </p>
                    )}
                  </div>
                  <Button
                    onClick={() => {
                      if (window.confirm(`Delete group "${detail.data!.name}"?`)) remove.mutate(selectedId)
                    }}
                  >
                    <Trash2 className="h-4 w-4" /> Delete
                  </Button>
                </div>

                <h4 className="mb-2 text-sm font-medium">Members</h4>
                <div className="mb-3 flex gap-2">
                  <input
                    className="flex-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
                    placeholder="User UUID"
                    value={memberInput}
                    onChange={(e) => setMemberInput(e.target.value)}
                  />
                  <Button
                    onClick={() => addMember.mutate({ id: selectedId, userId: memberInput })}
                    disabled={!memberInput || addMember.isPending}
                  >
                    <UserPlus className="h-4 w-4" /> Add
                  </Button>
                </div>

                {detail.data.members.length === 0 ? (
                  <p className="text-sm text-[var(--color-text-secondary)]">No members yet.</p>
                ) : (
                  <ul className="space-y-1">
                    {detail.data.members.map((m) => (
                      <li
                        key={m.user_id}
                        className="flex items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2"
                      >
                        <div>
                          <div className="text-sm font-medium">{m.display_name || m.email}</div>
                          <div className="text-xs text-[var(--color-text-secondary)]">{m.email}</div>
                        </div>
                        <Button
                          onClick={() =>
                            removeMember.mutate({ id: selectedId, userId: m.user_id })
                          }
                          disabled={removeMember.isPending}
                        >
                          <UserMinus className="h-4 w-4" />
                        </Button>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            ) : (
              <div className="flex h-full items-center justify-center rounded-lg border border-dashed border-[var(--color-border)] p-8 text-sm text-[var(--color-text-secondary)]">
                Select a group to see members.
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/groups')({ component: GroupsPage })
