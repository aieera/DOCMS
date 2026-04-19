import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { getWorkspaces } from '@/api/workspaces'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Plus, FolderOpen, Users, FileText } from 'lucide-react'
import { Link } from '@tanstack/react-router'

function WorkspacesPage() {
  const { data, isLoading } = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })

  return (
    <div>
      <PageHeader
        title="Workspaces"
        description="Organize your documents by team or project"
        actions={<Button><Plus className="h-4 w-4" /> New Workspace</Button>}
      />
      {isLoading ? (
        <div className="grid grid-cols-3 gap-4">
          {Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-32" />)}
        </div>
      ) : (
        <div className="grid grid-cols-3 gap-4">
          {data?.map((ws) => (
            <Link
              key={ws.id}
              to="/workspaces/$workspaceId"
              params={{ workspaceId: ws.id }}
              className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-5 transition-shadow hover:shadow-md"
            >
              <div className="mb-3 flex items-center gap-3">
                <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-[var(--color-accent)]">
                  <FolderOpen className="h-5 w-5 text-[var(--color-primary)]" />
                </div>
                <h3 className="text-base font-semibold">{ws.name}</h3>
              </div>
              {ws.description && <p className="mb-3 text-sm text-[var(--color-text-secondary)] line-clamp-2">{ws.description}</p>}
              <div className="flex items-center gap-4 text-xs text-[var(--color-text-secondary)]">
                <span className="flex items-center gap-1"><FileText className="h-3.5 w-3.5" />{ws.document_count} docs</span>
                <span className="flex items-center gap-1"><Users className="h-3.5 w-3.5" />{ws.member_count} members</span>
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/')({ component: WorkspacesPage })
