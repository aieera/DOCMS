import * as SelectPrimitive from '@radix-ui/react-select'
import { Check, ChevronDown } from 'lucide-react'
import { cn } from '@/lib/cn'

interface SelectOption { value: string; label: string }

interface SelectProps {
  value?: string
  onValueChange: (v: string) => void
  options: SelectOption[]
  placeholder?: string
  label?: string
  className?: string
}

export function Select({ value, onValueChange, options, placeholder = 'Select...', label, className }: SelectProps) {
  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      {label && <span className="text-sm font-medium text-[var(--color-text-secondary)]">{label}</span>}
      <SelectPrimitive.Root value={value} onValueChange={onValueChange}>
        <SelectPrimitive.Trigger className="flex h-9 w-full items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-3 text-sm focus:outline-none focus:ring-2 focus:ring-[var(--color-primary)]">
          <SelectPrimitive.Value placeholder={placeholder} />
          <SelectPrimitive.Icon><ChevronDown className="h-4 w-4 opacity-50" /></SelectPrimitive.Icon>
        </SelectPrimitive.Trigger>
        <SelectPrimitive.Portal>
          <SelectPrimitive.Content className="z-50 overflow-hidden rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] shadow-md" position="popper" sideOffset={4}>
            <SelectPrimitive.Viewport className="p-1">
              {options.map((opt) => (
                <SelectPrimitive.Item key={opt.value} value={opt.value} className="relative flex h-8 cursor-pointer items-center rounded px-8 text-sm outline-none hover:bg-slate-100 data-[highlighted]:bg-slate-100 dark:hover:bg-slate-700 dark:data-[highlighted]:bg-slate-700">
                  <SelectPrimitive.ItemIndicator className="absolute left-2"><Check className="h-3.5 w-3.5" /></SelectPrimitive.ItemIndicator>
                  <SelectPrimitive.ItemText>{opt.label}</SelectPrimitive.ItemText>
                </SelectPrimitive.Item>
              ))}
            </SelectPrimitive.Viewport>
          </SelectPrimitive.Content>
        </SelectPrimitive.Portal>
      </SelectPrimitive.Root>
    </div>
  )
}
