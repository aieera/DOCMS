import { forwardRef, useId, useState } from 'react'
import { Eye, EyeOff } from 'lucide-react'
import { Input, type InputProps } from '@/components/ui/shadcn/input'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/cn'

// Input with a show/hide toggle. Mirrors Input's `label`/`error` sugar, but
// renders that wrapper itself so the toggle button stays positioned against
// the input row rather than the whole labeled column. Composing via
// <FormField> works too — just omit label/error here.
export type PasswordInputProps = Omit<InputProps, 'type'>

export const PasswordInput = forwardRef<HTMLInputElement, PasswordInputProps>(
  ({ className, disabled, label, error, id, ...props }, ref) => {
    const [visible, setVisible] = useState(false)
    const generated = useId()
    const inputId = id ?? generated
    const row = (
      <div className="relative">
        <Input
          ref={ref}
          id={inputId}
          type={visible ? 'text' : 'password'}
          aria-invalid={error ? true : undefined}
          className={cn('pe-9', className)}
          disabled={disabled}
          {...props}
        />
        <button
          type="button"
          onClick={() => setVisible((v) => !v)}
          disabled={disabled}
          aria-label={visible ? 'Hide password' : 'Show password'}
          className="absolute inset-y-0 end-0 flex items-center pe-3 text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
        >
          {visible ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
        </button>
      </div>
    )
    if (!label && !error) return row
    return (
      <div className="space-y-1.5">
        {label && (
          <Label htmlFor={inputId} className={error ? 'text-destructive' : undefined}>
            {label}
          </Label>
        )}
        {row}
        {error && <p className="text-xs font-medium text-destructive">{error}</p>}
      </div>
    )
  },
)
PasswordInput.displayName = 'PasswordInput'
