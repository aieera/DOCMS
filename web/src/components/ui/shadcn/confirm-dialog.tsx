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
import { cn } from '@/lib/cn'

interface ConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  confirmLabel?: string
  destructive?: boolean
  loading?: boolean
  onConfirm: () => void
}

// Convenience wrapper around the canonical AlertDialog primitives.
// API-compatible with the bespoke ./ConfirmDialog so call sites
// migrate with a single import-line change. Internally uses the
// canonical AlertDialog so it inherits the focus-trap, Escape-to-
// cancel, and aria-labelledby/aria-describedby wiring upstream.
//
// For dialogs that need richer content (form, multi-step flow,
// inline error message), compose AlertDialog primitives directly
// instead of using this wrapper.
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = 'Confirm',
  destructive,
  loading,
  onConfirm,
}: ConfirmDialogProps) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={loading}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={loading}
            onClick={(e) => {
              // Prevent the default Radix behaviour of auto-closing on
              // click — the parent's onConfirm typically fires a
              // mutation that toggles `loading`, and the dialog should
              // stay open until the mutation resolves.
              e.preventDefault()
              onConfirm()
            }}
            className={cn(
              destructive && 'bg-destructive text-destructive-foreground hover:bg-destructive/90',
            )}
          >
            {loading && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
