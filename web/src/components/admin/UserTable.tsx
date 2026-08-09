import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { type ColumnDef } from '@tanstack/react-table'
import { toast } from 'sonner'
import { MoreHorizontal, Shield, Ban, KeyRound, UserCheck } from 'lucide-react'

import { DataTable } from '@/components/ui/DataTable'
import { Badge } from '@/components/ui/shadcn/badge'
import { Avatar } from '@/components/ui/shadcn/avatar'
import { Button } from '@/components/ui/shadcn/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/shadcn/dropdown-menu'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/shadcn/dialog'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { changeUserRole, resetMFA, suspendUser, reactivateUser } from '@/api/admin'
import { readErrorMessage } from '@/api/client'
import type { User } from '@/types/api'
import { formatRelativeTime } from '@/lib/formatters'

const ROLE_OPTIONS = [
  { value: 'owner',              label: 'Owner — full tenant control' },
  { value: 'admin',              label: 'Admin — manage users + settings' },
  { value: 'member',             label: 'Member — read/write documents' },
  { value: 'viewer',             label: 'Viewer — read-only' },
  { value: 'compliance_officer', label: 'Compliance officer — legal hold + redaction' },
]

interface ActionState {
  user: User
  kind: 'role' | 'mfa' | 'suspend' | 'reactivate'
}

// UserTable renders the admin user list and wires the dropdown
// actions (Edit role, Reset MFA, Suspend) into real backend calls.
// Previously the dropdown items had no onSelect handlers — clicking
// any of them closed the menu silently.
export function UserTable({ users, isLoading }: { users: User[]; isLoading?: boolean }) {
  const qc = useQueryClient()
  // Single-slot action state — only one dialog open at a time.
  const [action, setAction] = useState<ActionState | null>(null)
  const [pendingRole, setPendingRole] = useState<string>('member')

  const close = () => setAction(null)

  const roleMut = useAppMutation({
    mutationFn: ({ id, role }: { id: string; role: string }) => changeUserRole(id, role),
    onSuccess: (_, vars) => {
      toast.success(`Role updated to ${vars.role}`)
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      close()
    },
    onError: (e: Error) => toast.error(e.message || 'Could not change role'),
  })
  const mfaMut = useAppMutation({
    mutationFn: (id: string) => resetMFA(id),
    onSuccess: () => {
      toast.success('MFA reset — user will be prompted to re-enroll')
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      close()
    },
    onError: (e: Error) => toast.error(e.message || 'Could not reset MFA'),
  })
  const suspendMut = useAppMutation({
    mutationFn: (id: string) => suspendUser(id),
    onSuccess: () => {
      toast.success('User suspended — active sessions revoked')
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      close()
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not suspend user'),
  })
  const reactivateMut = useAppMutation({
    mutationFn: (id: string) => reactivateUser(id),
    onSuccess: () => {
      toast.success('User reactivated')
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      close()
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not reactivate user'),
  })

  const columns: ColumnDef<User, unknown>[] = [
    {
      accessorKey: 'display_name',
      header: 'User',
      cell: ({ row }) => (
        <div className="flex items-center gap-2">
          <Avatar name={row.original.display_name} size="sm" />
          <div>
            <p className="text-sm font-medium">{row.original.display_name}</p>
            <p className="text-xs text-muted-foreground">{row.original.email}</p>
          </div>
        </div>
      ),
    },
    {
      accessorKey: 'role',
      header: 'Role',
      cell: ({ row }) => {
        const r: string = row.original.role
        // Distinct color per privilege tier so an admin row reads
        // differently at a glance from a member row. Owner gets the
        // strongest accent; member stays neutral so the dominant
        // case doesn't shout.
        const cls =
          r === 'owner'
            ? 'border-violet-500/40 bg-violet-500/15 text-violet-700 dark:text-violet-300'
            : r === 'admin'
              ? 'border-purple-500/40 bg-purple-500/10 text-purple-700 dark:text-purple-300'
              : r === 'compliance_officer'
                ? 'border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300'
                : r === 'viewer' || r === 'guest'
                  ? 'border-border bg-muted text-muted-foreground'
                  : 'border-border bg-card text-foreground'
        return (
          <span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[11px] font-medium capitalize ${cls}`}>
            {r}
          </span>
        )
      },
    },
    {
      accessorKey: 'status',
      header: 'Status',
      cell: ({ row }) => {
        const s = row.original.status ?? 'active'
        // An invited account that has never signed in is neither a
        // success nor a problem — the green "active" tone overstated it.
        const neverLoggedIn = !row.original.last_login_at && s === 'active'
        const cls =
          neverLoggedIn
            ? 'border-border bg-muted text-muted-foreground'
            : s === 'active'
              ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300'
              : s === 'suspended'
                ? 'border-destructive/40 bg-destructive/10 text-red-700 dark:text-red-300'
                : 'border-border bg-muted text-muted-foreground'
        const label = neverLoggedIn ? 'Never logged in' : s
        return (
          <span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[11px] font-medium capitalize ${cls}`}>
            {label}
          </span>
        )
      },
    },
    {
      accessorKey: 'mfa_enabled',
      header: 'MFA',
      cell: ({ row }) => (
        <Badge variant={row.original.mfa_enabled ? 'active' : 'draft'}>
          {row.original.mfa_enabled ? 'Enabled' : 'Off'}
        </Badge>
      ),
    },
    {
      accessorKey: 'last_login_at',
      header: 'Last Login',
      cell: ({ row }) => (
        <span className="text-sm text-muted-foreground">
          {row.original.last_login_at ? formatRelativeTime(row.original.last_login_at) : 'Never'}
        </span>
      ),
    },
    {
      id: 'actions',
      header: '',
      cell: ({ row }) => {
        const u = row.original
        return (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" aria-label={`Actions for ${u.email}`} data-testid={`user-actions-${u.id}`}>
                <MoreHorizontal className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                onSelect={() => { setPendingRole(u.role); setAction({ user: u, kind: 'role' }) }}
                data-testid={`edit-role-${u.id}`}
              >
                <Shield className="h-4 w-4" />
                Edit role
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={() => setAction({ user: u, kind: 'mfa' })}
                data-testid={`reset-mfa-${u.id}`}
              >
                <KeyRound className="h-4 w-4" />
                Reset MFA
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              {u.status === 'suspended' ? (
                <DropdownMenuItem
                  onSelect={() => setAction({ user: u, kind: 'reactivate' })}
                  data-testid={`reactivate-${u.id}`}
                >
                  <UserCheck className="h-4 w-4" />
                  Reactivate
                </DropdownMenuItem>
              ) : (
                <DropdownMenuItem
                  className="text-destructive focus:text-destructive"
                  onSelect={() => setAction({ user: u, kind: 'suspend' })}
                  data-testid={`suspend-${u.id}`}
                >
                  <Ban className="h-4 w-4" />
                  Suspend
                </DropdownMenuItem>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        )
      },
    },
  ]

  return (
    <>
      <DataTable
        columns={columns}
        data={users}
        isLoading={isLoading}
        emptyState="No users yet. Invite someone to get started."
      />

      <Dialog open={action?.kind === 'role'} onOpenChange={(o) => { if (!o) close() }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Change role for {action?.user.email}</DialogTitle>
            <DialogDescription>
              Owners can manage everything in the tenant. The last remaining owner can't be demoted.
            </DialogDescription>
          </DialogHeader>
          <Select
            label="New role"
            value={pendingRole}
            onValueChange={setPendingRole}
            options={ROLE_OPTIONS}
          />
          <DialogFooter>
            <Button variant="ghost" onClick={close} disabled={roleMut.isPending}>Cancel</Button>
            <Button
              onClick={() => action && roleMut.mutate({ id: action.user.id, role: pendingRole })}
              loading={roleMut.isPending}
              data-testid="change-role-confirm"
            >
              Update role
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={action?.kind === 'mfa'}
        onOpenChange={(o) => { if (!o) close() }}
        title={action ? `Reset MFA for ${action.user.email}?` : 'Reset MFA'}
        description="The user's stored MFA secret + recovery codes are wiped. They'll be prompted to re-enroll on next login. Active sessions are revoked."
        confirmLabel="Reset MFA"
        loading={mfaMut.isPending}
        onConfirm={() => action && mfaMut.mutate(action.user.id)}
      />

      <ConfirmDialog
        open={action?.kind === 'suspend'}
        onOpenChange={(o) => { if (!o) close() }}
        title={action ? `Suspend ${action.user.email}?` : 'Suspend user'}
        description="Suspended users can't sign in and their active sessions are revoked. You can reactivate them by removing the suspension."
        confirmLabel="Suspend user"
        destructive
        loading={suspendMut.isPending}
        onConfirm={() => action && suspendMut.mutate(action.user.id)}
      />

      <ConfirmDialog
        open={action?.kind === 'reactivate'}
        onOpenChange={(o) => { if (!o) close() }}
        title={action ? `Reactivate ${action.user.email}?` : 'Reactivate user'}
        description="Reactivating restores sign-in but does not restore the user's old sessions — they'll go through the regular login flow (including MFA, if enabled)."
        confirmLabel="Reactivate"
        loading={reactivateMut.isPending}
        onConfirm={() => action && reactivateMut.mutate(action.user.id)}
      />
    </>
  )
}
