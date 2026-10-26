import type { ReactNode } from 'react'
import { cn } from '@/lib/cn'

// Keyboard-shortcut chip (22px tall) shown inside the header search trigger
// and menu items. `font-sans` overrides Tailwind preflight's monospace `kbd`
// so modifier glyphs (⌘, ⌃) render in the UI face.

export function Kbd({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <kbd
      className={cn(
        'inline-flex h-[22px] items-center rounded-sm bg-muted px-1.5 font-sans text-[11px] font-medium text-muted-foreground',
        className,
      )}
    >
      {children}
    </kbd>
  )
}
