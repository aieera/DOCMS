import { useEffect, useMemo, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Settings as SettingsIcon, HardDrive, Users, ShieldAlert, UserCog, UserPlus, UserMinus, Search, Sparkles } from 'lucide-react'
import { toast } from 'sonner'

import { getWorkspace } from '@/api/workspaces'
import { getUsers } from '@/api/admin'
import { listUserDirectory } from '@/api/auth'
import {
  useUpdateWorkspace, useDeleteWorkspace, useTransferWorkspaceOwnership,
  useWorkspaceMembers, useAddWorkspaceMember, useUpdateWorkspaceMemberRole,
  useRemoveWorkspaceMember,
} from '@/hooks/useWorkspaces'
import { useCurrentUser } from '@/hooks/useAuth'
import { readErrorMessage } from '@/api/client'

import { PageHeader } from '@/components/shared/PageHeader'
import { Skeleton } from '@/components/ui/Skeleton'
import { Avatar } from '@/components/ui/shadcn/avatar'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { TypedConfirmDialog } from '@/components/ui/shadcn/typed-confirm-dialog'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { WorkspaceAISettingsSection } from '@/components/intelligence/WorkspaceAISettings'

// Settings route — sectioned page for a single workspace.
//
// Role gating:
//   * tenant role=owner → can do everything (transfer, delete).
//   * caller.id === workspace.created_by → can transfer + delete its own.
//   * tenant role=admin → can edit details (uses admin capability on the
//     workspace via UpdateWorkspace's policy check; no transfer/delete UI).
//   * anyone else → read-only details, no danger zone.

function SettingsPage() {
  const { workspaceId } = Route.useParams()
  const navigate = useNavigate()
  const wsQ = useQuery({
    queryKey: ['workspace', workspaceId],
    queryFn: () => getWorkspace(workspaceId),
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title={
          wsQ.isLoading ? <Skeleton className="h-7 w-48" /> : (
            <span className="flex items-center gap-2">
              <SettingsIcon className="h-5 w-5 text-muted-foreground" />
              {wsQ.data?.name ?? 'Workspace'} settings
            </span>
          )
        }
        description="Manage details, members, AI, storage, ownership, and danger zone for this workspace."
        actions={
          <Button
            variant="ghost"
            onClick={() => navigate({ to: '/workspaces/$workspaceId', params: { workspaceId } })}
          >
            Back to workspace
          </Button>
        }
      />
      <WorkspaceSettingsSections
        workspaceId={workspaceId}
        onDeleted={() => navigate({ to: '/workspaces' })}
      />
    </div>
  )
}

// Shared settings body — rendered both by this page route and by the
// WorkspaceSettingsDialog modal (browser header → Settings). Owns the
// role gating and the section composition, including the AI section.
export function WorkspaceSettingsSections({
  workspaceId, onDeleted,
}: {
  workspaceId: string
  onDeleted: () => void
}) {
  const user = useCurrentUser()
  const wsQ = useQuery({
    queryKey: ['workspace', workspaceId],
    queryFn: () => getWorkspace(workspaceId),
  })

  const role = user?.role
  const isTenantOwner = role === 'owner'
  const isTenantAdmin = role === 'owner' || role === 'admin'
  const isWorkspaceCreator = !!user?.id && !!wsQ.data?.created_by && user.id === wsQ.data.created_by
  const canEditDetails = isTenantAdmin || isWorkspaceCreator
  const canTransfer = isTenantOwner || isWorkspaceCreator
  const canDelete = isTenantOwner || isWorkspaceCreator

  if (wsQ.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-40" />
        <Skeleton className="h-40" />
      </div>
    )
  }
  if (wsQ.isError || !wsQ.data) {
    return (
      <p className="rounded-md border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
        Could not load workspace.
      </p>
    )
  }

  return (
    <div className="space-y-6">
      <DetailsSection
        workspaceId={workspaceId}
        initialName={wsQ.data.name}
        initialDescription={wsQ.data.description ?? ''}
        canEdit={canEditDetails}
      />
      <MembersSection
        workspaceId={workspaceId}
        workspaceCreatedBy={wsQ.data.created_by ?? ''}
        canManage={canEditDetails}
      />
      {isTenantAdmin && <AISection workspaceId={workspaceId} />}
      <StorageSection />
      {canTransfer && (
        <TransferOwnershipSection
          workspaceId={workspaceId}
          currentOwnerId={wsQ.data.created_by ?? ''}
        />
      )}
      {canDelete && (
        <DangerZoneSection
          workspaceId={workspaceId}
          workspaceName={wsQ.data.name}
          documentCount={wsQ.data.document_count ?? 0}
          onDeleted={onDeleted}
        />
      )}
    </div>
  )
}

function AISection({ workspaceId }: { workspaceId: string }) {
  return (
    <SectionCard
      icon={<Sparkles className="h-4 w-4" />}
      title="AI (Ask)"
      description="Pick the answer model and per-user limits for the Ask page in this workspace."
    >
      <WorkspaceAISettingsSection workspaceId={workspaceId} />
    </SectionCard>
  )
}

// ---- Sections --------------------------------------------------------------

function SectionCard({
  icon, title, description, children, danger,
}: {
  icon: React.ReactNode
  title: string
  description?: string
  children: React.ReactNode
  danger?: boolean
}) {
  return (
    <section
      className={[
        'rounded-lg border bg-card p-5',
        danger ? 'border-destructive/40' : 'border-border',
      ].join(' ')}
    >
      <header className="mb-4">
        <h2 className={`flex items-center gap-2 text-base font-semibold ${danger ? 'text-destructive' : ''}`}>
          {icon}
          {title}
        </h2>
        {description && (
          <p className="mt-1 text-sm text-muted-foreground">{description}</p>
        )}
      </header>
      {children}
    </section>
  )
}

function DetailsSection({
  workspaceId, initialName, initialDescription, canEdit,
}: {
  workspaceId: string
  initialName: string
  initialDescription: string
  canEdit: boolean
}) {
  const [name, setName] = useState(initialName)
  const [description, setDescription] = useState(initialDescription)
  const update = useUpdateWorkspace()

  useEffect(() => { setName(initialName) }, [initialName])
  useEffect(() => { setDescription(initialDescription) }, [initialDescription])

  const trimmedName = name.trim()
  const dirty =
    (trimmedName !== initialName && trimmedName.length > 0) ||
    description !== initialDescription

  return (
    <SectionCard
      icon={<SettingsIcon className="h-4 w-4" />}
      title="Details"
      description="Workspace name appears in the sidebar, breadcrumbs, and audit log."
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (!canEdit || update.isPending) return
          if (!trimmedName) { toast.error('Name is required'); return }
          if (!dirty) return
          update.mutate(
            { id: workspaceId, input: { name: trimmedName, description } },
            { onSuccess: () => toast.success('Workspace updated') },
          )
        }}
        className="space-y-4"
      >
        <Input
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={!canEdit}
          data-testid="ws-settings-name"
        />
        <div>
          <label className="mb-1 block text-sm font-medium">Description</label>
          <textarea
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            disabled={!canEdit}
            rows={3}
            className="w-full rounded-md border border-input bg-muted p-2 text-sm shadow-neu-inset disabled:opacity-50"
            data-testid="ws-settings-description"
          />
        </div>
        {canEdit ? (
          <div className="flex justify-end">
            <Button
              type="submit"
              disabled={update.isPending}
              loading={update.isPending}
              data-testid="ws-settings-save"
            >
              Save changes
            </Button>
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">
            You need workspace admin or tenant admin privileges to edit these details.
          </p>
        )}
      </form>
    </SectionCard>
  )
}

function MembersSection({
  workspaceId,
  workspaceCreatedBy,
  canManage,
}: {
  workspaceId: string
  workspaceCreatedBy: string
  canManage: boolean
}) {
  const membersQ = useWorkspaceMembers(workspaceId)
  const add = useAddWorkspaceMember()
  const updateRole = useUpdateWorkspaceMemberRole()
  const remove = useRemoveWorkspaceMember()
  const [pendingRemoveId, setPendingRemoveId] = useState<string | null>(null)

  const memberIds = useMemo(
    () => new Set((membersQ.data ?? []).map((m) => m.user_id)),
    [membersQ.data],
  )
  const pendingRemove = (membersQ.data ?? []).find((m) => m.user_id === pendingRemoveId) ?? null

  return (
    <SectionCard
      icon={<Users className="h-4 w-4" />}
      title="Members"
      description="Add or remove people, change their workspace role."
    >
      {canManage && (
        <div className="mb-4">
          <MemberPicker
            excludeIds={memberIds}
            isPending={add.isPending}
            onPick={(userId) =>
              add.mutate(
                { workspaceId, userId, role: 'member' },
                { onSuccess: () => toast.success('Member added') },
              )
            }
          />
        </div>
      )}

      <h4 className="mb-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        Members ({membersQ.data?.length ?? 0})
      </h4>

      {membersQ.isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : membersQ.isError ? (
        <p className="rounded-md border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
          {readErrorMessage(membersQ.error) ?? 'Could not load members.'}
        </p>
      ) : (membersQ.data?.length ?? 0) === 0 ? (
        <p className="rounded-md border border-dashed border-border bg-muted/30 p-4 text-center text-sm text-muted-foreground">
          {canManage
            ? 'No members yet. Use the search above to add someone.'
            : 'No members yet.'}
        </p>
      ) : (
        <ul className="space-y-1.5" data-testid="workspace-members-list">
          {(membersQ.data ?? []).map((m) => {
            const isCreator = m.user_id === workspaceCreatedBy
            return (
              <li
                key={m.user_id}
                className="flex items-center justify-between gap-3 rounded-md border border-border bg-card p-2"
              >
                <div className="flex min-w-0 items-center gap-2">
                  <Avatar name={m.display_name || m.email} size="sm" />
                  <div className="min-w-0">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-sm font-medium">
                        {m.display_name || m.email}
                      </span>
                      {isCreator && (
                        <span className="rounded-full bg-primary/10 px-1.5 py-0 text-[10px] font-medium uppercase text-primary">
                          Owner
                        </span>
                      )}
                    </div>
                    <div className="truncate text-xs text-muted-foreground">{m.email}</div>
                  </div>
                </div>
                <div className="flex items-center gap-1.5">
                  {canManage ? (
                    <Select
                      label=""
                      value={m.role}
                      onValueChange={(v) =>
                        updateRole.mutate(
                          { workspaceId, userId: m.user_id, role: v as 'admin' | 'member' | 'viewer' },
                          { onSuccess: () => toast.success('Role updated') },
                        )
                      }
                      options={[
                        { value: 'admin', label: 'Admin' },
                        { value: 'member', label: 'Member' },
                        { value: 'viewer', label: 'Viewer' },
                      ]}
                      disabled={isCreator || updateRole.isPending}
                    />
                  ) : (
                    <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] font-medium uppercase text-muted-foreground">
                      {m.role}
                    </span>
                  )}
                  {canManage && !isCreator && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setPendingRemoveId(m.user_id)}
                      disabled={remove.isPending}
                      aria-label={`Remove ${m.email}`}
                      title={`Remove ${m.email}`}
                    >
                      <UserMinus className="h-4 w-4" />
                    </Button>
                  )}
                </div>
              </li>
            )
          })}
        </ul>
      )}

      <ConfirmDialog
        open={!!pendingRemoveId}
        onOpenChange={(o) => !o && setPendingRemoveId(null)}
        title="Remove member?"
        description={pendingRemove
          ? `Revoke workspace access for ${pendingRemove.display_name || pendingRemove.email}? They lose member-based access immediately; folder grants remain unchanged.`
          : ''}
        confirmLabel="Remove"
        destructive
        loading={remove.isPending}
        onConfirm={() => {
          if (!pendingRemoveId) return
          remove.mutate(
            { workspaceId, userId: pendingRemoveId },
            {
              onSuccess: () => {
                toast.success('Member removed')
                setPendingRemoveId(null)
              },
            },
          )
        }}
      />

      <p className="mt-3 text-xs text-muted-foreground">
        Manage users &amp; groups tenant-wide under{' '}
        <a className="underline" href="/admin/identity">Identity &amp; Access</a>.
      </p>
      <span id={`workspace-members-${workspaceId}`} className="sr-only">members</span>
    </SectionCard>
  )
}

// MemberPicker — focus opens initial candidate list; type to filter.
// Already-member users are excluded. Mirrors the picker we added on
// /admin/groups so admins get a consistent UX across the two surfaces.
function MemberPicker({
  excludeIds,
  isPending,
  onPick,
}: {
  excludeIds: Set<string>
  isPending: boolean
  onPick: (userId: string) => void
}) {
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)

  // Search the SERVER, not a snapshot of it.
  //
  // This used to fetch getUsers({limit:'25'}) once and filter the result in
  // the browser, which broke two ways:
  //   - the response was cached for 60s under a query key that ignored the
  //     search term, so a user created after the picker had been opened was
  //     unfindable until a hard reload — you could see them in Identity &
  //     Access yet not add them to a workspace;
  //   - only the 25 NEWEST users were ever fetched (ORDER BY created_at
  //     DESC), so past 25 users everyone older simply never appeared, and
  //     no amount of typing would surface them.
  // Keying the query on the debounced term fixes both: each distinct search
  // is its own cache entry and is answered by the database.
  const [debounced, setDebounced] = useState('')
  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 250)
    return () => clearTimeout(t)
  }, [q])

  const usersQ = useQuery({
    queryKey: ['user-directory', debounced],
    queryFn: () => listUserDirectory(debounced),
    enabled: open,
    staleTime: 30_000,
  })
  const candidates = (usersQ.data ?? [])
    .filter((u) => !excludeIds.has(u.id))
    .slice(0, 8)
  return (
    <div className="relative space-y-1">
      <div className="relative">
        <Search className="pointer-events-none absolute start-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
        <Input
          placeholder="Search by name or email to add a member…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onFocus={() => setOpen(true)}
          onBlur={() => setTimeout(() => setOpen(false), 150)}
          autoComplete="off"
          className="ps-9"
          data-testid="ws-member-picker-search"
        />
      </div>
      {open && (
        <ul
          className="absolute z-20 mt-1 max-h-56 w-full overflow-y-auto rounded-md bg-card text-sm shadow-neu"
          data-testid="ws-member-picker-results"
        >
          {usersQ.isLoading ? (
            <li className="px-3 py-2 text-xs text-muted-foreground">Loading…</li>
          ) : candidates.length === 0 ? (
            <li className="px-3 py-2 text-xs text-muted-foreground">
              {q ? 'No matching users.' : 'No users available to add.'}
            </li>
          ) : (
            candidates.map((u) => (
              <li key={u.id}>
                <button
                  type="button"
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => {
                    onPick(u.id)
                    setQ('')
                    setOpen(false)
                  }}
                  disabled={isPending}
                  className="flex w-full items-center justify-between gap-2 px-3 py-2 text-start hover:bg-muted disabled:opacity-50"
                  data-testid={`ws-member-pick-${u.id}`}
                >
                  <span className="flex min-w-0 items-center gap-2">
                    <Avatar name={u.display_name || u.email} size="sm" />
                    <span className="min-w-0">
                      <span className="block truncate text-sm">{u.display_name || u.email}</span>
                      <span className="block truncate text-xs text-muted-foreground">{u.email}</span>
                    </span>
                  </span>
                  <UserPlus className="h-4 w-4 text-muted-foreground" />
                </button>
              </li>
            ))
          )}
        </ul>
      )}
    </div>
  )
}

function StorageSection() {
  return (
    <SectionCard
      icon={<HardDrive className="h-4 w-4" />}
      title="Storage quota"
      description="How much workspace storage is used, and how much is left."
    >
      <div className="rounded-md border border-dashed border-border bg-muted/30 p-4 text-sm text-muted-foreground">
        Storage quota tracking is not yet available on the backend. This panel
        will populate once a workspace-scoped usage endpoint ships — no
        fabricated numbers in the meantime.
      </div>
    </SectionCard>
  )
}

function TransferOwnershipSection({
  workspaceId, currentOwnerId,
}: {
  workspaceId: string
  currentOwnerId: string
}) {
  const [newOwnerId, setNewOwnerId] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const transfer = useTransferWorkspaceOwnership()

  // Tenant-wide user list. The backend gate (`IsMember`) refuses any
  // non-member with a 400, so picking a non-member here would silently
  // fail. We can't filter by workspace membership without a list-
  // members endpoint (Phase 7), so for now we (a) surface the real
  // backend error via the useAppMutation wrapper, and (b) hard-disable
  // the action whenever the picker is empty so the bad-UUID 400 path
  // can't fire at all.
  const usersQ = useQuery({
    queryKey: ['admin', 'users', { for: 'workspace-transfer' }],
    queryFn: () => getUsers(),
    staleTime: 60_000,
  })

  const candidates = (usersQ.data?.items ?? []).filter((u) => u.id !== currentOwnerId)
  const selectedUser = candidates.find((u) => u.id === newOwnerId)
  const canTransfer = !!newOwnerId && !!selectedUser && !transfer.isPending

  return (
    <SectionCard
      icon={<UserCog className="h-4 w-4" />}
      title="Transfer ownership"
      description="Hand off the workspace to another existing member. The new owner must already belong to this workspace."
    >
      <div className="space-y-4">
        <Select
          label="New owner"
          value={newOwnerId}
          onValueChange={setNewOwnerId}
          placeholder="Select a user…"
          options={candidates.map((u) => ({
            value: u.id,
            label: `${u.display_name ?? u.email} — ${u.email}`,
          }))}
        />
        <div className="flex items-center justify-between">
          <p className="text-xs text-muted-foreground">
            You will lose your current owner privileges immediately. This is recorded in the audit log.
          </p>
          <Button
            variant="outline"
            disabled={!canTransfer}
            onClick={() => setConfirmOpen(true)}
            title={canTransfer ? undefined : 'Pick a workspace member to transfer to before continuing.'}
            data-testid="ws-transfer-open"
          >
            Transfer…
          </Button>
        </div>
      </div>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="Transfer workspace ownership?"
        description={selectedUser
          ? `Ownership will be reassigned to ${selectedUser.display_name ?? selectedUser.email}. You'll keep workspace access but lose the owner-only controls.`
          : 'Pick a new owner first.'}
        confirmLabel="Transfer ownership"
        loading={transfer.isPending}
        onConfirm={() => {
          if (!newOwnerId) return
          transfer.mutate(
            { workspaceId, newOwnerId },
            {
              onSuccess: () => {
                toast.success('Ownership transferred')
                setConfirmOpen(false)
                setNewOwnerId('')
              },
            },
          )
        }}
      />
    </SectionCard>
  )
}

function DangerZoneSection({
  workspaceId, workspaceName, documentCount, onDeleted,
}: {
  workspaceId: string
  workspaceName: string
  documentCount: number
  onDeleted: () => void
}) {
  const [open, setOpen] = useState(false)
  const del = useDeleteWorkspace()
  // Backend refuses with a 400 ("workspace is not empty") whenever
  // documentCount > 0. Surface the constraint up-front instead of
  // showing the typed-confirm dialog only to land on a toast error.
  const blockedByContents = documentCount > 0

  return (
    <SectionCard
      icon={<ShieldAlert className="h-4 w-4" />}
      title="Danger zone"
      description="Soft-delete the workspace. Folders and documents must be removed first."
      danger
    >
      <div className="flex items-center justify-between gap-4">
        <div className="space-y-1">
          <p className="text-sm text-muted-foreground">
            The workspace will be hidden from the UI. Re-activation requires admin support — there is no UI undo.
          </p>
          {blockedByContents && (
            <p className="text-xs text-destructive">
              This workspace still holds {documentCount} document{documentCount === 1 ? '' : 's'}. Move or delete them before you can delete the workspace itself.
            </p>
          )}
        </div>
        <Button
          variant="destructive"
          onClick={() => setOpen(true)}
          disabled={blockedByContents}
          title={blockedByContents ? 'Remove the documents inside this workspace first.' : undefined}
          data-testid="ws-delete-open"
        >
          Delete workspace
        </Button>
      </div>

      <TypedConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title="Delete this workspace?"
        description={`Soft-deletes "${workspaceName}". The auto-created Root folder is removed in the same transaction; any user-created folders or documents must already be gone (server returns 409 otherwise).`}
        expectedValue={workspaceName}
        inputLabel="Workspace name"
        confirmLabel="Delete workspace"
        destructive
        loading={del.isPending}
        confirmTestId="ws-delete-confirm"
        onConfirm={() => del.mutate(workspaceId, {
          onSuccess: () => {
            toast.success('Workspace deleted')
            setOpen(false)
            onDeleted()
          },
        })}
      />
    </SectionCard>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/settings')({
  component: SettingsPage,
})
