import * as DropdownPrimitive from '@radix-ui/react-dropdown-menu'
import { cn } from '@/lib/cn'
import type { ReactNode } from 'react'

export const DropdownMenu = DropdownPrimitive.Root
export const DropdownMenuTrigger = DropdownPrimitive.Trigger

export function DropdownMenuContent({ children, className, ...props }: DropdownPrimitive.DropdownMenuContentProps & { children: ReactNode }) {
  return (
    <DropdownPrimitive.Portal>
      <DropdownPrimitive.Content
        sideOffset={4}
        className={cn('z-50 min-w-[160px] overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-1 shadow-md', className)}
        {...props}
      >
        {children}
      </DropdownPrimitive.Content>
    </DropdownPrimitive.Portal>
  )
}

interface MenuItemProps { icon?: ReactNode; shortcut?: string; destructive?: boolean; onSelect?: () => void; children: ReactNode }

export function DropdownMenuItem({ icon, shortcut, destructive, onSelect, children }: MenuItemProps) {
  return (
    <DropdownPrimitive.Item
      onSelect={onSelect}
      className={cn(
        'flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm outline-none transition-colors',
        destructive ? 'text-red-500 hover:bg-red-50 dark:hover:bg-red-950' : 'hover:bg-slate-100 dark:hover:bg-slate-700',
      )}
    >
      {icon}
      <span className="flex-1">{children}</span>
      {shortcut && <span className="text-xs text-[var(--color-text-secondary)]">{shortcut}</span>}
    </DropdownPrimitive.Item>
  )
}

export const DropdownMenuSeparator = () => <DropdownPrimitive.Separator className="my-1 h-px bg-[var(--color-border)]" />
