// Compat shim. The real implementation now lives in
// ./shadcn/dialog.tsx (canonical Radix surface). This file
// preserves the bespoke `<Dialog open onOpenChange title
// description size>{children}</Dialog>` API for the 11
// remaining importers; new code should compose the canonical
// pieces directly (DialogContent + DialogHeader + DialogTitle
// + DialogDescription + DialogFooter).

import type { ReactNode } from 'react'
import {
  Dialog as DialogRoot,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from './shadcn/dialog'
import { cn } from '@/lib/cn'

const sizes = {
  sm: 'max-w-sm',
  md: 'max-w-lg',
  lg: 'max-w-2xl',
  xl: 'max-w-4xl',
  full: 'max-w-[90vw]',
} as const

interface DialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description?: string
  size?: keyof typeof sizes
  children: ReactNode
}

export function Dialog({ open, onOpenChange, title, description, size = 'md', children }: DialogProps) {
  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      {/* When there's no description, explicitly pass aria-describedby={undefined}
          — Radix's documented opt-out — so it doesn't warn about a missing
          Description. With a description, DialogDescription auto-wires it. */}
      <DialogContent
        className={cn(sizes[size])}
        {...(description ? {} : { 'aria-describedby': undefined })}
      >
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        {children}
      </DialogContent>
    </DialogRoot>
  )
}
