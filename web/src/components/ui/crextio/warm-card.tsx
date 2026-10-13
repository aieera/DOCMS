import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/cn'

// Crextio-kit card, re-skinned neumorphic (spec 2026-08-29). Two
// surface variants — "cream" is now a canvas-color raised card
// (bg-card + shadow-neu), "dark" stays the deliberate charcoal
// contrast pair (the "Onboarding Task" card in the reference).

const warmCardVariants = cva(
  cn(
    'relative overflow-hidden rounded-2xl p-[22px]',
    'flex flex-col gap-0 transition-shadow',
  ),
  {
    variants: {
      variant: {
        // Neumorphic raised card — same surface color as the canvas,
        // depth comes purely from the dual-tone shadow-neu utility
        // (spec 2026-08-29). No hard border.
        cream: 'bg-card text-card-foreground shadow-neu',
        // Deliberate charcoal contrast card (the reference "Onboarding Task"
        // pairing). Intentionally dark in BOTH themes — a neutral elevated
        // surface a touch above the canvas, not a theme-token surface.
        dark: cn(
          'bg-[#1A1A1A] text-[#FAFAFA]',
          'shadow-[0_24px_56px_-22px_rgba(0,0,0,0.6)]',
        ),
        bare: 'bg-transparent shadow-none p-0',
      },
      padded: {
        sm: 'p-4',
        md: 'p-[22px]',
        lg: 'p-7',
        none: 'p-0',
      },
    },
    defaultVariants: { variant: 'cream', padded: 'md' },
  },
)

export interface WarmCardProps
  extends HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof warmCardVariants> {}

export const WarmCard = forwardRef<HTMLDivElement, WarmCardProps>(
  ({ className, variant, padded, ...props }, ref) => (
    <div ref={ref} className={cn(warmCardVariants({ variant, padded }), className)} {...props} />
  ),
)
WarmCard.displayName = 'WarmCard'

// Header row for a WarmCard — title on the left, optional action on
// the right (e.g. an arrow button, or a "%" badge).
export interface WarmCardHeaderProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  title: ReactNode
  action?: ReactNode
  titleClassName?: string
}

export const WarmCardHeader = forwardRef<HTMLDivElement, WarmCardHeaderProps>(
  ({ className, title, action, titleClassName, ...props }, ref) => (
    <div
      ref={ref}
      className={cn('mb-4 flex items-center justify-between gap-3', className)}
      {...props}
    >
      <h3 className={cn('text-[15px] font-semibold tracking-tight', titleClassName)}>
        {title}
      </h3>
      {action}
    </div>
  ),
)
WarmCardHeader.displayName = 'WarmCardHeader'

// Small circular arrow button used in the reference cards' top-right
// corner — opens the underlying surface in a fuller view.
export const WarmCardArrow = forwardRef<HTMLButtonElement, HTMLAttributes<HTMLButtonElement>>(
  ({ className, ...props }, ref) => (
    <button
      ref={ref}
      type="button"
      aria-label="Open"
      className={cn(
        'inline-flex h-[30px] w-[30px] items-center justify-center rounded-full',
        'bg-background text-foreground shadow-neu-sm',
        'transition-[box-shadow,transform,color] duration-150',
        'hover:-translate-y-0.5 hover:translate-x-0.5 hover:text-primary hover:shadow-neu',
        'active:translate-x-0 active:translate-y-0 active:shadow-neu-pressed',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        className,
      )}
      {...props}
    >
      <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round">
        <path d="M7 17L17 7" />
        <path d="M9 7h8v8" />
      </svg>
    </button>
  ),
)
WarmCardArrow.displayName = 'WarmCardArrow'
