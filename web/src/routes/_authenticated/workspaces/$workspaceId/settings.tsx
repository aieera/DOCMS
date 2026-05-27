import { useEffect, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Settings as SettingsIcon, HardDrive, Users, ShieldAlert, UserCog } from 'lucide-react'
import { toast } from 'sonner'

import { getWorkspace } from '@/api/workspaces'
import { getUsers } from '@/api/admin'
import {
  useUpdateWorkspace, useDeleteWorkspace, useTransferWorkspaceOwnership,
} from '@/hooks/useWorkspaces'
import { useCurrentUser } from '@/hooks/useAuth'

import { PageHeader } from '@/components/shared/PageHeader'
import { Skeleton } from '@/components/ui/Skeleton'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { TypedConfirmDialog } from '@/components/ui/shadcn/typed-confirm-dialog'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'

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
        description="Manage details, members, storage, ownership, and danger zone for this workspace."
        actions={
          <Button
            variant="ghost"
            onClick={() => navigate({
              to: '/workspaces/$workspaceId',
              params: { workspaceId },
            })}
          >
            Back to workspace
          </Button>
        }
      />

      {wsQ.isLoading ? (
        <div className="space-y-4">
          <Skeleton className="h-40" />
          <Skeleton className="h-40" />
        </div>
      ) : wsQ.isError ? (
        <p className="rounded-md border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
          Could not load workspace.
        </p>
      ) : wsQ.data && (
        <>
          <DetailsSection
            workspaceId={workspaceId}
            initialName={wsQ.data.name}
            initialDescription={wsQ.data.description ?? ''}
            canEdit={canEditDetails}
          />
          <MembersSection workspaceId={workspaceId} />
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
              onDeleted={() => navigate({ to: '/workspaces' })}
            />
          )}
        </>
      )}
    </div>
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
            className="w-full rounded-md border border-border bg-background p-2 text-sm disabled:opacity-50"
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

function MembersSection({ workspaceId }: { workspaceId: string }) {
  return (
    <SectionCard
      icon={<Users className="h-4 w-4" />}
      title="Members"
      description="Add or remove people, change their workspace role."
    >
      <div className="rounded-md border border-dashed border-border bg-muted/30 p-4 text-sm text-muted-foreground">
        Per-workspace member management lives in the Manage Access surface (Phase 7).
        Until then, workspace_members entries are created when an admin grants
        permissions through the policy service.
      </div>
      <p className="mt-3 text-xs text-muted-foreground">
        Tenant-wide user administration: <a className="underline" href="/admin/users">/admin/users</a>{' '}
        ·{' '}
        Groups: <a className="underline" href="/admin/groups">/admin/groups</a>
      </p>
      {/* Hidden anchor so the route can deep-link to this section. */}
      <span id={`workspace-members-${workspaceId}`} className="sr-only">members</span>
    </SectionCard>
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
  // non-member, so the worst case here is a "must already be a
  // workspace member" 400 toast — clearer than silently filtering.
  const usersQ = useQuery({
    queryKey: ['admin', 'users', { for: 'workspace-transfer' }],
    queryFn: () => getUsers(),
    staleTime: 60_000,
  })

  const candidates = (usersQ.data?.items ?? []).filter((u) => u.id !== currentOwnerId)
  const selectedUser = candidates.find((u) => u.id === newOwnerId)

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
            disabled={transfer.isPending}
            onClick={() => {
              if (!newOwnerId) { toast.error('Select a user to transfer to'); return }
              setConfirmOpen(true)
            }}
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
  workspaceId, workspaceName, onDeleted,
}: {
  workspaceId: string
  workspaceName: string
  onDeleted: () => void
}) {
  const [open, setOpen] = useState(false)
  const del = useDeleteWorkspace()

  return (
    <SectionCard
      icon={<ShieldAlert className="h-4 w-4" />}
      title="Danger zone"
      description="Soft-delete the workspace. Folders and documents must be removed first."
      danger
    >
      <div className="flex items-center justify-between gap-4">
        <p className="text-sm text-muted-foreground">
          The workspace will be hidden from the UI. Re-activation requires admin support — there is no UI undo.
        </p>
        <Button
          variant="destructive"
          onClick={() => setOpen(true)}
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
