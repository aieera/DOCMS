import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { CheckCircle2, Pencil, X } from 'lucide-react'

import { correctClassification } from '@/api/classify-corrections'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/Input'

interface Props {
  documentId: string
  /** Current classification shown next to the button. The user
   * sees this and types the corrected value. */
  currentCategory: string
  /** Optional confidence reported by the model — passed through to
   * the correction record so the training collector knows how
   * confident the model was when it got it wrong. */
  currentConfidence?: number
}

/** Drop-in inline-edit control for re-labelling a document's
 * classification (ADR 0059). Posts to /classify/correct, invalidates
 * the document query so the new label renders immediately. */
export function CorrectClassificationButton({
  documentId,
  currentCategory,
  currentConfidence,
}: Props) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(currentCategory)

  const correct = useMutation({
    mutationFn: (corrected: string) =>
      correctClassification(documentId, {
        corrected_category: corrected,
        original_category: currentCategory,
        original_confidence: currentConfidence,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['document', documentId] })
      qc.invalidateQueries({ queryKey: ['classify-corrections', documentId] })
      toast.success('Classification corrected')
      setEditing(false)
    },
    onError: () => toast.error('Correction failed'),
  })

  if (!editing) {
    return (
      <Button
        size="sm"
        variant="ghost"
        onClick={() => {
          setValue(currentCategory)
          setEditing(true)
        }}
        aria-label="Correct classification"
      >
        <Pencil className="mr-1 h-3 w-3" />
        Correct
      </Button>
    )
  }

  const submit = () => {
    const trimmed = value.trim()
    if (!trimmed) {
      toast.error('Category required')
      return
    }
    if (trimmed === currentCategory) {
      setEditing(false)
      return
    }
    correct.mutate(trimmed)
  }

  return (
    <div className="flex items-center gap-1">
      <Input
        autoFocus
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') submit()
          if (e.key === 'Escape') setEditing(false)
        }}
        className="h-7 w-40 text-xs"
      />
      <Button
        size="sm"
        variant="outline"
        disabled={correct.isPending}
        onClick={submit}
        aria-label="Save correction"
      >
        <CheckCircle2 className="h-4 w-4 text-emerald-600" />
      </Button>
      <Button
        size="sm"
        variant="ghost"
        onClick={() => setEditing(false)}
        aria-label="Cancel"
      >
        <X className="h-4 w-4" />
      </Button>
    </div>
  )
}
