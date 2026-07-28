import { LayoutGrid, List } from 'lucide-react'
import { cn } from '@/lib/cn'

export type ViewMode = 'grid' | 'list'

// Single-button grid/list switch: shows the view you'll switch TO
// (Drive-style) and flips on every click.
export function ViewModeToggle({
  value, onChange, className,
}: { value: ViewMode; onChange: (mode: ViewMode) => void; className?: string }) {
  const next: ViewMode = value === 'grid' ? 'list' : 'grid'
  const label = next === 'list' ? 'Switch to list view' : 'Switch to grid view'
  return (
    <button
      type="button"
      onClick={() => onChange(next)}
      className={cn(
        'rounded-md border border-border bg-background p-2 text-muted-foreground transition-colors',
        'hover:bg-muted/50 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
        className,
      )}
      aria-label={label}
      title={label}
      data-testid="view-mode-toggle"
    >
      {next === 'list' ? <List className="h-4 w-4" /> : <LayoutGrid className="h-4 w-4" />}
    </button>
  )
}
