import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Lock, User as UserIcon, Users as GroupIcon, X, Search } from 'lucide-react'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/shadcn/dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@/components/ui/shadcn/tabs'
import {
  useAddWorkflowGrant,
  useRemoveWorkflowGrant,
  useWorkflowGrants,
} from '@/hooks/useWorkflows'
import { readErrorMessage } from '@/api/client'
import { getUsers } from '@/api/admin'
import { listGroups } from '@/api/groups'
import type { WorkflowDefinition } from '@/api/workflows'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  definition: WorkflowDefinition
}

// ManageWorkflowAccessDialog — mirror of folders/ManageFolderAccessDialog.
// Owner row + revocable grants list + tabbed user/group picker with
// dedup against existing grants. Owner falls back to created_by when
// the workflow has never been flipped to private.
export function ManageWorkflowAccessDialog({ open, onOpenChange, definition }: Props) {
  const { t } = useTranslation('workflows')
  const grants = useWorkflowGrants(open ? definition.id : null)
  const addGrant = useAddWorkflowGrant()
  const removeGrant = useRemoveWorkflowGrant()

  const [tab, setTab] = useState<'user' | 'group'>('user')
  const [query, setQuery] = useState('')

  // Always-fetch — needed to resolve owner_id / grant rows to human
  // labels regardless of which tab the picker is on.
  const usersQ = useQuery({
    queryKey: ['admin-users-workflow-grant'],
    queryFn: () => getUsers(),
    enabled: open,
    staleTime: 60_000,
  })
  const groupsQ = useQuery({
    queryKey: ['groups-workflow-grant'],
    queryFn: () => listGroups(),
    enabled: open,
    staleTime: 60_000,
  })

  const grantedIds = useMemo(() => {
    const u = new Set<string>()
    const g = new Set<string>()
    for (const x of grants.data ?? []) {
      if (x.grantee_type === 'user') u.add(x.grantee_id)
      else g.add(x.grantee_id)
    }
    return { u, g }
  }, [grants.data])

  // Owner falls back to created_by when no explicit owner_id is set —
  // matches the canManageDefinition fallback on the backend so the FE
  // doesn't show a blank owner for shared-being-flipped rows.
  const effectiveOwnerId = definition.owner_id || definition.created_by

  const visibleUsers = useMemo(() => {
    const items = usersQ.data?.items ?? []
    const q = query.trim().toLowerCase()
    return items.filter((u) => {
      if (u.id === effectiveOwnerId) return false
      if (grantedIds.u.has(u.id)) return false
      if (!q) return true
      return (
        u.email?.toLowerCase().includes(q) ||
        u.display_name?.toLowerCase().includes(q)
      )
    })
  }, [usersQ.data, grantedIds.u, query, effectiveOwnerId])

  const visibleGroups = useMemo(() => {
    const items = groupsQ.data ?? []
    const q = query.trim().toLowerCase()
    return items.filter((g) => {
      if (grantedIds.g.has(g.id)) return false
      if (!q) return true
      return g.name?.toLowerCase().includes(q)
    })
  }, [groupsQ.data, grantedIds.g, query])

  const userById = useMemo(() => {
    const m = new Map<string, { email: string; display_name?: string }>()
    for (const u of usersQ.data?.items ?? []) m.set(u.id, u)
    return m
  }, [usersQ.data])
  const groupById = useMemo(() => {
    const m = new Map<string, { name: string }>()
    for (const g of groupsQ.data ?? []) m.set(g.id, g)
    return m
  }, [groupsQ.data])

  const handleAdd = (granteeType: 'user' | 'group', granteeId: string) => {
    addGrant.mutate(
      { workflowId: definition.id, granteeType, granteeId },
      {
        onSuccess: () => toast.success(t('toasts.grant_added')),
        onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('toasts.error')),
      },
    )
  }

  const handleRemove = (granteeType: 'user' | 'group', granteeId: string) => {
    removeGrant.mutate(
      { workflowId: definition.id, granteeType, granteeId },
      {
        onSuccess: () => toast.success(t('toasts.grant_removed')),
        onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('toasts.error')),
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Lock className="h-4 w-4 text-warning" />
            {t('manage_access_dialog.title')}
          </DialogTitle>
          <DialogDescription>{t('manage_access_dialog.description')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {/* Owner */}
          <div className="flex items-center gap-3 rounded-lg border border-border bg-muted/30 p-3">
            <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-primary/10 text-primary">
              <UserIcon className="h-4 w-4" />
            </span>
            <div className="min-w-0 flex-1">
              <p className="text-[10px] uppercase tracking-wide text-muted-foreground">
                {t('manage_access_dialog.owner_label')}
              </p>
              {(() => {
                const owner = effectiveOwnerId ? userById.get(effectiveOwnerId) : undefined
                const label = owner?.display_name || owner?.email
                if (label) {
                  return (
                    <>
                      <p className="truncate text-sm font-medium">{label}</p>
                      {owner?.email && owner?.display_name && owner.email !== owner.display_name && (
                        <p className="truncate text-xs text-muted-foreground">{owner.email}</p>
                      )}
                    </>
                  )
                }
                return (
                  <p className="truncate font-mono text-xs text-muted-foreground">
                    {effectiveOwnerId ?? '—'}
                  </p>
                )
              })()}
            </div>
          </div>

          {/* Current grants */}
          <div>
            <p className="mb-2 text-xs font-medium text-muted-foreground">
              {t('manage_access_dialog.current_label')}
            </p>
            {grants.isLoading ? (
              <div className="flex justify-center py-4">
                <Spinner className="h-4 w-4" />
              </div>
            ) : (grants.data ?? []).length === 0 ? (
              <p className="rounded-md border border-dashed border-border p-3 text-center text-xs text-muted-foreground">
                {t('manage_access_dialog.no_grants')}
              </p>
            ) : (
              <ul className="space-y-1" data-testid="workflow-grants-list">
                {(grants.data ?? []).map((g) => {
                  const userInfo = g.grantee_type === 'user' ? userById.get(g.grantee_id) : undefined
                  const groupInfo = g.grantee_type === 'group' ? groupById.get(g.grantee_id) : undefined
                  const label =
                    userInfo?.display_name || userInfo?.email || groupInfo?.name || g.grantee_id
                  return (
                    <li
                      key={`${g.grantee_type}-${g.grantee_id}`}
                      className="flex items-center gap-2 rounded-md border border-border bg-card p-2 text-sm"
                    >
                      {g.grantee_type === 'user' ? (
                        <UserIcon className="h-4 w-4 text-muted-foreground" />
                      ) : (
                        <GroupIcon className="h-4 w-4 text-muted-foreground" />
                      )}
                      <span className="min-w-0 flex-1 truncate">{label}</span>
                      <span className="rounded-full bg-muted px-1.5 py-0 text-[10px] uppercase text-muted-foreground">
                        {g.grantee_type === 'user'
                          ? t('manage_access_dialog.type_user')
                          : t('manage_access_dialog.type_group')}
                      </span>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-9 w-9 text-destructive hover:bg-destructive/10"
                        onClick={() => handleRemove(g.grantee_type, g.grantee_id)}
                        disabled={removeGrant.isPending}
                        aria-label={t('manage_access_dialog.revoke')}
                        data-testid={`workflow-grant-revoke-${g.grantee_id}`}
                      >
                        <X className="h-3.5 w-3.5" />
                      </Button>
                    </li>
                  )
                })}
              </ul>
            )}
          </div>

          {/* Add new */}
          <div>
            <p className="mb-2 text-xs font-medium text-muted-foreground">
              {t('manage_access_dialog.add_label')}
            </p>
            <Tabs value={tab} onValueChange={(v) => setTab(v as 'user' | 'group')}>
              <TabsList>
                <TabsTrigger value="user" data-testid="wf-grant-tab-user">
                  {t('manage_access_dialog.add_user')}
                </TabsTrigger>
                <TabsTrigger value="group" data-testid="wf-grant-tab-group">
                  {t('manage_access_dialog.add_group')}
                </TabsTrigger>
              </TabsList>
              <TabsContent value="user" className="mt-3">
                <SearchAndList
                  query={query}
                  setQuery={setQuery}
                  isLoading={usersQ.isLoading}
                  items={visibleUsers.map((u) => ({
                    id: u.id,
                    primary: u.display_name || u.email,
                    secondary: u.email,
                  }))}
                  onPick={(id) => handleAdd('user', id)}
                  isAdding={addGrant.isPending}
                  testIdPrefix="wf-grant-user"
                />
              </TabsContent>
              <TabsContent value="group" className="mt-3">
                <SearchAndList
                  query={query}
                  setQuery={setQuery}
                  isLoading={groupsQ.isLoading}
                  items={visibleGroups.map((g) => ({
                    id: g.id,
                    primary: g.name,
                    secondary: g.description,
                  }))}
                  onPick={(id) => handleAdd('group', id)}
                  isAdding={addGrant.isPending}
                  testIdPrefix="wf-grant-group"
                />
              </TabsContent>
            </Tabs>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function SearchAndList({
  query,
  setQuery,
  isLoading,
  items,
  onPick,
  isAdding,
  testIdPrefix,
}: {
  query: string
  setQuery: (v: string) => void
  isLoading: boolean
  items: { id: string; primary: string; secondary?: string }[]
  onPick: (id: string) => void
  isAdding: boolean
  testIdPrefix: string
}) {
  return (
    <div className="space-y-2">
      <div className="relative">
        <Search className="pointer-events-none absolute start-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search…"
          className="ps-9"
          data-testid={`${testIdPrefix}-search`}
        />
      </div>
      {isLoading ? (
        <div className="flex justify-center py-4">
          <Spinner className="h-4 w-4" />
        </div>
      ) : items.length === 0 ? (
        <p className="rounded-md border border-dashed border-border p-3 text-center text-xs text-muted-foreground">
          No matches
        </p>
      ) : (
        <ul className="max-h-40 space-y-1 overflow-y-auto">
          {items.slice(0, 50).map((it) => (
            <li key={it.id}>
              <button
                type="button"
                onClick={() => onPick(it.id)}
                disabled={isAdding}
                className="flex w-full items-center gap-2 rounded-md border border-border bg-card p-2 text-start text-sm transition-colors hover:border-primary/40 hover:bg-muted/40 disabled:opacity-50"
                data-testid={`${testIdPrefix}-pick-${it.id}`}
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium">{it.primary}</span>
                  {it.secondary && (
                    <span className="block truncate text-xs text-muted-foreground">
                      {it.secondary}
                    </span>
                  )}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
