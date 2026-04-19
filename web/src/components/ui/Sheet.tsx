import * as DialogPrimitive from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import { cn } from '@/lib/cn'
import type { ReactNode } from 'react'

interface SheetProps { open: boolean; onOpenChange: (o: boolean) => void; title?: string; children: ReactNode; className?: string }

export function Sheet({ open, onOpenChange, title, children, className }: SheetProps) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/30" />
        <DialogPrimitive.Content className={cn(
          'fixed inset-y-0 end-0 z-50 flex w-[400px] flex-col border-s border-[var(--color-border)] bg-[var(--color-bg-secondary)] shadow-xl transition-transform duration-200 data-[state=open]:translate-x-0 data-[state=closed]:translate-x-full',
          className,
        )}>
          {title && (
            <div className="flex items-center justify-between border-b border-[var(--color-border)] px-4 py-3">
              <DialogPrimitive.Title className="text-sm font-semibold">{title}</DialogPrimitive.Title>
              <DialogPrimitive.Close className="rounded-md p-1 hover:bg-slate-100 dark:hover:bg-slate-800" aria-label="Close"><X className="h-4 w-4" /></DialogPrimitive.Close>
            </div>
          )}
          <div className="flex-1 overflow-y-auto p-4">{children}</div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}
