import type { ComponentProps } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ChevronDown, RefreshCw } from 'lucide-react'

import { rerunOCR, type ForceEngine } from '@/api/ocr'
import { useAppMutation } from '@/hooks/useAppMutation'
import { cn } from '@/lib/cn'
import { Button } from '@/components/ui/shadcn/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/shadcn/dropdown-menu'
import { Spinner } from '@/components/ui/Spinner'

// Explicit engine the menu can force (undefined = Auto / per-config default).
type ForcibleEngine = Exclude<ForceEngine, 'auto'>

/**
 * Re-run OCR with an optional forced engine.
 *
 * Backend (services/intelligence/app/tasks/ocr.py:390) only acts on
 * `force_engine == "surya"` — every other value falls back to default
 * behavior. So this control exposes exactly the two meaningful modes:
 *
 *   - Auto (no forceEngine): the default pipeline (pymupdf fast-path
 *     if the PDF has a text layer; surya otherwise).
 *   - Surya (force): bypass the fast-path, always run surya. Useful
 *     when the auto pipeline produced poor text or skipped layout
 *     analysis.
 *
 * Split-button: the main click runs Auto; the chevron opens a menu
 * for explicit engine choice. Preserves the existing one-click UX
 * while making the engine option discoverable.
 */
interface Props {
  documentId: string
  versionId: string
  /** Caller-side permission gate; when false the component renders nothing. */
  canRerun: boolean
  variant?: ComponentProps<typeof Button>['variant']
  size?: ComponentProps<typeof Button>['size']
  /** Override the visible label (defaults to "Re-run OCR"). */
  label?: string
  /** Override the success toast copy. */
  successMessage?: string
  className?: string
  /** data-testid root; the chevron uses `${testId}-engine-menu`. */
  testId?: string
  /** Notify the parent when the re-queue request succeeds. Useful for
   *  suppressing the "OCR appears stuck" banner for a grace period
   *  after a re-run, since the upload-time-based stuck detection
   *  would otherwise still flag the doc as stuck. */
  onRerunSuccess?: () => void
}

export function RerunOcrButton({
  documentId, versionId, canRerun,
  variant = 'outline', size = 'sm',
  label = 'Re-run OCR',
  successMessage = 'OCR re-queued',
  className,
  testId = 'rerun-ocr',
  onRerunSuccess,
}: Props) {
  const qc = useQueryClient()

  const rerun = useAppMutation<unknown, unknown, ForcibleEngine | undefined>({
    mutationFn: (engine) =>
      rerunOCR(documentId, versionId, engine ? { forceEngine: engine } : {}),
    onSuccess: (_data, engine) => {
      const labels: Record<ForcibleEngine, string> = {
        surya: 'full page scan', printed: 'printed text', handwriting: 'handwriting (ICR)',
      }
      toast.success(
        engine ? `${successMessage} (engine: ${labels[engine]})` : successMessage,
      )
      qc.invalidateQueries({ queryKey: ['ocr', documentId, versionId] })
      onRerunSuccess?.()
    },
    defaultErrorMessage: 'Re-run failed',
  })

  if (!canRerun) return null

  const busy = rerun.isPending

  return (
    <div className={cn('inline-flex', className)} data-testid={testId}>
      <Button
        variant={variant}
        size={size}
        onClick={() => rerun.mutate(undefined)}
        disabled={busy}
        className="gap-1 rounded-e-none"
        data-testid={`${testId}-main`}
      >
        {busy ? <Spinner className="h-3 w-3" /> : <RefreshCw className="h-3 w-3" />}
        {label}
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant={variant}
            size={size}
            disabled={busy}
            className="rounded-s-none border-s-0 px-1.5"
            aria-label="Choose OCR engine"
            data-testid={`${testId}-engine-menu`}
          >
            <ChevronDown className="h-3 w-3" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-56">
          <DropdownMenuItem
            onSelect={() => rerun.mutate(undefined)}
            data-testid={`${testId}-engine-auto`}
          >
            <div>
              <p className="text-sm font-medium">Auto (default)</p>
              <p className="text-xs text-muted-foreground">Reuses the text already inside a PDF when there is one; scans the pages otherwise.</p>
            </div>
          </DropdownMenuItem>
          <DropdownMenuItem
            onSelect={() => rerun.mutate('printed')}
            data-testid={`${testId}-engine-printed`}
          >
            <div>
              <p className="text-sm font-medium">Printed (force)</p>
              <p className="text-xs text-muted-foreground">Always scan the pages, even when the PDF already contains text.</p>
            </div>
          </DropdownMenuItem>
          <DropdownMenuItem
            onSelect={() => rerun.mutate('handwriting')}
            data-testid={`${testId}-engine-handwriting`}
          >
            <div>
              <p className="text-sm font-medium">Handwriting (ICR)</p>
              <p className="text-xs text-muted-foreground">Recognises handwritten ink and merges it with the printed text. Best for forms &amp; notes.</p>
            </div>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}
