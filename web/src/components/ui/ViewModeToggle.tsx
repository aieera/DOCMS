import { LayoutGrid, List } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/cn'

export type ViewMode = 'grid' | 'list'

// Single-button grid/list switch: shows the view you'll switch TO
// (Drive-style) and flips on every click.
export function ViewModeToggle({
  value, onChange, className,
}: { value: ViewMode; onChange: (mode: ViewMode) => void; className?: string }) {
  const { t } = useTranslation('common')
  const next: ViewMode = value === 'grid' ? 'list' : 'grid'
  const label = next === 'list' ? t('actions.switch_list') : t('actions.switch_grid')
  return (
    <button
      type="button"
      onClick={() => onChange(next)}
      className={cn(
        'inline-flex items-center justify-center rounded-xl bg-muted p-1 text-muted-foreground transition-colors',
        'hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        className,
      )}
      aria-label={label}
      title={label}
      data-testid="view-mode-toggle"
    >
      <span className="flex h-6 w-6 items-center justify-center rounded-lg bg-background text-primary shadow-xs">
        {next === 'list' ? <List className="h-4 w-4" /> : <LayoutGrid className="h-4 w-4" />}
      </span>
    </button>
  )
}
