import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Search, GitBranch } from 'lucide-react'
import { cn } from '@/lib/cn'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/shadcn/dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import type { WorkflowDefinition } from '@/api/workflows'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  templates: WorkflowDefinition[]
  isLoading: boolean
  onPick: (templateId: string) => void
  isStarting: boolean
}

// StartWorkflowPicker is the modal that opens from the doc-detail
// Workflow tab's empty-state. Lists active templates with a search
// filter and a "Start" button that fires the attach mutation in the
// parent. Steps preview shows the type chain so the user knows what
// they're about to commit to.
export function StartWorkflowPicker({
  open,
  onOpenChange,
  templates,
  isLoading,
  onPick,
  isStarting,
}: Props) {
  const { t } = useTranslation('workflows')
  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const visible = templates
    .filter((d) => !query || matches(d, query))
    // Most-recent first so a fresh save lands at the top.
    .sort((a, b) => (b.updated_at ?? b.created_at).localeCompare(a.updated_at ?? a.created_at))

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('tab.picker_title')}</DialogTitle>
          <DialogDescription>{t('tab.picker_description')}</DialogDescription>
        </DialogHeader>

        <div className="relative">
          <Search className="pointer-events-none absolute start-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('tab.picker_search')}
            className="ps-9"
            data-testid="workflow-picker-search"
          />
        </div>

        <div className="max-h-80 overflow-y-auto">
          {isLoading ? (
            <div className="flex justify-center py-8">
              <Spinner className="h-5 w-5" />
            </div>
          ) : visible.length === 0 ? (
            <p className="rounded-md border border-dashed border-border bg-muted/30 p-6 text-center text-sm text-muted-foreground">
              {t('tab.picker_empty')}
            </p>
          ) : (
            <ul className="space-y-2" data-testid="workflow-picker-list">
              {visible.map((d) => {
                const active = selectedId === d.id
                return (
                  <li key={d.id}>
                    <button
                      type="button"
                      onClick={() => setSelectedId(d.id)}
                      className={cn(
                        'flex w-full items-start gap-3 rounded-xl border p-3 text-start transition-colors',
                        active
                          ? 'border-primary bg-primary/10'
                          : 'border-border hover:border-primary/40 hover:bg-muted/40',
                      )}
                      data-testid={`workflow-picker-template-${d.id}`}
                    >
                      <span
                        className={cn(
                          'mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                          active ? 'bg-primary text-primary-foreground' : 'bg-muted text-muted-foreground',
                        )}
                        aria-hidden
                      >
                        <GitBranch className="h-4 w-4" />
                      </span>
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-sm font-medium">{d.name}</p>
                        {d.description && (
                          <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
                            {d.description}
                          </p>
                        )}
                        <p className="mt-1 text-[10px] text-muted-foreground">
                          {t('templates.card.steps_count', { count: d.steps?.length ?? 0 })}
                        </p>
                      </div>
                    </button>
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={isStarting}>
            {t('common.cancel', { defaultValue: 'Cancel', ns: 'common' })}
          </Button>
          <Button
            onClick={() => selectedId && onPick(selectedId)}
            disabled={!selectedId || isStarting}
            data-testid="workflow-picker-start"
          >
            {isStarting ? <Spinner className="me-1 h-3.5 w-3.5" /> : null}
            {t('tab.picker_start')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function matches(d: WorkflowDefinition, query: string): boolean {
  const q = query.toLowerCase()
  return (
    d.name.toLowerCase().includes(q) ||
    (d.description?.toLowerCase().includes(q) ?? false)
  )
}
