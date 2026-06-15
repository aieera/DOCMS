import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/cn'

// Crextio-aesthetic card. Two surface variants — cream for the warm
// editorial cards, dark for the contrast pair (the "Onboarding Task"
// charcoal card in the reference). Generous 24px radius, soft drop
// shadow, optional inset highlight at the top edge.

const warmCardVariants = cva(
  cn(
    'relative overflow-hidden rounded-[24px] border p-[22px]',
    'flex flex-col gap-0 transition-shadow',
  ),
  {
    variants: {
      variant: {
        // Editorial card surface driven by the THEME's card token, not a
        // pinned cream hex — so it flips with light/dark. Light resolves to
        // the warm cream (#FFFAEF family); dark to the warm near-black, with
        // `card-foreground` keeping the title + inner text legible in both.
        // (Previously the hard-coded cream made dark-mode stat numerals —
        // which use the theme foreground tokens — render pale-on-cream.)
        cream: cn(
          'bg-card border-border/70 text-card-foreground',
          // Cool layered shadow — matches the canonical Card "new" look.
          'shadow-[0_1px_2px_rgba(16,24,40,0.04),0_10px_28px_-14px_rgba(16,24,40,0.12)]',
          'dark:shadow-[0_12px_32px_-16px_rgba(0,0,0,0.6)]',
        ),
        // Deliberate charcoal contrast card (the reference "Onboarding Task"
        // pairing). Intentionally dark in BOTH themes; in dark mode it reads
        // as a neutral elevated surface a touch above the warm-black page.
        dark: cn(
          'bg-[#1A1A1A] border-white/10 text-[#FAFAFA]',
          'shadow-[0_24px_56px_-22px_rgba(0,0,0,0.6)]',
        ),
        bare: 'bg-transparent border-transparent shadow-none p-0',
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
        'inline-flex h-[30px] w-[30px] items-center justify-center rounded-full border',
        'border-[rgba(26,26,26,0.08)] bg-black/[0.025] text-current',
        'transition-[background,transform] duration-150',
        'hover:translate-x-[2px] hover:-translate-y-[2px] hover:bg-black/5',
        '[.dark_&]:border-white/10 [.dark_&]:bg-white/5 [.dark_&]:hover:bg-white/10',
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
