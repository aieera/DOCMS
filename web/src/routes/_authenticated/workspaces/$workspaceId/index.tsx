import { createFileRoute } from '@tanstack/react-router'
import { useDocuments } from '@/hooks/useDocuments'
import { DocumentList } from '@/components/documents/DocumentList'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Upload } from 'lucide-react'

function WorkspacePage() {
  const { workspaceId } = Route.useParams()
  const { data, isLoading } = useDocuments(workspaceId)

  return (
    <div>
      <PageHeader
        title="Workspace"
        actions={<Button><Upload className="h-4 w-4" /> Upload</Button>}
      />
      <DocumentList documents={data?.items} isLoading={isLoading} />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/')({ component: WorkspacePage })
