import { forwardRef, type InputHTMLAttributes } from 'react'
import { cn } from '@/lib/cn'

// Canonical shadcn Input. Pure <input> wrapper — no built-in
// `label` prop. Pair with the canonical Label (or Form's
// FormLabel/FormControl) for accessible labelling. Replaces the
// bespoke ./Input which baked label + error message into the
// component; canonical pattern moves those concerns into Form.

export type InputProps = InputHTMLAttributes<HTMLInputElement>

const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ className, type, ...props }, ref) => (
    <input
      ref={ref}
      type={type}
      className={cn(
        'flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm transition-colors',
        'file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground',
        'placeholder:text-muted-foreground',
        'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
        'disabled:cursor-not-allowed disabled:opacity-50',
        'aria-[invalid=true]:border-destructive aria-[invalid=true]:focus-visible:ring-destructive',
        className,
      )}
      {...props}
    />
  ),
)
Input.displayName = 'Input'

export { Input }
