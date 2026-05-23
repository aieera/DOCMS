import { useEffect, useId, useState } from 'react'
import { Loader2 } from 'lucide-react'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from './alert-dialog'
import { Input } from './input'
import { cn } from '@/lib/cn'

interface TypedConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  /**
   * The exact string the user must type to enable the action. Match is
   * case-sensitive and whitespace-sensitive — same UX as GitHub's
   * "type the repository name to confirm" pattern. The expected value
   * is rendered inline in the description for clarity.
   */
  expectedValue: string
  /** Label of the input field. Defaults to "Type to confirm". */
  inputLabel?: string
  confirmLabel?: string
  destructive?: boolean
  loading?: boolean
  onConfirm: () => void
  /**
   * Optional test-id forwarded to the confirm button so callers can
   * target it without relying on visual labels (which may vary by
   * destructive surface).
   */
  confirmTestId?: string
}

/**
 * AlertDialog variant for high-risk destructive actions (workspace delete,
 * legal-hold release, etc.) that gates the confirm button behind an
 * exact-match typed value. Matches GitHub / Linear's "type the name to
 * confirm" pattern.
 *
 * Differs from ConfirmDialog: confirm stays disabled until the input
 * exactly matches expectedValue. Input is auto-cleared each time the
 * dialog opens so re-opening doesn't accidentally land on a confirmable
 * state.
 */
export function TypedConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  expectedValue,
  inputLabel,
  confirmLabel = 'Confirm',
  destructive,
  loading,
  onConfirm,
  confirmTestId,
}: TypedConfirmDialogProps) {
  const [typed, setTyped] = useState('')
  const labelId = useId()

  useEffect(() => {
    if (open) setTyped('')
  }, [open])

  const matches = typed === expectedValue
  const canConfirm = matches && !loading

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>
            {description}
            <span className="mt-3 block">
              To confirm, type{' '}
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
                {expectedValue}
              </code>
              {' '}below.
            </span>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <Input
          id={labelId}
          label={inputLabel ?? 'Type to confirm'}
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          autoFocus
          autoComplete="off"
          data-testid="typed-confirm-input"
        />
        <AlertDialogFooter>
          <AlertDialogCancel disabled={loading}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={!canConfirm}
            onClick={(e) => {
              // Match ConfirmDialog's pattern: preventDefault so the
              // dialog stays open while the parent's mutation flips
              // loading; the parent closes the dialog on success.
              e.preventDefault()
              if (canConfirm) onConfirm()
            }}
            className={cn(
              destructive && 'bg-destructive text-destructive-foreground hover:bg-destructive/90',
            )}
            data-testid={confirmTestId ?? 'typed-confirm-confirm'}
          >
            {loading && <Loader2 className="me-2 h-4 w-4 animate-spin" />}
            {confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
