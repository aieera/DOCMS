import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { getUsers } from '@/api/admin'
import { PageHeader } from '@/components/shared/PageHeader'
import { UserTable } from '@/components/admin/UserTable'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Plus } from 'lucide-react'

function UsersPage() {
  const { data, isLoading } = useQuery({ queryKey: ['admin', 'users'], queryFn: () => getUsers() })
  return (
    <div>
      <PageHeader title="Users" description="Manage team members" actions={<Button><Plus className="h-4 w-4" /> Invite User</Button>} />
      {isLoading ? <Skeleton className="h-64" /> : <UserTable users={data?.items || []} />}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/users')({ component: UsersPage })
