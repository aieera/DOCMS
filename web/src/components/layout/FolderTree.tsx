import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getFolders } from '@/api/workspaces'
import { useUIStore } from '@/store/uiStore'
import { ChevronRight, Folder, FolderOpen } from 'lucide-react'
import { cn } from '@/lib/cn'
import type { Folder as FolderType } from '@/types/api'

function FolderNode({ folder, workspaceId, depth }: { folder: FolderType; workspaceId: string; depth: number }) {
  const [expanded, setExpanded] = useState(false)
  const { activeFolderId, setActiveFolder } = useUIStore()
  const active = activeFolderId === folder.id
  const { data: children } = useQuery({
    queryKey: ['folders', workspaceId, folder.id],
    queryFn: () => getFolders(workspaceId, folder.id),
    enabled: expanded,
  })

  return (
    <div>
      <button
        onClick={() => { setActiveFolder(folder.id); if (folder.children_count > 0) setExpanded(!expanded) }}
        className={cn(
          'flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-sm transition-colors hover:bg-slate-100 dark:hover:bg-slate-800',
          active && 'bg-[var(--color-accent)] text-[var(--color-primary)] font-medium',
        )}
        style={{ paddingInlineStart: `${depth * 16 + 8}px` }}
      >
        {folder.children_count > 0 && (
          <ChevronRight className={cn('h-3.5 w-3.5 transition-transform', expanded && 'rotate-90')} />
        )}
        {expanded ? <FolderOpen className="h-4 w-4" /> : <Folder className="h-4 w-4" />}
        <span className="truncate">{folder.name}</span>
        {folder.document_count > 0 && <span className="ms-auto text-xs text-[var(--color-text-secondary)]">{folder.document_count}</span>}
      </button>
      {expanded && children?.map((c) => <FolderNode key={c.id} folder={c} workspaceId={workspaceId} depth={depth + 1} />)}
    </div>
  )
}

export function FolderTree({ workspaceId }: { workspaceId: string }) {
  const { data: folders } = useQuery({
    queryKey: ['folders', workspaceId, 'root'],
    queryFn: () => getFolders(workspaceId),
    enabled: !!workspaceId,
  })
  if (!folders?.length) return <p className="px-3 py-2 text-xs text-[var(--color-text-secondary)]">No folders</p>
  return <div className="space-y-0.5">{folders.map((f) => <FolderNode key={f.id} folder={f} workspaceId={workspaceId} depth={0} />)}</div>
}
