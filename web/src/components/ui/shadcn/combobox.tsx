import { useState } from 'react'
import { Check, ChevronsUpDown } from 'lucide-react'
import { cn } from '@/lib/cn'
import { Button } from './button'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from './command'
import { Popover, PopoverContent, PopoverTrigger } from './popover'

interface ComboboxOption {
  value: string
  label: string
  // Optional disabled flag for individual options.
  disabled?: boolean
}

interface ComboboxProps {
  options: ComboboxOption[]
  value?: string
  onChange?: (value: string) => void
  placeholder?: string
  emptyText?: string
  searchPlaceholder?: string
  // Width of the trigger button. Popover width matches the
  // trigger via Radix.
  className?: string
  triggerClassName?: string
  disabled?: boolean
  id?: string
}

// Composition of canonical Command + Popover. The cmdk-powered
// Combobox adds keyboard search to a Select-like surface — handy
// for choosing from a long list (workspace pickers, tag pickers,
// user pickers). For multi-select use Command directly.
function Combobox({
  options,
  value,
  onChange,
  placeholder = 'Select…',
  emptyText = 'No results.',
  searchPlaceholder = 'Search…',
  className,
  triggerClassName,
  disabled,
  id,
}: ComboboxProps) {
  const [open, setOpen] = useState(false)
  const selected = options.find((o) => o.value === value)
  return (
    <div className={className}>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button
            id={id}
            variant="outline"
            role="combobox"
            aria-expanded={open}
            disabled={disabled}
            className={cn(
              'w-full justify-between font-normal',
              !selected && 'text-muted-foreground',
              triggerClassName,
            )}
          >
            {selected?.label ?? placeholder}
            <ChevronsUpDown className="opacity-50" />
          </Button>
        </PopoverTrigger>
        <PopoverContent className="w-[--radix-popover-trigger-width] p-0" align="start">
          <Command>
            <CommandInput placeholder={searchPlaceholder} />
            <CommandList>
              <CommandEmpty>{emptyText}</CommandEmpty>
              <CommandGroup>
                {options.map((opt) => (
                  <CommandItem
                    key={opt.value}
                    value={opt.label}
                    disabled={opt.disabled}
                    onSelect={() => {
                      onChange?.(opt.value)
                      setOpen(false)
                    }}
                  >
                    {opt.label}
                    {value === opt.value && <Check className="ml-auto" />}
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    </div>
  )
}

export { Combobox }
