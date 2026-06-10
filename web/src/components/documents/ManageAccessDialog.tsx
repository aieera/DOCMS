import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Trash2, User as UserIcon, Users as UsersIcon, ShieldAlert } from 'lucide-react'

import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Badge } from '@/components/ui/shadcn/badge'
import { Skeleton } from '@/components/ui/Skeleton'
import { EmptyState } from '@/components/ui/EmptyState'
import { Combobox } from '@/components/ui/shadcn/combobox'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { useAppMutation } from '@/hooks/useAppMutation'
import { readErrorMessage } from '@/api/client'

import {
  getPermissions, grantPermission, revokePermission, checkPermission,
  type Permission,
} from '@/api/permissions'
import { getUsers } from '@/api/admin'
import { listGroups, type Group } from '@/api/groups'
import type { User } from '@/types/api'

// Capabilities surfaced in the UI dropdown. Backend accepts more
// (view_unredacted, delete) but we deliberately expose the smaller
// set per the Phase 7 design decision. Admins who need the rare
// levels can call the API directly.
export type Capability = 'view' | 'share' | 'edit' | 'admin'

const CAPABILITY_LABELS: Record<Capability, string> = {
  view: 'Viewer',
  share: 'Commenter',
  edit: 'Editor',
  admin: 'Admin',
}

const CAPABILITY_DESC: Record<Capability, string> = {
  view: 'Can view',
  share: 'Can view and share',
  edit: 'Can view, edit, and share',
  admin: 'Full control, including managing access',
}

// Hierarchy rank — must match services/policy/internal/model/model.go.
// Used to compare residual capability when removing a direct grant.
const CAPABILITY_RANK: Record<string, number> = {
  view: 10,
  view_unredacted: 15,
  share: 20,
  edit: 30,
  delete: 40,
  admin: 50,
}

function capabilityLabel(capability: string): string {
  if (capability in CAPABILITY_LABELS) return CAPABILITY_LABELS[capability as Capability]
  return capability.charAt(0).toUpperCase() + capability.slice(1).replace('_', ' ')
}

export interface ManageAccessDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  resourceType: 'document' | 'folder' | 'workspace'
  resourceId: string
  resourceTitle: string
  /** Parent workspace id — used to fetch inherited grants. */
  workspaceId?: string
  /** Parent folder id — used to fetch inherited grants (documents only). */
  folderId?: string
}

export function ManageAccessDialog({
  open, onOpenChange,
  resourceType, resourceId, resourceTitle,
  workspaceId, folderId,
}: ManageAccessDialogProps) {
  const qc = useQueryClient()
  const [principalMode, setPrincipalMode] = useState<'user' | 'group'>('user')
  const [selectedPrincipalId, setSelectedPrincipalId] = useState<string>('')
  const [capability, setCapability] = useState<Capability>('view')
  const [expiresAt, setExpiresAt] = useState<string>('')
  const [removeTarget, setRemoveTarget] = useState<Permission | null>(null)

  // -- Backend truth check: can the caller administer this resource? --
  // The grant/revoke endpoints reject non-admins server-side, so the FE
  // gate is purely an affordance — show the management form only when
  // the caller is actually entitled.
  const canManage = useQuery({
    queryKey: ['permission-check', 'admin', resourceType, resourceId],
    queryFn: () => checkPermission('admin', resourceType, resourceId),
    enabled: open,
  })

  // Direct grants on THIS resource.
  const direct = useQuery({
    queryKey: ['permissions', resourceType, resourceId],
    queryFn: () => getPermissions(resourceType, resourceId),
    enabled: open,
  })

  // Workspace-level grants — inherited by every folder + document
  // inside. Skip when the dialog is targeting the workspace itself.
  const wsPerms = useQuery({
    queryKey: ['permissions', 'workspace', workspaceId],
    queryFn: () => getPermissions('workspace', workspaceId!),
    enabled: open && !!workspaceId && resourceType !== 'workspace',
  })

  // Folder-level grants — inherited by documents inside that folder.
  const folderPermsQuery = useQuery({
    queryKey: ['permissions', 'folder', folderId],
    queryFn: () => getPermissions('folder', folderId!),
    enabled: open && !!folderId && resourceType === 'document',
  })

  // Users + groups for the principal picker. Only fetched when the
  // caller has admin access — they're irrelevant otherwise.
  // Share the ['admin', 'users'] cache with the admin Users page (and
  // other pickers) so opening this dialog reuses an already-fetched
  // list instead of issuing a duplicate /admin/users request. `select`
  // keeps the local items-only shape without forking the cache.
  const usersQuery = useQuery({
    queryKey: ['admin', 'users'],
    queryFn: () => getUsers(),
    select: (r) => r.items,
    enabled: open && canManage.data === true,
  })
  const groupsQuery = useQuery({
    queryKey: ['groups'],
    queryFn: listGroups,
    enabled: open && canManage.data === true,
  })

  const invalidateAll = () => {
    qc.invalidateQueries({ queryKey: ['permissions', resourceType, resourceId] })
  }

  const grant = useAppMutation({
    mutationFn: (vars: { principalType: 'user' | 'group'; principalId: string; capability: Capability; expiresAt?: string }) =>
      grantPermission(resourceType, resourceId, vars.principalType, vars.principalId, vars.capability, vars.expiresAt),
    onSuccess: () => {
      toast.success('Access granted')
      invalidateAll()
      setSelectedPrincipalId('')
      setExpiresAt('')
      setCapability('view')
    },
    defaultErrorMessage: 'Could not grant access',
  })

  // Revoke deletes ALL direct grants for this principal on this
  // resource (the backend endpoint is principal-scoped, not grant-
  // scoped). Inheritance from parent scopes is unaffected.
  //
  // OPTIMISTIC PATTERN: onMutate strips the rows from the cache
  // before the network call; if the backend rejects (403/423/etc)
  // we restore the snapshot in onError and surface the error
  // message via readErrorMessage. onSettled invalidates so the
  // server's canonical state replaces the optimistic write either
  // way. We pass an explicit onError so we own rollback + toast in
  // one place; useAppMutation respects a caller-provided onError
  // verbatim (no default toast fires on top of ours).
  const revoke = useAppMutation({
    mutationFn: (principalId: string) =>
      revokePermission(resourceType, resourceId, principalId),
    onMutate: async (principalId: string) => {
      await qc.cancelQueries({ queryKey: ['permissions', resourceType, resourceId] })
      const previous = qc.getQueryData<Permission[]>(['permissions', resourceType, resourceId])
      qc.setQueryData<Permission[]>(
        ['permissions', resourceType, resourceId],
        (old) => (old ?? []).filter((p) => p.principal_id !== principalId),
      )
      return { previous }
    },
    onError: (err, _principalId, ctx) => {
      if (ctx?.previous) {
        qc.setQueryData(['permissions', resourceType, resourceId], ctx.previous)
      }
      toast.error(readErrorMessage(err) ?? 'Could not remove access')
    },
    onSuccess: () => {
      toast.success('Access removed')
      setRemoveTarget(null)
    },
    onSettled: () => {
      // Invalidate regardless of outcome so the server's truth wins.
      qc.invalidateQueries({ queryKey: ['permissions', resourceType, resourceId] })
    },
  })

  // Capability change: backend has no in-place update; the upsert path
  // is revoke (principal) → grant (principal, new capability). The
  // window between the two calls is small and a failure between them
  // surfaces the error via the mutation's default onError toast.
  const changeCapability = useAppMutation({
    mutationFn: async (vars: { principalType: 'user' | 'group'; principalId: string; newCapability: Capability }) => {
      await revokePermission(resourceType, resourceId, vars.principalId)
      return grantPermission(resourceType, resourceId, vars.principalType, vars.principalId, vars.newCapability)
    },
    onSuccess: () => {
      toast.success('Access level updated')
      invalidateAll()
    },
    defaultErrorMessage: 'Could not update access level',
  })

  const directGranteeIds = useMemo(
    () => new Set((direct.data ?? []).map((p) => p.principal_id)),
    [direct.data],
  )

  const principalOptions = useMemo(() => {
    if (principalMode === 'user') {
      return (usersQuery.data ?? [])
        .filter((u: User) => !directGranteeIds.has(u.id))
        .map((u: User) => ({
          value: u.id,
          label: u.display_name ? `${u.display_name} (${u.email})` : u.email,
        }))
    }
    return (groupsQuery.data ?? [])
      .filter((g: Group) => !directGranteeIds.has(g.id))
      .map((g: Group) => ({ value: g.id, label: g.name }))
  }, [principalMode, usersQuery.data, groupsQuery.data, directGranteeIds])

  function principalLabel(perm: Permission): { name: string; sub?: string; isGroup: boolean } {
    if (perm.principal_type === 'group') {
      const g = (groupsQuery.data ?? []).find((g: Group) => g.id === perm.principal_id)
      return { name: g?.name ?? 'Unknown group', sub: 'Group', isGroup: true }
    }
    const u = (usersQuery.data ?? []).find((u: User) => u.id === perm.principal_id)
    return { name: u?.display_name ?? 'Unknown user', sub: u?.email, isGroup: false }
  }

  // For the removal-confirmation copy: find the residual grant the
  // principal would still have via a parent scope. Returns the highest
  // residual grant (folder beats workspace because folder is closer in
  // the cascade, BUT the backend resolves by max-rank — so we pick
  // max-rank to mirror that exactly).
  function residualAccess(principalId: string): { capability: string; source: string } | null {
    const folderG = (folderPermsQuery.data ?? []).find((p) => p.principal_id === principalId)
    const wsG = (wsPerms.data ?? []).find((p) => p.principal_id === principalId)
    const candidates: { capability: string; source: string }[] = []
    if (folderG) candidates.push({ capability: folderG.capability, source: 'folder' })
    if (wsG) candidates.push({ capability: wsG.capability, source: 'workspace' })
    if (candidates.length === 0) return null
    candidates.sort((a, b) => (CAPABILITY_RANK[b.capability] ?? 0) - (CAPABILITY_RANK[a.capability] ?? 0))
    return candidates[0]
  }

  const handleGrant = () => {
    if (!selectedPrincipalId) {
      toast.error('Pick a user or group')
      return
    }
    grant.mutate({
      principalType: principalMode,
      principalId: selectedPrincipalId,
      capability,
      expiresAt: expiresAt || undefined,
    })
  }

  const isLoading = direct.isLoading || canManage.isLoading
  const isAdmin = canManage.data === true

  // Inherited rows from workspace + folder for the "Inherited" section.
  // We render BOTH workspaces and folders; rows in the direct table are
  // excluded since they already show as direct.
  const inheritedRows = useMemo(() => {
    const rows: { perm: Permission; source: 'workspace' | 'folder' }[] = []
    for (const p of wsPerms.data ?? []) {
      rows.push({ perm: p, source: 'workspace' })
    }
    for (const p of folderPermsQuery.data ?? []) {
      rows.push({ perm: p, source: 'folder' })
    }
    return rows
  }, [wsPerms.data, folderPermsQuery.data])

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Manage access — ${resourceTitle}`}
      description="Direct grants on this item, plus access inherited from the workspace and folder."
      size="lg"
    >
      <div className="space-y-6" data-testid="manage-access-dialog">
        {isLoading ? (
          <div className="space-y-3">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : (
          <>
            {/* --- Add people (admin only) ---------------------------- */}
            {isAdmin && (
              <section className="space-y-3 rounded-lg border border-border bg-card p-4" data-testid="add-access-form">
                <h3 className="text-sm font-semibold">Add people or groups</h3>
                <div className="flex gap-2">
                  <Button
                    type="button"
                    variant={principalMode === 'user' ? 'default' : 'outline'}
                    size="sm"
                    onClick={() => { setPrincipalMode('user'); setSelectedPrincipalId('') }}
                    data-testid="principal-mode-user"
                  >
                    <UserIcon className="me-1 h-3.5 w-3.5" /> User
                  </Button>
                  <Button
                    type="button"
                    variant={principalMode === 'group' ? 'default' : 'outline'}
                    size="sm"
                    onClick={() => { setPrincipalMode('group'); setSelectedPrincipalId('') }}
                    data-testid="principal-mode-group"
                  >
                    <UsersIcon className="me-1 h-3.5 w-3.5" /> Group
                  </Button>
                </div>

                <div className="grid gap-2 md:grid-cols-[2fr_1fr_auto]">
                  <Combobox
                    options={principalOptions}
                    value={selectedPrincipalId}
                    onChange={setSelectedPrincipalId}
                    placeholder={principalMode === 'user' ? 'Pick a user…' : 'Pick a group…'}
                    searchPlaceholder={principalMode === 'user' ? 'Search by name or email' : 'Search groups'}
                    emptyText={principalMode === 'user' ? 'No users found' : 'No groups found'}
                  />
                  <select
                    value={capability}
                    onChange={(e) => setCapability(e.target.value as Capability)}
                    className="rounded-md border border-border bg-background px-2 py-2 text-sm"
                    aria-label="Access level"
                    data-testid="capability-select"
                  >
                    {(Object.keys(CAPABILITY_LABELS) as Capability[]).map((c) => (
                      <option key={c} value={c}>{CAPABILITY_LABELS[c]}</option>
                    ))}
                  </select>
                  <Button
                    type="button"
                    onClick={handleGrant}
                    disabled={!selectedPrincipalId || grant.isPending}
                    loading={grant.isPending}
                    data-testid="grant-access-submit"
                  >
                    Add access
                  </Button>
                </div>

                <div className="grid gap-2 md:grid-cols-[1fr_auto]">
                  <p className="text-xs text-muted-foreground">
                    {CAPABILITY_DESC[capability]}
                  </p>
                  <div className="flex items-center gap-2 text-xs">
                    <label htmlFor="grant-expires" className="text-muted-foreground">Expires:</label>
                    <input
                      id="grant-expires"
                      type="date"
                      value={expiresAt.slice(0, 10)}
                      onChange={(e) => setExpiresAt(e.target.value ? `${e.target.value}T00:00:00Z` : '')}
                      className="rounded-md border border-border bg-background px-1.5 py-1 text-xs"
                      data-testid="grant-expires-input"
                    />
                  </div>
                </div>
              </section>
            )}

            {!isAdmin && (
              <div className="flex items-start gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-sm" data-testid="manage-access-readonly-notice">
                <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
                <div>
                  <p className="font-medium">View-only</p>
                  <p className="text-muted-foreground">
                    You don&apos;t have admin access on this {resourceType}. You can see who else has access but cannot grant or revoke.
                  </p>
                </div>
              </div>
            )}

            {/* --- Direct grants -------------------------------------- */}
            <section className="space-y-2">
              <h3 className="text-sm font-semibold">Direct access</h3>
              {(direct.data ?? []).length === 0 ? (
                <EmptyState
                  title="No direct grants"
                  description="Only inherited access from the workspace or folder applies."
                />
              ) : (
                <ul className="divide-y divide-border rounded-lg border border-border" data-testid="direct-access-list">
                  {(direct.data ?? []).map((perm) => {
                    const p = principalLabel(perm)
                    return (
                      <li
                        key={`${perm.principal_id}:${perm.capability}`}
                        className="flex items-center gap-3 px-3 py-2"
                        data-testid={`direct-access-${perm.principal_id}`}
                      >
                        <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted">
                          {p.isGroup ? <UsersIcon className="h-4 w-4" /> : <UserIcon className="h-4 w-4" />}
                        </span>
                        <div className="min-w-0 flex-1">
                          <p className="truncate text-sm font-medium">{p.name}</p>
                          {p.sub && <p className="truncate text-xs text-muted-foreground">{p.sub}</p>}
                        </div>
                        {isAdmin ? (
                          <select
                            value={(perm.capability as Capability) in CAPABILITY_LABELS ? perm.capability : 'view'}
                            onChange={(e) =>
                              changeCapability.mutate({
                                principalType: perm.principal_type as 'user' | 'group',
                                principalId: perm.principal_id,
                                newCapability: e.target.value as Capability,
                              })
                            }
                            disabled={changeCapability.isPending}
                            className="rounded-md border border-border bg-background px-2 py-1 text-xs"
                            aria-label={`Access level for ${p.name}`}
                            data-testid={`direct-capability-${perm.principal_id}`}
                          >
                            {(Object.keys(CAPABILITY_LABELS) as Capability[]).map((c) => (
                              <option key={c} value={c}>{CAPABILITY_LABELS[c]}</option>
                            ))}
                          </select>
                        ) : (
                          <Badge variant="default">{capabilityLabel(perm.capability)}</Badge>
                        )}
                        {isAdmin && (
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7 text-destructive hover:text-destructive"
                            onClick={() => setRemoveTarget(perm)}
                            aria-label={`Remove access for ${p.name}`}
                            data-testid={`remove-access-${perm.principal_id}`}
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                          </Button>
                        )}
                      </li>
                    )
                  })}
                </ul>
              )}
            </section>

            {/* --- Inherited grants ----------------------------------- */}
            {resourceType !== 'workspace' && (
              <section className="space-y-2">
                <h3 className="text-sm font-semibold">Inherited access</h3>
                <p className="text-xs text-muted-foreground">
                  Grants made at the workspace or parent folder cascade here. Highest level wins. Workspace owners and organisation admins always retain full access.
                </p>
                {inheritedRows.length === 0 ? (
                  <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-muted-foreground">
                    No grants inherited from the workspace or folder.
                  </p>
                ) : (
                  <ul className="divide-y divide-border rounded-lg border border-border bg-muted/30" data-testid="inherited-access-list">
                    {inheritedRows.map(({ perm, source }) => {
                      const p = principalLabel(perm)
                      return (
                        <li
                          key={`${source}:${perm.principal_id}:${perm.capability}`}
                          className="flex items-center gap-3 px-3 py-2"
                          data-testid={`inherited-access-${source}-${perm.principal_id}`}
                        >
                          <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted">
                            {p.isGroup ? <UsersIcon className="h-4 w-4" /> : <UserIcon className="h-4 w-4" />}
                          </span>
                          <div className="min-w-0 flex-1">
                            <p className="truncate text-sm font-medium">{p.name}</p>
                            {p.sub && <p className="truncate text-xs text-muted-foreground">{p.sub}</p>}
                          </div>
                          <Badge variant="default">{capabilityLabel(perm.capability)}</Badge>
                          <Badge variant="outline">From {source}</Badge>
                        </li>
                      )
                    })}
                  </ul>
                )}
              </section>
            )}
          </>
        )}

        <div className="flex justify-end">
          <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
            Done
          </Button>
        </div>
      </div>

      <ConfirmDialog
        open={!!removeTarget}
        onOpenChange={(o) => { if (!o) setRemoveTarget(null) }}
        title="Remove direct access?"
        description={removeTarget ? buildRemovalDescription(principalLabel(removeTarget), residualAccess(removeTarget.principal_id)) : ''}
        confirmLabel="Remove access"
        destructive
        loading={revoke.isPending}
        onConfirm={() => removeTarget && revoke.mutate(removeTarget.principal_id)}
      />
    </Dialog>
  )
}

function buildRemovalDescription(
  who: { name: string },
  residual: { capability: string; source: string } | null,
): string {
  if (residual) {
    return `${who.name} will still have ${capabilityLabel(residual.capability)} access from the ${residual.source}.`
  }
  return `${who.name} will lose direct access. They may still have access via group membership or organisation role.`
}
