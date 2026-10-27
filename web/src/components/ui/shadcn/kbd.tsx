import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/cn'

// Keyboard-shortcut chip (22px tall, 4px radius) shown inside the header
// search trigger and menu items. `font-sans` overrides Tailwind preflight's
// monospace `kbd` so modifier glyphs (⌘, ⌃) render in the UI face. Remaining
// HTML attributes (e.g. `aria-hidden` when the shortcut is announced
// elsewhere) are forwarded to the element.

export function Kbd({ children, className, ...props }: HTMLAttributes<HTMLElement>) {
  return (
    <kbd
      className={cn(
        'inline-flex h-[22px] items-center rounded-[4px] bg-muted px-1.5 font-sans text-[11px] font-medium text-muted-foreground',
        className,
      )}
      {...props}
    >
      {children}
    </kbd>
  )
}
