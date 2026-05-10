import { type ColumnDef } from '@tanstack/react-table'
import { DataTable } from '@/components/ui/DataTable'
import { Badge } from '@/components/ui/Badge'
import { Avatar } from '@/components/ui/Avatar'
import { Button } from '@/components/ui/shadcn/button'
import { MoreHorizontal, Shield, Ban, KeyRound } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/shadcn/dropdown-menu'
import type { User } from '@/types/api'
import { formatRelativeTime } from '@/lib/formatters'

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
  { accessorKey: 'role', header: 'Role', cell: ({ row }) => <Badge>{row.original.role}</Badge> },
  {
    accessorKey: 'mfa_enabled',
    header: 'MFA',
    cell: ({ row }) => <Badge variant={row.original.mfa_enabled ? 'active' : 'draft'}>{row.original.mfa_enabled ? 'Enabled' : 'Off'}</Badge>,
  },
  {
    accessorKey: 'last_login_at',
    header: 'Last Login',
    cell: ({ row }) => <span className="text-sm text-muted-foreground">{row.original.last_login_at ? formatRelativeTime(row.original.last_login_at) : 'Never'}</span>,
  },
  {
    id: 'actions',
    header: '',
    cell: () => (
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" aria-label="User actions">
            <MoreHorizontal className="h-4 w-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem>
            <Shield className="h-4 w-4" />
            Edit role
          </DropdownMenuItem>
          <DropdownMenuItem>
            <KeyRound className="h-4 w-4" />
            Reset MFA
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem className="text-destructive focus:text-destructive">
            <Ban className="h-4 w-4" />
            Suspend
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    ),
  },
]

export function UserTable({ users, isLoading }: { users: User[]; isLoading?: boolean }) {
  return (
    <DataTable
      columns={columns}
      data={users}
      isLoading={isLoading}
      emptyState="No users yet. Invite someone to get started."
    />
  )
}
