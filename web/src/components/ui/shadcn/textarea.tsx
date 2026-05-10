import { forwardRef, type TextareaHTMLAttributes } from 'react'
import { cn } from '@/lib/cn'

export type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement>

const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  ({ className, ...props }, ref) => (
    <textarea
      ref={ref}
      className={cn(
        'flex min-h-[60px] w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-sm',
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
Textarea.displayName = 'Textarea'

export { Textarea }
