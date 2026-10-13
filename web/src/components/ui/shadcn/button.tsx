import { forwardRef, type ButtonHTMLAttributes } from 'react'
import { Slot } from '@radix-ui/react-slot'
import { cva, type VariantProps } from 'class-variance-authority'
import { Loader2 } from 'lucide-react'
import { cn } from '@/lib/cn'

// Canonical shadcn Button extended with a `loading` sugar prop.
// CVA-typed variants, asChild for composing with anchors/Links
// (`<Button asChild><Link to="…">`). When `loading` is true the
// button renders a leading spinner and is disabled — saves every
// mutation site from rendering Loader2 by hand. Pure canonical
// shadcn pattern is `disabled + manual spinner child`; the sugar
// here keeps the call sites identical to the bespoke ./Button so
// the strangler import-swap is purely mechanical.

const buttonVariants = cva(
  cn(
    'inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg text-sm font-medium',
    'ring-offset-background transition-[box-shadow,background-color,color] duration-150',
    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
    'disabled:pointer-events-none disabled:opacity-50 disabled:shadow-none',
    '[&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0',
  ),
  {
    variants: {
      variant: {
        default: 'bg-primary text-primary-foreground shadow-neu-sm hover:brightness-110 active:shadow-neu-pressed',
        destructive:
          'bg-destructive text-destructive-foreground shadow-neu-sm hover:brightness-110 active:shadow-neu-pressed',
        outline:
          'bg-background text-foreground border border-input shadow-neu-sm hover:text-primary active:shadow-neu-pressed',
        secondary:
          'bg-secondary text-secondary-foreground shadow-neu-sm hover:text-primary active:shadow-neu-pressed',
        ghost: 'text-foreground hover:shadow-neu-sm hover:text-primary active:shadow-neu-pressed',
        link: 'text-primary underline-offset-4 hover:underline',
      },
      size: {
        default: 'h-9 px-4 py-2',
        sm: 'h-8 rounded-md px-3 text-xs',
        lg: 'h-10 rounded-md px-6',
        icon: 'h-9 w-9',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
)

export interface ButtonProps
  extends ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean
  loading?: boolean
}

const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, loading, disabled, children, ...props }, ref) => {
    const Comp = asChild ? Slot : 'button'
    if (asChild) {
      // Slot composition can't accept extra children injected here —
      // forward as-is so the parent (Link, anchor) renders them.
      return (
        <Comp
          className={cn(buttonVariants({ variant, size, className }))}
          ref={ref}
          {...props}
        >
          {children}
        </Comp>
      )
    }
    return (
      <Comp
        className={cn(buttonVariants({ variant, size, className }))}
        ref={ref}
        disabled={disabled || loading}
        {...props}
      >
        {loading && <Loader2 className="h-4 w-4 animate-spin" />}
        {children}
      </Comp>
    )
  },
)
Button.displayName = 'Button'

export { Button, buttonVariants }
