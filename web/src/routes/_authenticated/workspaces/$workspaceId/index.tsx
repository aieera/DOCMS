import { useRef, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useDocuments } from '@/hooks/useDocuments'
import { useUpload } from '@/hooks/useUpload'
import { DocumentList } from '@/components/documents/DocumentList'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Upload, Sparkles } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
import { WorkspaceAISettingsDialog } from '@/components/intelligence/WorkspaceAISettings'

function WorkspacePage() {
  const { workspaceId } = Route.useParams()
  const { data, isLoading } = useDocuments(workspaceId)
  const { uploadFiles } = useUpload(workspaceId)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [aiOpen, setAiOpen] = useState(false)
  const role = useAuthStore((s) => s.user?.role)
  // Workspace AI settings are admin-curated; matches the policy
  // service's owner|admin gate on PUT. Hide for non-admins so they
  // don't see a button that always 403s.
  const isAdmin = role === 'owner' || role === 'admin'

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
            {isAdmin && (
              <Button
                variant="ghost"
                onClick={() => setAiOpen(true)}
                data-testid="open-ai-settings"
              >
                <Sparkles className="h-4 w-4" /> AI settings
              </Button>
            )}
            <Button onClick={onPick}>
              <Upload className="h-4 w-4" /> Upload
            </Button>
          </>
        }
      />
      <WorkspaceAISettingsDialog
        open={aiOpen}
        onOpenChange={setAiOpen}
        workspaceId={workspaceId}
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
