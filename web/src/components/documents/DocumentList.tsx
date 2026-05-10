import { useUIStore } from '@/store/uiStore'
import { Grid, List, Upload } from 'lucide-react'
import { Button } from '@/components/ui/shadcn/button'
import { DocumentCard } from './DocumentCard'
import { EmptyState } from '@/components/ui/EmptyState'
import { Skeleton } from '@/components/ui/Skeleton'
import type { Document } from '@/types/api'

interface Props {
  documents: Document[] | undefined
  isLoading: boolean
  onUploadClick?: () => void
}

export function DocumentList({ documents, isLoading, onUploadClick }: Props) {
  const { viewMode, setViewMode } = useUIStore()

  if (isLoading) {
    return (
      <div className="grid grid-cols-3 gap-4">
        {Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-28" />)}
      </div>
    )
  }

  if (!documents?.length) {
    return (
      <EmptyState
        title="No documents yet"
        description="Upload your first document to get started"
        actionLabel="Upload"
        onAction={onUploadClick}
      />
    )
  }

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <span className="text-sm text-[var(--color-text-secondary)]">{documents.length} documents</span>
        <div className="flex items-center gap-1">
          <button onClick={() => setViewMode('grid')} className={`rounded-md p-1.5 ${viewMode === 'grid' ? 'bg-slate-200 dark:bg-slate-700' : ''}`} aria-label="Grid view">
            <Grid className="h-4 w-4" />
          </button>
          <button onClick={() => setViewMode('table')} className={`rounded-md p-1.5 ${viewMode === 'table' ? 'bg-slate-200 dark:bg-slate-700' : ''}`} aria-label="Table view">
            <List className="h-4 w-4" />
          </button>
          {onUploadClick && <Button variant="default" size="sm" onClick={onUploadClick}><Upload className="h-4 w-4" /> Upload</Button>}
        </div>
      </div>
      <div className={viewMode === 'grid' ? 'grid grid-cols-3 gap-4' : 'flex flex-col gap-2'}>
        {documents.map((d) => <DocumentCard key={d.id} doc={d} />)}
      </div>
    </div>
  )
}
