// Compliance-officer redaction dialog. Mounted from the document
// detail page behind a role check. Today exposes entity-based
// auto-detection (PII / SSN / etc.) — coordinate-picker manual
// region selection is a separate viewer-integration concern.

import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ScissorsLineDashed } from 'lucide-react'
import toast from 'react-hot-toast'

import { redactDocument, REDACTION_ENTITY_TYPES } from '@/api/redaction'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/Button'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  documentID: string
  versionID: string
}

export function RedactDialog({ open, onOpenChange, documentID, versionID }: Props) {
  const qc = useQueryClient()
  const [reason, setReason] = useState('')
  const [picked, setPicked] = useState<string[]>([])

  const redact = useMutation({
    mutationFn: () =>
      redactDocument(documentID, {
        reason: reason.trim(),
        version_id: versionID || undefined,
        entity_types: picked,
      }),
    onSuccess: (r) => {
      toast.success(
        `Redaction queued (${r.redaction_id.slice(0, 8)}…). New version will land when pixel-level redaction completes.`,
      )
      qc.invalidateQueries({ queryKey: ['document', documentID] })
      qc.invalidateQueries({ queryKey: ['document', documentID, 'versions'] })
      onOpenChange(false)
      setReason('')
      setPicked([])
    },
    onError: (e: unknown) =>
      toast.error(
        `Redaction failed: ${typeof e === 'object' && e && 'message' in e ? String((e as { message: string }).message) : String(e)}`,
      ),
  })

  const valid = reason.trim().length > 0 && picked.length > 0

  const toggle = (id: string) =>
    setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]))

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Redact document" size="md">
      <div className="space-y-4">
        <div className="rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200">
          <strong>This is irreversible.</strong> A new redacted version is produced; the original
          version remains in the audit trail but its content is rewritten with black bars over
          every match. Redactions are blocked when the document is on legal hold (423 Locked).
        </div>

        <div>
          <label htmlFor="redact-reason" className="text-xs font-medium">
            Reason <span className="text-red-600">*</span>
          </label>
          <textarea
            id="redact-reason"
            data-testid="redact-reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            rows={3}
            placeholder="GDPR Article 17 erasure / customer request 2026-04-19 / matter MATTER-2026-00042"
            className="mt-1 w-full rounded border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2 py-1.5 text-sm"
          />
        </div>

        <fieldset className="space-y-1">
          <legend className="text-xs font-medium">
            Entity types to redact <span className="text-red-600">*</span>
          </legend>
          <p className="text-xs text-[var(--color-text-secondary)]">
            The intelligence service runs NER on the selected version and burns a black bar over
            every match.
          </p>
          <div className="mt-2 grid grid-cols-2 gap-1.5">
            {REDACTION_ENTITY_TYPES.map((t) => {
              const checked = picked.includes(t.id)
              return (
                <label
                  key={t.id}
                  data-testid={`redact-entity-${t.id}`}
                  className="flex items-center gap-2 rounded border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2 py-1.5 text-xs"
                >
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={() => toggle(t.id)}
                  />
                  <span>{t.label}</span>
                </label>
              )
            })}
          </div>
        </fieldset>

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            data-testid="redact-submit"
            disabled={!valid}
            loading={redact.isPending}
            onClick={() => redact.mutate()}
          >
            <ScissorsLineDashed className="mr-1 h-4 w-4" /> Redact
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
