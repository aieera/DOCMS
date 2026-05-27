import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { useUpdateDocument } from '@/hooks/useDocuments'

interface Props {
  open: boolean
  onOpenChange: (o: boolean) => void
  documentId: string
  initialTitle: string
}

export function RenameDocumentDialog({ open, onOpenChange, documentId, initialTitle }: Props) {
  const [title, setTitle] = useState(initialTitle)
  const update = useUpdateDocument()

  useEffect(() => {
    if (open) setTitle(initialTitle)
  }, [open, initialTitle])

  const submit = () => {
    if (update.isPending) return
    const trimmed = title.trim()
    if (!trimmed) { toast.error('Title is required'); return }
    if (trimmed === initialTitle) { toast.error('Enter a new title'); return }
    update.mutate(
      { id: documentId, body: { title: trimmed } },
      { onSuccess: () => onOpenChange(false) },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Rename document">
      <form
        onSubmit={(e) => { e.preventDefault(); submit() }}
        className="space-y-4"
      >
        <Input
          label="Title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          autoFocus
          data-testid="rename-document-input"
        />
        <div className="flex justify-end gap-2">
          <Button
            type="button"
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={update.isPending}
          >
            Cancel
          </Button>
          <Button
            type="submit"
            disabled={update.isPending}
            loading={update.isPending}
            data-testid="rename-document-submit"
          >
            Save
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
