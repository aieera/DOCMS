import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useDocuments } from '@/hooks/useDocuments'
import { DocumentList } from '@/components/documents/DocumentList'
import { DocumentUpload } from '@/components/documents/DocumentUpload'
import { UploadProgress } from '@/components/documents/UploadProgress'
import { PageHeader } from '@/components/shared/PageHeader'
import { getFolders } from '@/api/workspaces'

// The workspace landing page lists docs and exposes the upload dropzone
// for the workspace's first folder. A richer folder navigator is a
// follow-up; until that ships, "Shared Documents" (the seed default
// folder per workspace) is a reasonable target so /storage/uploads/
// initiate has a valid folder_id.
function WorkspacePage() {
  const { workspaceId } = Route.useParams()
  const { data, isLoading } = useDocuments(workspaceId)
  const { data: folders } = useQuery({
    queryKey: ['workspaces', workspaceId, 'folders'],
    queryFn: () => getFolders(workspaceId),
  })
  const targetFolder = folders?.[0]

  return (
    <div className="space-y-6">
      <PageHeader
        title="Workspace"
        description={
          targetFolder
            ? `Drop files to upload into "${targetFolder.name}".`
            : 'No folder yet — create one before uploading.'
        }
      />
      {targetFolder && (
        <DocumentUpload workspaceId={workspaceId} folderId={targetFolder.id} />
      )}
      <DocumentList documents={data?.items} isLoading={isLoading} />
      <UploadProgress />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/')({ component: WorkspacePage })
