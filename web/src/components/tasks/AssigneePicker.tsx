// Multi-select people picker for tasks (2026-07-28 task-service design).
// Selected users render as removable avatar chips; typing searches the
// tenant people directory.
//
// Shares the ['mention-search', q] query key with the comment @mention
// autocomplete so the two surfaces reuse one cache — the directory
// endpoint is available to every authenticated user (unlike /admin/users,
// which is admin-gated and 403s for members).
import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Search, X } from 'lucide-react'

import { listUserDirectory, type DirectoryUser } from '@/api/auth'
import { Avatar } from '@/components/ui/shadcn/avatar'
import { Input } from '@/components/ui/shadcn/input'
import { cn } from '@/lib/cn'

export interface AssigneePickerProps {
  /** Selected user ids. */
  value: string[]
  onChange: (ids: string[]) => void
  disabled?: boolean
  /** Rendered above the control; omit for a bare picker. */
  label?: string
  /** Directory entries the caller already knows (avoids "Unknown user"
   *  chips for ids that a narrow search wouldn't return). */
  known?: DirectoryUser[]
}

export function AssigneePicker({
  value,
  onChange,
  disabled,
  label = 'Assignees',
  known = [],
}: AssigneePickerProps) {
  const [query, setQuery] = useState('')
  const [debounced, setDebounced] = useState('')

  useEffect(() => {
    const t = setTimeout(() => setDebounced(query.trim()), 250)
    return () => clearTimeout(t)
  }, [query])

  const { data: candidates = [], isFetching } = useQuery({
    queryKey: ['mention-search', debounced],
    queryFn: () => listUserDirectory(debounced),
    staleTime: 30_000,
  })

  // Chip labels come from whichever source knows the id: the caller's
  // `known` list, or anyone the directory has returned so far.
  const byId = useMemo(() => {
    const m = new Map<string, DirectoryUser>()
    for (const u of [...known, ...candidates]) m.set(u.id, u)
    return m
  }, [known, candidates])

  const add = (id: string) => {
    if (!value.includes(id)) onChange([...value, id])
    setQuery('')
  }
  const remove = (id: string) => onChange(value.filter((v) => v !== id))

  const unselected = candidates.filter((u) => !value.includes(u.id)).slice(0, 6)

  return (
    <div className="space-y-2" data-testid="assignee-picker">
      {label && <div className="text-sm font-medium">{label}</div>}

      {value.length > 0 && (
        <ul className="flex flex-wrap gap-1.5">
          {value.map((id) => {
            const u = byId.get(id)
            const name = u?.display_name || u?.email || 'Unknown user'
            return (
              <li key={id}>
                <span
                  className="inline-flex items-center gap-1.5 rounded-full bg-secondary py-0.5 pe-1 ps-1 text-xs text-secondary-foreground"
                  data-testid={`assignee-chip-${id}`}
                >
                  <Avatar name={name} size="sm" />
                  <span className="max-w-40 truncate">{name}</span>
                  {!disabled && (
                    <button
                      type="button"
                      onClick={() => remove(id)}
                      aria-label={`Remove ${name}`}
                      className="rounded-full p-0.5 hover:bg-muted"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  )}
                </span>
              </li>
            )
          })}
        </ul>
      )}

      {!disabled && (
        <div className="relative">
          <Search className="pointer-events-none absolute start-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search people…"
            aria-label="Search people to assign"
            className="ps-7"
            data-testid="assignee-search"
          />
          {query.trim() !== '' && (
            <ul
              className="absolute z-20 mt-1 max-h-56 w-full overflow-y-auto rounded-md border border-border bg-card shadow"
              data-testid="assignee-results"
            >
              {unselected.length === 0 ? (
                <li className="px-2 py-1.5 text-xs text-muted-foreground">
                  {isFetching ? 'Searching…' : 'No matches'}
                </li>
              ) : (
                unselected.map((u) => (
                  <li key={u.id}>
                    <button
                      type="button"
                      onClick={() => add(u.id)}
                      className={cn(
                        'flex w-full items-center gap-2 px-2 py-1.5 text-start text-sm',
                        'hover:bg-slate-100 dark:hover:bg-slate-800',
                      )}
                      data-testid={`assignee-option-${u.id}`}
                    >
                      <Avatar name={u.display_name || u.email} size="sm" />
                      <span className="truncate font-medium">{u.display_name || u.email}</span>
                      <span className="ms-auto truncate text-xs text-muted-foreground">{u.email}</span>
                    </button>
                  </li>
                ))
              )}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}
