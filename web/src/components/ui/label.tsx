import { forwardRef, type LabelHTMLAttributes } from 'react'
import { cn } from '@/lib/cn'

// Canonical shadcn Label primitive. Pure semantic <label> wrapper
// with consistent typography. The "peer-disabled" hooks let
// disabled inputs cascade their state to their associated label
// without extra wiring (`<Input disabled className="peer" /><Label
// htmlFor=…>` works automatically).
const Label = forwardRef<HTMLLabelElement, LabelHTMLAttributes<HTMLLabelElement>>(
  ({ className, ...props }, ref) => (
    <label
      ref={ref}
      className={cn(
        'text-sm font-medium leading-none text-foreground',
        'peer-disabled:cursor-not-allowed peer-disabled:opacity-70',
        className,
      )}
      {...props}
    />
  ),
)
Label.displayName = 'Label'

export { Label }
