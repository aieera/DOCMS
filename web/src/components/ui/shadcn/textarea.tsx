import { forwardRef, useId, type TextareaHTMLAttributes, type ReactNode } from 'react'
import { cn } from '@/lib/cn'
import { Label } from '@/components/ui/label'

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  label?: ReactNode
  error?: ReactNode
}

const TextareaBase = forwardRef<
  HTMLTextAreaElement,
  Omit<TextareaProps, 'label' | 'error'> & { hasError?: boolean }
>(({ className, hasError, ...props }, ref) => (
  <textarea
    ref={ref}
    aria-invalid={hasError || undefined}
    className={cn(
      'flex min-h-[5rem] w-full rounded-md border border-input bg-outline-bg px-3 py-2 text-sm text-foreground transition-shadow',
      'placeholder:text-muted-foreground',
      'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring focus-visible:ring-offset-0',
      'focus-visible:shadow-focus-halo',
      'disabled:cursor-not-allowed disabled:opacity-50',
      'aria-[invalid=true]:border-destructive aria-[invalid=true]:focus-visible:ring-destructive',
      'aria-[invalid=true]:focus-visible:shadow-focus-halo-destructive',
      className,
    )}
    {...props}
  />
))
TextareaBase.displayName = 'TextareaBase'

const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(({ label, error, id, ...props }, ref) => {
  const generated = useId()
  const inputId = id ?? generated
  if (!label && !error) {
    return <TextareaBase ref={ref} id={inputId} {...props} />
  }
  return (
    <div className="space-y-1.5">
      {label && <Label htmlFor={inputId} className={error ? 'text-destructive' : undefined}>{label}</Label>}
      <TextareaBase ref={ref} id={inputId} hasError={!!error} {...props} />
      {error && <p className="text-xs font-medium text-destructive">{error}</p>}
    </div>
  )
})
Textarea.displayName = 'Textarea'

export { Textarea }
