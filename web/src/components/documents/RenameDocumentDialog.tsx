import { useEffect, useState } from 'react'
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

// Validation follows the NewFolderDialog pattern: the submit button is
// disabled until the form can actually be submitted, and the reason
// shows inline under the field (aria-invalid + aria-describedby via the
// Input's `error` prop). It used to leave Save enabled and answer an
// empty title with a bottom-right toast — far from the field the user
// was looking at, and gone before they could act on it.
export function RenameDocumentDialog({ open, onOpenChange, documentId, initialTitle }: Props) {
  const [title, setTitle] = useState(initialTitle)
  // Errors surface only after the user has interacted, so opening the
  // dialog doesn't greet them with a red "unchanged" complaint.
  const [touched, setTouched] = useState(false)
  const update = useUpdateDocument()

  useEffect(() => {
    if (open) {
      setTitle(initialTitle)
      setTouched(false)
    }
  }, [open, initialTitle])

  const trimmed = title.trim()
  const error = !trimmed
    ? 'Enter a title.'
    : trimmed === initialTitle
      ? 'Enter a different title to rename this document.'
      : null
  const canSubmit = !error && !update.isPending

  const submit = () => {
    setTouched(true)
    if (!canSubmit) return
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
          onChange={(e) => { setTitle(e.target.value); setTouched(true) }}
          error={touched && error ? error : undefined}
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
            disabled={!canSubmit}
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
