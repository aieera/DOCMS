import { useState, type DragEvent } from 'react'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Sparkles, Upload } from 'lucide-react'

import { Button } from '@/components/ui/shadcn/button'
import { cn } from '@/lib/cn'

interface DashboardHeadProps {
  /** The h1, from `greet()`. */
  greeting: string
  /** `undefined` while the workspaces query is unsettled: the subline then
   * stays empty rather than claiming "0 workspaces". */
  workspaceCount: number | undefined
  onUpload: () => void
  /** When supplied, files dropped on the Upload button are handed here (the
   * button still opens the dialog on click). Without it the button is not a
   * drop target. */
  onFilesDropped?: (files: File[]) => void
}

/** A drag that carries files, not text or a link: only those may be dropped here. */
const carriesFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes('Files')

/**
 * The dashboard's head row: greeting and subline on the start side, the
 * Ask and Upload actions on the end side (stacked below the shell
 * breakpoint).
 */
export function DashboardHead({ greeting, workspaceCount, onUpload, onFilesDropped }: DashboardHeadProps) {
  const { t } = useTranslation('common')
  const [fileOver, setFileOver] = useState(false)

  // preventDefault on dragenter/dragover is what makes the button a valid drop
  // target; it is withheld for non-file drags so those are refused as usual.
  const onDragOver = (e: DragEvent) => {
    if (!carriesFiles(e)) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
    setFileOver(true)
  }
  const dropTarget = onFilesDropped
    ? {
        onDragEnter: onDragOver,
        onDragOver,
        // The button's only child element is its icon, and Button sets
        // pointer-events-none on svgs, so a drag never "leaves" onto a child.
        onDragLeave: () => setFileOver(false),
        onDrop: (e: DragEvent) => {
          // Always stop the browser opening the file, even if there is nothing to hand over.
          e.preventDefault()
          setFileOver(false)
          const files = Array.from(e.dataTransfer?.files ?? [])
          if (files.length > 0) onFilesDropped(files)
        },
      }
    : {}

  return (
    <div className="flex flex-col gap-4 shell:flex-row shell:items-end shell:justify-between">
      <div className="min-w-0">
        {/* dir="auto", as in PageHeader: a Latin first name inside an Arabic
            greeting (or the reverse) resolves its own direction. */}
        <h1 dir="auto" className="page-title text-foreground">{greeting}</h1>
        {/* The slot always renders (min-h-5 = one 14/20 line) so the head
            keeps its height when the count arrives. */}
        <p dir="auto" className="mt-1 min-h-5 text-sm text-muted-foreground">
          {workspaceCount === undefined ? null : t('dashboard.subline', { count: workspaceCount })}
        </p>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <Button variant="outline" asChild>
          <Link to="/ask">
            <Sparkles aria-hidden />
            {t('dashboard.ask_question')}
          </Link>
        </Button>
        <Button
          onClick={onUpload}
          data-testid="header-upload-button"
          className={cn(fileOver && 'ring-2 ring-ring')}
          {...dropTarget}
        >
          <Upload aria-hidden />
          {t('dashboard.upload')}
        </Button>
      </div>
    </div>
  )
}
