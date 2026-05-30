import { useTranslation } from 'react-i18next'
import { Folder as FolderIcon, Home } from 'lucide-react'
import { cn } from '@/lib/cn'
import type { Folder } from '@/types/api'

interface Props {
  // Ancestors ordered root → … → parent. Empty array when the
  // viewer is sitting at the workspace root (no folder selected).
  ancestors: Folder[]
  // Current folder being viewed, or null when at the workspace root.
  current: Folder | null
  workspaceName: string
  onNavigate: (folderId: string | null) => void
}

// FolderBreadcrumbs renders the path from the workspace root to the
// current folder. Each crumb is clickable — clicking a parent flips
// the URL ?folder= back to that id (or null for the workspace root)
// so the workspace view re-fetches. RTL: the chevrons use a
// directional character so Arabic renders right-to-left without an
// extra branch.
export function FolderBreadcrumbs({ ancestors, current, workspaceName, onNavigate }: Props) {
  const { t } = useTranslation('folders')
  if (!current && ancestors.length === 0) return null
  return (
    <nav
      aria-label={t('breadcrumb_label')}
      className="flex flex-wrap items-center gap-1 text-sm"
      data-testid="folder-breadcrumbs"
    >
      <Crumb
        label={workspaceName}
        icon={<Home className="h-3.5 w-3.5" />}
        onClick={() => onNavigate(null)}
        muted
      />
      {ancestors.map((a) => (
        <span key={a.id} className="flex items-center gap-1">
          <Separator />
          <Crumb
            label={a.name}
            icon={<FolderIcon className="h-3.5 w-3.5" />}
            onClick={() => onNavigate(a.id)}
            muted
          />
        </span>
      ))}
      {current && (
        <span className="flex items-center gap-1">
          <Separator />
          <Crumb
            label={current.name}
            icon={<FolderIcon className="h-3.5 w-3.5" />}
            onClick={() => onNavigate(current.id)}
          />
        </span>
      )}
    </nav>
  )
}

function Crumb({
  label,
  icon,
  onClick,
  muted = false,
}: {
  label: string
  icon: React.ReactNode
  onClick: () => void
  muted?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1 rounded-md px-2 py-1 text-sm transition-colors hover:bg-muted/60',
        muted ? 'text-muted-foreground' : 'font-medium text-foreground',
      )}
    >
      {icon}
      <span className="max-w-[20ch] truncate">{label}</span>
    </button>
  )
}

function Separator() {
  return (
    <span
      aria-hidden
      className="select-none text-muted-foreground/40 rtl:rotate-180"
    >
      ›
    </span>
  )
}
