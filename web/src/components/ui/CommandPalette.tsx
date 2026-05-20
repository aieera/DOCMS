import { Command } from 'cmdk'
import { useEffect, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Search, FileText, FolderOpen, Settings, Tag, User, Clock } from 'lucide-react'

import { suggest } from '@/api/search'

// ADR 0084 — global Cmd+K palette wired to GET /search/suggest.
//
// Three live-fetched groups (documents / tags / people) plus the
// user's recent searches. cmdk handles keyboard nav (arrows + enter)
// + ARIA announcements for free; we only own the data + click
// targets.
//
// Debounce inside the useQuery key — TanStack Query naturally
// dedupes by key, so successive keystrokes produce one in-flight
// request per stable q value.

export function CommandPalette() {
  const [open, setOpen] = useState(false)
  const [q, setQ] = useState('')
  const navigate = useNavigate()

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault()
        setOpen(true)
      }
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [])

  // Reset state on close so a re-open doesn't show stale results.
  useEffect(() => {
    if (!open) setQ('')
  }, [open])

  // Debounce + minimum-prefix gate — only hit the backend when the
  // user has typed at least 2 chars. The recent-searches group
  // doesn't need the round-trip; rendered separately below.
  const enabled = q.length >= 2
  const { data } = useQuery({
    queryKey: ['suggest', q],
    queryFn: () => suggest(q, 5),
    enabled,
    staleTime: 10_000,
  })

  const go = (to: string) => {
    setOpen(false)
    navigate({ to })
  }

  if (!open) return null

  const recent = (data?.recent ?? []).slice(0, 5)
  const docs   = data?.documents ?? []
  const tags   = data?.tags ?? []
  const people = data?.people ?? []
  const empty  = enabled && data && !docs.length && !tags.length && !people.length

  return (
    <div className="fixed inset-0 z-[100]" data-testid="command-palette" onClick={() => setOpen(false)}>
      <div className="fixed inset-0 bg-black/50" />
      <div className="fixed start-1/2 top-[20%] z-[101] w-full max-w-lg -translate-x-1/2" onClick={(e) => e.stopPropagation()}>
        <Command className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] shadow-2xl"
          // shouldFilter=false: cmdk's built-in filter would re-rank
          // server-supplied groups by its own match algorithm. We
          // already get a relevance-ordered list back; let it stand.
          shouldFilter={false}
        >
          <div className="flex items-center gap-2 border-b border-[var(--color-border)] px-4">
            <Search className="h-4 w-4 text-[var(--color-text-secondary)]" />
            <Command.Input
              value={q}
              onValueChange={setQ}
              placeholder="Search documents, tags, people…"
              className="h-12 flex-1 bg-transparent text-sm outline-none placeholder:text-[var(--color-text-secondary)]"
              data-testid="command-palette-input"
            />
          </div>
          <Command.List className="max-h-96 overflow-y-auto p-2" data-testid="command-palette-list">
            {empty && (
              <Command.Empty className="p-4 text-center text-sm text-[var(--color-text-secondary)]">
                No matches for &ldquo;{q}&rdquo;
              </Command.Empty>
            )}

            {/* Recent searches first — the §7.5 spec calls this out. */}
            {recent.length > 0 && (
              <Command.Group
                heading="Recent"
                className="mb-2"
                data-testid="suggest-group-recent"
              >
                {recent.map((r) => (
                  <Item
                    key={`recent-${r.text}`}
                    icon={<Clock className="h-4 w-4 text-[var(--color-text-secondary)]" />}
                    label={r.text}
                    onSelect={() => {
                      setOpen(false)
                      navigate({ to: '/search', search: { q: r.text } })
                    }}
                    testId={`suggest-recent-${r.text}`}
                  />
                ))}
              </Command.Group>
            )}

            {docs.length > 0 && (
              <Command.Group
                heading="Documents"
                className="mb-2"
                data-testid="suggest-group-documents"
              >
                {docs.map((d) => (
                  <Item
                    key={`doc-${d.document_id}`}
                    icon={<FileText className="h-4 w-4 text-blue-500" />}
                    label={d.text}
                    // Docs route to the search page filtered to the
                    // exact title — workspace_id isn't on the suggest
                    // payload (kept terse), and a search hit click
                    // takes the user to the doc detail page anyway.
                    onSelect={() => {
                      setOpen(false)
                      navigate({ to: '/search', search: { q: d.text } })
                    }}
                    testId={`suggest-doc-${d.document_id}`}
                  />
                ))}
              </Command.Group>
            )}

            {tags.length > 0 && (
              <Command.Group
                heading="Tags"
                className="mb-2"
                data-testid="suggest-group-tags"
              >
                {tags.map((t) => (
                  <Item
                    key={`tag-${t.text}`}
                    icon={<Tag className="h-4 w-4 text-emerald-500" />}
                    label={t.text}
                    badge={`${t.count} doc${t.count === 1 ? '' : 's'}`}
                    onSelect={() => {
                      setOpen(false)
                      navigate({ to: '/search', search: { q: '', tag: [t.text] } })
                    }}
                    testId={`suggest-tag-${t.text}`}
                  />
                ))}
              </Command.Group>
            )}

            {people.length > 0 && (
              <Command.Group
                heading="People"
                className="mb-2"
                data-testid="suggest-group-people"
              >
                {people.map((p) => (
                  <Item
                    key={`person-${p.text}`}
                    icon={<User className="h-4 w-4 text-violet-500" />}
                    label={p.text}
                    badge={`${p.count} doc${p.count === 1 ? '' : 's'}`}
                    onSelect={() => {
                      setOpen(false)
                      navigate({ to: '/search', search: { q: '', author: [p.text] } })
                    }}
                    testId={`suggest-person-${p.text}`}
                  />
                ))}
              </Command.Group>
            )}

            {/* Static navigation — only when the user hasn't started
                typing, so the query results don't get crowded out. */}
            {!enabled && (
              <Command.Group heading="Navigation" data-testid="suggest-group-nav">
                <Item icon={<FileText className="h-4 w-4" />} label="Dashboard"  onSelect={() => go('/')} />
                <Item icon={<FolderOpen className="h-4 w-4" />} label="Workspaces" onSelect={() => go('/workspaces')} />
                <Item icon={<Search className="h-4 w-4" />} label="Search"      onSelect={() => go('/search')} />
                <Item icon={<Settings className="h-4 w-4" />} label="Admin"     onSelect={() => go('/admin')} />
              </Command.Group>
            )}
          </Command.List>
        </Command>
      </div>
    </div>
  )
}

function Item({ icon, label, badge, onSelect, testId }: {
  icon: React.ReactNode
  label: string
  badge?: string
  onSelect: () => void
  testId?: string
}) {
  return (
    <Command.Item
      onSelect={onSelect}
      className="flex cursor-pointer items-center justify-between gap-2 rounded-md px-2 py-1.5 text-sm aria-selected:bg-slate-100 dark:aria-selected:bg-slate-700"
      data-testid={testId}
    >
      <span className="flex items-center gap-2 truncate">
        {icon}
        <span className="truncate">{label}</span>
      </span>
      {badge && (
        <span className="shrink-0 text-xs text-[var(--color-text-secondary)]">{badge}</span>
      )}
    </Command.Item>
  )
}
