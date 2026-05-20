import { useState } from 'react'
import { Calendar as CalendarIcon } from 'lucide-react'
import { cn } from '@/lib/cn'
import { Button } from './button'
import { Calendar } from './calendar'
import { Popover, PopoverContent, PopoverTrigger } from './popover'

interface DatePickerProps {
  value?: Date
  onChange?: (date: Date | undefined) => void
  placeholder?: string
  disabled?: boolean
  className?: string
  // Optional id passthrough so callers can pair an external <Label htmlFor>.
  id?: string
}

// Composition of canonical Calendar inside Popover. Drop into
// forms wherever a single date is needed; controlled value/onChange
// API mirrors the bespoke pattern. For range pickers, use Calendar
// directly with `mode="range"` + your own state.
function DatePicker({ value, onChange, placeholder = 'Pick a date', disabled, className, id }: DatePickerProps) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id}
          variant="outline"
          disabled={disabled}
          className={cn(
            'w-full justify-start text-start font-normal',
            !value && 'text-muted-foreground',
            className,
          )}
        >
          <CalendarIcon className="h-4 w-4" />
          {value ? value.toLocaleDateString() : placeholder}
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-auto p-0" align="start">
        <Calendar
          mode="single"
          selected={value}
          onSelect={(d) => { onChange?.(d); setOpen(false) }}
          autoFocus
        />
      </PopoverContent>
    </Popover>
  )
}

export { DatePicker }
