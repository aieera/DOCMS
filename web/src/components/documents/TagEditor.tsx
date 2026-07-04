import { useState } from 'react'
import { X, Plus } from 'lucide-react'
import { useUpdateDocument } from '@/hooks/useDocuments'

export function TagEditor({ documentId, tags }: { documentId: string; tags: string[] }) {
  const [input, setInput] = useState('')
  const [adding, setAdding] = useState(false)
  const update = useUpdateDocument()

  const addTag = () => {
    const tag = input.trim().toLowerCase()
    if (tag && !tags.includes(tag)) {
      update.mutate({ id: documentId, body: { tags: [...tags, tag] } })
    }
    setInput('')
    setAdding(false)
  }

  const removeTag = (tag: string) => {
    update.mutate({ id: documentId, body: { tags: tags.filter((t) => t !== tag) } })
  }

  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {tags.map((t) => (
        <span key={t} className="inline-flex items-center gap-1 rounded-full bg-slate-100 px-2 py-0.5 text-xs dark:bg-slate-700">
          {t}
          <button onClick={() => removeTag(t)} className="hover:text-red-500" aria-label={`Remove ${t}`}><X className="h-3 w-3" /></button>
        </span>
      ))}
      {adding ? (
        <input
          autoFocus
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') addTag(); if (e.key === 'Escape') setAdding(false) }}
          onBlur={addTag}
          className="h-6 w-20 rounded border border-[var(--color-border)] bg-transparent px-1.5 text-xs outline-none focus:ring-1 focus:ring-[var(--color-primary)]"
          placeholder="tag..."
          aria-label="Add a tag"
        />
      ) : (
        <button onClick={() => setAdding(true)} className="inline-flex items-center gap-0.5 rounded-full bg-slate-100 px-2 py-0.5 text-xs hover:bg-slate-200 dark:bg-slate-700 dark:hover:bg-slate-600">
          <Plus className="h-3 w-3" /> Add
        </button>
      )}
    </div>
  )
}
