import { Command } from 'cmdk'
import { useEffect, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { Search, FileText, FolderOpen, Settings } from 'lucide-react'

export function CommandPalette() {
  const [open, setOpen] = useState(false)
  const navigate = useNavigate()

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') { e.preventDefault(); setOpen(true) }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [])

  const go = (to: string) => { navigate({ to }); setOpen(false) }

  if (!open) return null

  return (
    <div className="fixed inset-0 z-[100]" onClick={() => setOpen(false)}>
      <div className="fixed inset-0 bg-black/50" />
      <div className="fixed left-1/2 top-[20%] z-[101] w-full max-w-lg -translate-x-1/2" onClick={(e) => e.stopPropagation()}>
        <Command className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] shadow-2xl">
          <div className="flex items-center gap-2 border-b border-[var(--color-border)] px-4">
            <Search className="h-4 w-4 text-[var(--color-text-secondary)]" />
            <Command.Input placeholder="Search or jump to..." className="h-12 flex-1 bg-transparent text-sm outline-none placeholder:text-[var(--color-text-secondary)]" />
          </div>
          <Command.List className="max-h-80 overflow-y-auto p-2">
            <Command.Empty className="p-4 text-center text-sm text-[var(--color-text-secondary)]">No results</Command.Empty>
            <Command.Group heading="Navigation" className="mb-2 text-xs font-medium text-[var(--color-text-secondary)] px-2 py-1">
              <Command.Item onSelect={() => go('/')} className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm aria-selected:bg-slate-100 dark:aria-selected:bg-slate-700">
                <FileText className="h-4 w-4" /> Dashboard
              </Command.Item>
              <Command.Item onSelect={() => go('/workspaces')} className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm aria-selected:bg-slate-100 dark:aria-selected:bg-slate-700">
                <FolderOpen className="h-4 w-4" /> Workspaces
              </Command.Item>
              <Command.Item onSelect={() => go('/search')} className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm aria-selected:bg-slate-100 dark:aria-selected:bg-slate-700">
                <Search className="h-4 w-4" /> Search
              </Command.Item>
              <Command.Item onSelect={() => go('/admin')} className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm aria-selected:bg-slate-100 dark:aria-selected:bg-slate-700">
                <Settings className="h-4 w-4" /> Admin
              </Command.Item>
            </Command.Group>
          </Command.List>
        </Command>
      </div>
    </div>
  )
}
