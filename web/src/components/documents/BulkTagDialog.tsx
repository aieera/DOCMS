import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { X } from 'lucide-react'

import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { updateDocument } from '@/api/documents'

interface BulkTagTarget {
  id: string
  tags?: string[]
}

interface Props {
  open: boolean
  onOpenChange: (o: boolean) => void
  documents: BulkTagTarget[]
  workspaceId: string
  onDone?: () => void
}

// BulkTagDialog adds one or more tags to every selected document. Tags
// are MERGED with each document's existing tags (de-duplicated), so this
// is additive — it never strips tags a document already has.
export function BulkTagDialog({ open, onOpenChange, documents, workspaceId, onDone }: Props) {
  const [draft, setDraft] = useState('')
  const [tags, setTags] = useState<string[]>([])
  const [pending, setPending] = useState(false)
  const qc = useQueryClient()

  const reset = () => {
    setDraft('')
    setTags([])
  }

  const addTag = (raw: string) => {
    const t = raw.trim()
    if (t && !tags.includes(t)) setTags((prev) => [...prev, t])
    setDraft('')
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault()
      addTag(draft)
    } else if (e.key === 'Backspace' && draft === '' && tags.length > 0) {
      setTags((prev) => prev.slice(0, -1))
    }
  }

  const apply = async () => {
    const toAdd = draft.trim() ? [...tags, draft.trim()] : tags
    if (toAdd.length === 0) {
      toast.error('Add at least one tag')
      return
    }
    setPending(true)
    const results = await Promise.allSettled(
      documents.map((d) => {
        const merged = Array.from(new Set([...(d.tags ?? []), ...toAdd]))
        return updateDocument(d.id, { tags: merged })
      }),
    )
    setPending(false)
    const failed = results.filter((r) => r.status === 'rejected').length
    const ok = results.length - failed
    if (ok > 0) toast.success(`Tagged ${ok} document${ok === 1 ? '' : 's'}`)
    if (failed > 0) toast.error(`${failed} could not be tagged`)
    qc.invalidateQueries({ queryKey: ['documents'] })
    qc.invalidateQueries({ queryKey: ['documents', workspaceId] })
    reset()
    onDone?.()
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => { if (!o) reset(); onOpenChange(o) }}
      title={`Tag ${documents.length} document${documents.length === 1 ? '' : 's'}`}
      description="Tags are added to every selected document. Existing tags are kept."
      size="sm"
    >
      <div className="space-y-4">
        <div className="flex flex-wrap gap-1.5 rounded-md border border-border bg-background p-2">
          {tags.map((t) => (
            <span key={t} className="inline-flex items-center gap-1 rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
              {t}
              <button type="button" onClick={() => setTags((prev) => prev.filter((x) => x !== t))} aria-label={`Remove ${t}`}>
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
          <Input
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder={tags.length === 0 ? 'Add tags (Enter or comma to separate)' : 'Add another…'}
            className="h-7 min-w-[8rem] flex-1 border-0 p-0 shadow-none focus-visible:ring-0"
            data-testid="bulk-tag-input"
            aria-label="Add tag"
          />
        </div>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={() => onOpenChange(false)} disabled={pending}>Cancel</Button>
          <Button type="button" onClick={apply} loading={pending} data-testid="bulk-tag-apply">
            Add tags
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
