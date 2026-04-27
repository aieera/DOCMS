import { type ColumnDef } from '@tanstack/react-table'
import { DataTable } from '@/components/ui/DataTable'
import { Badge } from '@/components/ui/Badge'
import { Avatar } from '@/components/ui/Avatar'
import { Button } from '@/components/ui/Button'
import { MoreHorizontal, Shield, Ban, KeyRound, Lock } from 'lucide-react'
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator } from '@/components/ui/DropdownMenu'
import type { User } from '@/types/api'
import { formatRelativeTime } from '@/lib/formatters'

// Per-row admin actions. Wired through props so the page component
// owns mutation state + react-query invalidations; the table stays a
// pure render. Each handler hits the matching admin.ts API call:
//
//   onResetMFA           → POST /admin/users/{id}/reset-mfa
//   onForcePasswordReset → POST /admin/users/{id}/force-password-reset (Wave 17 audit P2#8)
//   onSuspend            → POST /admin/users/{id}/suspend (destructive)
export interface UserTableActions {
  onResetMFA?: (user: User) => void
  onForcePasswordReset?: (user: User) => void
  onSuspend?: (user: User) => void
}

function buildColumns(actions: UserTableActions): ColumnDef<User, unknown>[] {
  return [
    {
      accessorKey: 'display_name',
      header: 'User',
      cell: ({ row }) => (
        <div className="flex items-center gap-2">
          <Avatar name={row.original.display_name} size="sm" />
          <div>
            <p className="text-sm font-medium">{row.original.display_name}</p>
            <p className="text-xs text-[var(--color-text-secondary)]">{row.original.email}</p>
          </div>
        </div>
      ),
    },
    { accessorKey: 'role', header: 'Role', cell: ({ row }) => <Badge>{row.original.role}</Badge> },
    {
      accessorKey: 'mfa_enabled',
      header: 'MFA',
      cell: ({ row }) => <Badge variant={row.original.mfa_enabled ? 'active' : 'draft'}>{row.original.mfa_enabled ? 'Enabled' : 'Off'}</Badge>,
    },
    {
      accessorKey: 'last_login_at',
      header: 'Last Login',
      cell: ({ row }) => <span className="text-sm text-[var(--color-text-secondary)]">{row.original.last_login_at ? formatRelativeTime(row.original.last_login_at) : 'Never'}</span>,
    },
    {
      id: 'actions',
      header: '',
      cell: ({ row }) => (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm" data-testid={`user-actions-${row.original.id}`}>
              <MoreHorizontal className="h-4 w-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuItem icon={<Shield className="h-4 w-4" />}>Edit Role</DropdownMenuItem>
            <DropdownMenuItem
              icon={<KeyRound className="h-4 w-4" />}
              onSelect={() => actions.onResetMFA?.(row.original)}
            >
              Reset MFA
            </DropdownMenuItem>
            <DropdownMenuItem
              icon={<Lock className="h-4 w-4" />}
              onSelect={() => actions.onForcePasswordReset?.(row.original)}
            >
              Force password reset
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              icon={<Ban className="h-4 w-4" />}
              destructive
              onSelect={() => actions.onSuspend?.(row.original)}
            >
              Suspend
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      ),
    },
  ]
}

export function UserTable({ users, actions }: { users: User[]; actions?: UserTableActions }) {
  return <DataTable columns={buildColumns(actions ?? {})} data={users} />
}
