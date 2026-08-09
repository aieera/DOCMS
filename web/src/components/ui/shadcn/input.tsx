import { forwardRef, useId, type InputHTMLAttributes, type ReactNode } from 'react'
import { cn } from '@/lib/cn'
import { Label } from '@/components/ui/label'

// Canonical shadcn Input with three sugar props bolted on:
//
// - `label`: when set, wraps the input in a flex column with a
//   matching <Label htmlFor>. Saves call sites from the
//   `<div className="space-y-1.5"><Label>…</Label><Input /></div>`
//   boilerplate. Pure canonical pattern is to compose those
//   yourself or use <FormField> + <FormLabel> + <FormControl>;
//   this sugar exists so the strangler import-swap from the
//   bespoke ./Input (which baked label in) can stay mechanical.
// - `error`: when set, renders a destructive-toned message
//   beneath the input and flips aria-invalid + the destructive
//   border / ring accent. Same convenience purpose.
// - `icon`: leading icon slot (rendered absolute-positioned
//   inside the input's left padding). The bespoke ./Input had
//   this; preserving it on the canonical sidecar keeps the
//   ~10 SearchInput call sites mechanical.

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: ReactNode
  error?: ReactNode
  icon?: ReactNode
}

const InputBase = forwardRef<
  HTMLInputElement,
  Omit<InputProps, 'label' | 'error'> & { hasError?: boolean }
>(({ className, type, hasError, icon, ...props }, ref) => {
  const input = (
    <input
      ref={ref}
      type={type}
      aria-invalid={hasError || undefined}
      className={cn(
        'flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm transition-colors',
        'file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground',
        'placeholder:text-muted-foreground',
        'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
        'disabled:cursor-not-allowed disabled:opacity-50',
        'aria-[invalid=true]:border-destructive aria-[invalid=true]:focus-visible:ring-destructive',
        icon && 'ps-9',
        className,
      )}
      {...props}
    />
  )
  if (!icon) return input
  return (
    <div className="relative">
      <span className="pointer-events-none absolute inset-y-0 start-0 flex items-center ps-3 text-muted-foreground">
        {icon}
      </span>
      {input}
    </div>
  )
})
InputBase.displayName = 'InputBase'

const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ label, error, id, 'aria-describedby': describedBy, ...props }, ref) => {
    const generated = useId()
    const inputId = id ?? generated
    if (!label && !error) {
      return <InputBase ref={ref} id={inputId} aria-describedby={describedBy} {...props} />
    }
    // Point the input at its own error text so a screen reader reads the
    // reason after announcing the invalid state — aria-invalid alone only
    // says "invalid", not why.
    const errorId = `${inputId}-error`
    return (
      <div className="space-y-1.5">
        {label && <Label htmlFor={inputId} className={error ? 'text-destructive' : undefined}>{label}</Label>}
        <InputBase
          ref={ref}
          id={inputId}
          hasError={!!error}
          aria-describedby={[describedBy, error ? errorId : null].filter(Boolean).join(' ') || undefined}
          {...props}
        />
        {error && <p id={errorId} className="text-xs font-medium text-destructive">{error}</p>}
      </div>
    )
  },
)
Input.displayName = 'Input'

export { Input }
