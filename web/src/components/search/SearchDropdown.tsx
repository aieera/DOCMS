import { Bell, Bookmark, Clock, Star, X } from 'lucide-react'

import type { SavedSearch } from '@/api/savedSearches'

interface Props {
  recents: string[]
  saved: SavedSearch[]
  onRun: (query: string) => void
  onSaveRecent: (query: string) => void
  onUnsave: (id: string) => void
  onRemoveRecent: (query: string) => void
  /** Navigate to the full management page (alerts, subscribers, smart folders). */
  onManage?: () => void
}

// Dropdown body for the topbar search: recent queries (star to keep)
// above the persisted saved searches (click to run, star to remove).
// Deeper management — alerts, subscribers, smart folders — stays on
// /saved-searches, linked from the footer.
export function SearchDropdown({ recents, saved, onRun, onSaveRecent, onUnsave, onRemoveRecent, onManage }: Props) {
  const savedQueries = new Set(saved.map((s) => s.query))
  const freshRecents = recents.filter((r) => !savedQueries.has(r))
  if (freshRecents.length === 0 && saved.length === 0) return null

  return (
    <div
      className="overflow-hidden rounded-xl bg-card text-foreground shadow-neu"
      data-testid="search-dropdown"
    >
      {freshRecents.length > 0 && (
        <section>
          <h4 className="px-3 pb-1 pt-2.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
            Recent
          </h4>
          <ul>
            {freshRecents.map((q) => (
              <li key={q} className="group flex items-center gap-2 px-1.5">
                <button
                  type="button"
                  onClick={() => onRun(q)}
                  className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 py-1.5 text-start text-sm hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <Clock className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  <span className="truncate">{q}</span>
                </button>
                <button
                  type="button"
                  onClick={() => onSaveRecent(q)}
                  aria-label={`Save search "${q}"`}
                  title="Save this search"
                  className="rounded-md p-1.5 text-muted-foreground opacity-0 transition-opacity hover:bg-muted/60 hover:text-foreground focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring group-hover:opacity-100"
                >
                  <Star className="h-3.5 w-3.5" />
                </button>
                <button
                  type="button"
                  onClick={() => onRemoveRecent(q)}
                  aria-label={`Remove recent search "${q}"`}
                  title="Remove from recent"
                  className="rounded-md p-1.5 text-muted-foreground opacity-0 transition-opacity hover:bg-muted/60 hover:text-foreground focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring group-hover:opacity-100"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}

      {saved.length > 0 && (
        <section className={freshRecents.length > 0 ? 'border-t border-border' : undefined}>
          <h4 className="px-3 pb-1 pt-2.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
            Saved
          </h4>
          <ul>
            {saved.map((s) => (
              <li key={s.id} className="group flex items-center gap-2 px-1.5">
                <button
                  type="button"
                  onClick={() => onRun(s.query)}
                  className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 py-1.5 text-start text-sm hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <Bookmark className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  <span className="truncate">{s.name}</span>
                  {s.query !== s.name && (
                    <span className="truncate text-xs text-muted-foreground">{s.query}</span>
                  )}
                  {s.notify && (
                    <Bell aria-label="Alert enabled" className="h-3 w-3 shrink-0 text-primary" />
                  )}
                </button>
                <button
                  type="button"
                  onClick={() => onUnsave(s.id)}
                  aria-label={`Remove saved search "${s.name}"`}
                  title="Remove from saved"
                  className="rounded-md p-1.5 text-muted-foreground opacity-0 transition-opacity hover:bg-muted/60 hover:text-foreground focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring group-hover:opacity-100"
                >
                  <Star className="h-3.5 w-3.5 fill-current text-warning" />
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}

      {onManage && (
        <div className="border-t border-border px-3 py-1.5">
          <button
            type="button"
            onClick={onManage}
            className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            Manage saved searches &amp; alerts →
          </button>
        </div>
      )}
    </div>
  )
}
