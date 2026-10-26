import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Sparkles, Upload } from 'lucide-react'

import { Button } from '@/components/ui/shadcn/button'

interface DashboardHeadProps {
  /** The h1, from `greet()`. */
  greeting: string
  /** `undefined` while the workspaces query is unsettled: the subline then
   * stays empty rather than claiming "0 workspaces". */
  workspaceCount: number | undefined
  onUpload: () => void
}

/**
 * The dashboard's head row: greeting and subline on the start side, the
 * Ask and Upload actions on the end side (stacked below the shell
 * breakpoint).
 */
export function DashboardHead({ greeting, workspaceCount, onUpload }: DashboardHeadProps) {
  const { t } = useTranslation('common')
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
        <Button onClick={onUpload} data-testid="header-upload-button">
          <Upload aria-hidden />
          {t('dashboard.upload')}
        </Button>
      </div>
    </div>
  )
}
