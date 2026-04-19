import { useCallback } from 'react'
import { useDropzone } from 'react-dropzone'
import { Upload } from 'lucide-react'
import { useUpload } from '@/hooks/useUpload'
import { cn } from '@/lib/cn'

interface Props { workspaceId?: string; folderId?: string }

export function DocumentUpload({ workspaceId, folderId }: Props) {
  const { uploadFiles } = useUpload(workspaceId, folderId)

  const onDrop = useCallback((files: File[]) => { uploadFiles(files) }, [uploadFiles])

  const { getRootProps, getInputProps, isDragActive } = useDropzone({ onDrop, noClick: false })

  return (
    <div
      {...getRootProps()}
      className={cn(
        'flex flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed p-12 text-center transition-colors',
        isDragActive ? 'border-[var(--color-primary)] bg-[var(--color-accent)]' : 'border-[var(--color-border)] hover:border-[var(--color-primary)]/50',
      )}
    >
      <input {...getInputProps()} />
      <Upload className={cn('h-10 w-10', isDragActive ? 'text-[var(--color-primary)]' : 'text-[var(--color-text-secondary)]')} />
      <div>
        <p className="font-medium">{isDragActive ? 'Drop files here' : 'Drag & drop files, or click to browse'}</p>
        <p className="mt-1 text-sm text-[var(--color-text-secondary)]">PDF, Office, images, video — up to 5 GB per file</p>
      </div>
    </div>
  )
}
