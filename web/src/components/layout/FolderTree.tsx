import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from '@tanstack/react-router'
import { Folder, FolderOpen, Sparkles, Lock, Users, Globe, ShieldCheck, AlertTriangle } from 'lucide-react'
import { getFolders } from '@/api/workspaces'
import { findRootFolder } from '@/lib/rootFolder'
import { listSmartFolders, type SavedSearch, type TreeVisibility } from '@/api/savedSearches'
import { useUIStore } from '@/store/uiStore'
import { cn } from '@/lib/cn'
import type { Folder as FolderType } from '@/types/api'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import {
  ContextMenu, ContextMenuTrigger, ContextMenuContent, ContextMenuItem,
} from '@/components/ui/shadcn/context-menu'
import { ManageAccessDialog } from '@/components/documents/ManageAccessDialog'

function FolderNode({ folder, workspaceId, depth }: { folder: FolderType; workspaceId: string; depth: number }) {
  const { t } = useTranslation('common')
  const [expanded, setExpanded] = useState(false)
  const [manageAccessOpen, setManageAccessOpen] = useState(false)
  const { activeFolderId, setActiveFolder } = useUIStore()
  const active = activeFolderId === folder.id
  const { data: children } = useQuery({
    queryKey: ['folders', workspaceId, folder.id],
    queryFn: () => getFolders(workspaceId, folder.id),
    enabled: expanded,
  })

  return (
    <div>
      <ContextMenu>
        <ContextMenuTrigger asChild>
          <button
            onClick={() => { setActiveFolder(folder.id); if ((folder.child_folder_count ?? folder.children_count ?? 0) > 0) setExpanded(!expanded) }}
            className={cn(
              'flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-sm transition-colors hover:bg-slate-100 dark:hover:bg-slate-800',
              active && 'bg-[var(--color-accent)] text-[var(--color-primary)] font-medium',
            )}
            style={{ paddingInlineStart: `${depth * 16 + 8}px` }}
            data-testid={`folder-node-${folder.id}`}
          >
            {(folder.child_folder_count ?? folder.children_count ?? 0) > 0 && (
              <DirectionalIcon name="ChevronRight" className={cn('h-3.5 w-3.5 transition-transform', expanded && 'rotate-90')} />
            )}
            {expanded ? <FolderOpen className="h-4 w-4" /> : <Folder className="h-4 w-4" />}
            <span className="truncate">{folder.name}</span>
            {folder.document_count > 0 && <span className="ms-auto text-xs text-[var(--color-text-secondary)]">{folder.document_count}</span>}
          </button>
        </ContextMenuTrigger>
        <ContextMenuContent data-testid={`folder-context-menu-${folder.id}`}>
          <ContextMenuItem
            onSelect={() => setManageAccessOpen(true)}
            data-testid={`folder-context-action-manage-access-${folder.id}`}
          >
            <ShieldCheck className="h-4 w-4" />
            <span>{t('folder_tree.manage_access') ?? 'Manage access…'}</span>
          </ContextMenuItem>
        </ContextMenuContent>
      </ContextMenu>
      {expanded && children?.map((c) => <FolderNode key={c.id} folder={c} workspaceId={workspaceId} depth={depth + 1} />)}
      <ManageAccessDialog
        open={manageAccessOpen}
        onOpenChange={setManageAccessOpen}
        resourceType="folder"
        resourceId={folder.id}
        resourceTitle={folder.name}
        workspaceId={workspaceId}
      />
    </div>
  )
}

// ADR 0100 — smart folder section. Renders below the regular folder
// list. Each smart folder links to the search route, pre-filled with
// the saved query. Visibility is shown as a small lock/users/globe
// glyph so the user knows whether a folder is theirs alone, shared
// with the workspace, or visible to the whole tenant.
function SmartFolderNode({ sf }: { sf: SavedSearch }) {
  const Vis = visibilityIcon(sf.tree_visibility)
  return (
    <Link
      to="/search"
      search={{ q: sf.query, saved: sf.id } as any}
      className="flex items-center gap-1.5 rounded-md px-2 py-1 text-sm text-[var(--color-text-secondary)] transition-colors hover:bg-slate-100 dark:hover:bg-slate-800"
      data-testid={`smart-folder-${sf.id}`}
    >
      <Sparkles className="h-3.5 w-3.5 text-violet-500" />
      <span className="truncate">{sf.name}</span>
      <Vis className="ms-auto h-3 w-3 opacity-60" aria-label={sf.tree_visibility ?? 'private'} />
    </Link>
  )
}

function visibilityIcon(v?: TreeVisibility) {
  if (v === 'workspace') return Users
  if (v === 'public') return Globe
  return Lock
}

export function FolderTree({ workspaceId }: { workspaceId: string }) {
  const { t } = useTranslation('common')
  const { data: folders, isError } = useQuery({
    queryKey: ['folders', workspaceId, 'root'],
    queryFn: () => getFolders(workspaceId),
    enabled: !!workspaceId,
  })
  // Smart folders are loaded regardless of the workspace folder fetch
  // because they cross-cut workspaces. Empty list → section hidden.
  const { data: smartFolders } = useQuery({
    queryKey: ['smart-folders'],
    queryFn: listSmartFolders,
    staleTime: 30_000,
  })

  return (
    <div className="space-y-3">
      <div className="space-y-0.5">
        {/* Wave 5 pattern 3: getFolders throws on a malformed response
            (was silently returning []). Distinguish the error from a
            genuinely empty workspace so the tree doesn't read as "No
            folders" when the fetch actually failed — cf. VersionHistory. */}
        {isError ? (
          <p className="flex items-center gap-1.5 px-3 py-2 text-xs text-destructive" role="alert">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden />
            {t('folder_tree.load_error') ?? "Couldn't load folders"}
          </p>
        ) : folders?.length ? (
          // The designated root folder is the workspace root itself (the
          // browser shows its files at root level), so its node would be
          // a duplicate of the workspace entry above it.
          folders
            .filter((f) => f.id !== findRootFolder(folders)?.id)
            .map((f) => <FolderNode key={f.id} folder={f} workspaceId={workspaceId} depth={0} />)
        ) : (
          <p className="px-3 py-2 text-xs text-[var(--color-text-secondary)]">{t('folder_tree.empty') ?? 'No folders'}</p>
        )}
      </div>

      {smartFolders && smartFolders.length > 0 && (
        <div>
          <div className="flex items-center gap-1 px-2 pb-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--color-text-secondary)]">
            <Sparkles className="h-3 w-3" />
            {t('sidebar.smart_folders') ?? 'Smart folders'}
          </div>
          <div className="space-y-0.5">
            {smartFolders.map((sf) => <SmartFolderNode key={sf.id} sf={sf} />)}
          </div>
        </div>
      )}
    </div>
  )
}
