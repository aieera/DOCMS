import { FolderClosed } from 'lucide-react'

import type { TrashLocation as Location } from '@/hooks/useTrash'

// TrashLocation — the "where will Restore put this back?" cell, shared
// by both trash tables (BUG-13: neither listed an original location, so
// a restore was a blind action).
//
// Deliberately degrades instead of guessing: an unresolvable folder
// (deleted in the same cascade, or past the lookup cap) shows the
// workspace alone, and an unresolvable workspace shows an em dash.
// Nothing here invents a location.
export function TrashLocation({ location }: { location: Location }) {
  const { workspaceName, folderName } = location
  if (!workspaceName && !folderName) return <span>—</span>
  return (
    /* block-level flex, not inline-flex: an inline-flex sizes to its
       content and ignores the td's max-width in auto table layout, so a
       long workspace name ran under the Size column (QA SD-12). A block
       flex fills the capped cell and lets the inner `truncate` work. */
    <span className="flex min-w-0 max-w-full items-center gap-1.5 overflow-hidden" title={[workspaceName, folderName].filter(Boolean).join(' / ')}>
      <FolderClosed className="h-3.5 w-3.5 shrink-0" aria-hidden />
      <span className="truncate">
        {workspaceName ?? 'Unknown workspace'}
        {folderName && (
          <>
            <span aria-hidden className="px-1 text-muted-foreground/50 rtl:rotate-180">›</span>
            {folderName}
          </>
        )}
      </span>
    </span>
  )
}
