import { useRef } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useDocuments } from '@/hooks/useDocuments'
import { useUpload } from '@/hooks/useUpload'
import { DocumentList } from '@/components/documents/DocumentList'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Upload } from 'lucide-react'

function WorkspacePage() {
  const { workspaceId } = Route.useParams()
  const { data, isLoading } = useDocuments(workspaceId)
  const { uploadFiles } = useUpload(workspaceId)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const onPick = () => fileInputRef.current?.click()
  const onChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files
    if (!files || files.length === 0) return
    uploadFiles(Array.from(files))
    // Reset so picking the same file twice in a row still fires.
    e.target.value = ''
  }

  return (
    <div>
      <PageHeader
        title="Workspace"
        actions={
          <>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              className="hidden"
              onChange={onChange}
            />
            <Button onClick={onPick}>
              <Upload className="h-4 w-4" /> Upload
            </Button>
          </>
        }
      />
      {/* Backend ListDocumentsResponse uses `documents`, not `items` —
          the PaginatedResponse<T> type's `items` field is wrong for
          this endpoint. Read both for resilience. */}
      <DocumentList
        documents={(data as unknown as { documents?: unknown[]; items?: unknown[] })?.documents
          ?? (data as unknown as { items?: unknown[] })?.items
          ?? []}
        isLoading={isLoading}
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/')({ component: WorkspacePage })
