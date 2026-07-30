// Multi-select document picker for tasks (2026-07-28 task-service
// design). Selected documents render as removable chips carrying their
// title; typing searches via the ADR 0084 grouped autocomplete endpoint,
// whose `documents` group returns {text, document_id}.
//
// The picker only ever deals in {document_id, title}. workspace_id — the
// other half of a document deep link — is resolved server-side when the
// task links the document, and comes back on the task's documents[].
import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { FileText, Search, X } from 'lucide-react'

import { suggest } from '@/api/search'
import { Input } from '@/components/ui/shadcn/input'
import { cn } from '@/lib/cn'

export interface PickedDocument {
  document_id: string
  title: string
}

export interface DocumentPickerProps {
  value: PickedDocument[]
  onChange: (docs: PickedDocument[]) => void
  disabled?: boolean
  label?: string
}

export function DocumentPicker({
  value,
  onChange,
  disabled,
  label = 'Documents',
}: DocumentPickerProps) {
  const [query, setQuery] = useState('')
  const [debounced, setDebounced] = useState('')

  useEffect(() => {
    const t = setTimeout(() => setDebounced(query.trim()), 250)
    return () => clearTimeout(t)
  }, [query])

  const { data, isFetching } = useQuery({
    queryKey: ['task-document-search', debounced],
    queryFn: () => suggest(debounced, 8),
    enabled: debounced.length > 0,
    staleTime: 30_000,
  })

  const selectedIds = new Set(value.map((d) => d.document_id))
  const results = (data?.documents ?? []).filter((d) => !selectedIds.has(d.document_id)).slice(0, 6)

  const add = (doc: PickedDocument) => {
    if (!selectedIds.has(doc.document_id)) onChange([...value, doc])
    setQuery('')
  }
  const remove = (id: string) => onChange(value.filter((d) => d.document_id !== id))

  return (
    <div className="space-y-2" data-testid="document-picker">
      {label && <div className="text-sm font-medium">{label}</div>}

      {value.length > 0 && (
        <ul className="flex flex-wrap gap-1.5">
          {value.map((d) => (
            <li key={d.document_id}>
              <span
                className="inline-flex items-center gap-1.5 rounded-full bg-secondary px-2 py-0.5 text-xs text-secondary-foreground"
                data-testid={`document-chip-${d.document_id}`}
              >
                <FileText className="h-3 w-3" />
                <span className="max-w-52 truncate">{d.title}</span>
                {!disabled && (
                  <button
                    type="button"
                    onClick={() => remove(d.document_id)}
                    aria-label={`Remove ${d.title}`}
                    className="rounded-full p-0.5 hover:bg-muted"
                  >
                    <X className="h-3 w-3" />
                  </button>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}

      {!disabled && (
        <div className="relative">
          <Search className="pointer-events-none absolute start-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search documents…"
            aria-label="Search documents to link"
            className="ps-7"
            data-testid="document-search"
          />
          {debounced !== '' && (
            <ul
              className="absolute z-20 mt-1 max-h-56 w-full overflow-y-auto rounded-md border border-border bg-card shadow"
              data-testid="document-results"
            >
              {results.length === 0 ? (
                <li className="px-2 py-1.5 text-xs text-muted-foreground">
                  {isFetching ? 'Searching…' : 'No matches'}
                </li>
              ) : (
                results.map((d) => (
                  <li key={d.document_id}>
                    <button
                      type="button"
                      onClick={() => add({ document_id: d.document_id, title: d.text })}
                      className={cn(
                        'flex w-full items-center gap-2 px-2 py-1.5 text-start text-sm',
                        'hover:bg-slate-100 dark:hover:bg-slate-800',
                      )}
                      data-testid={`document-option-${d.document_id}`}
                    >
                      <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      <span className="truncate">{d.text}</span>
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
