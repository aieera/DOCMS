import { type HTMLAttributes } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/cn'

// Canonical shadcn Badge with the four standard variants
// (default / secondary / destructive / outline) PLUS the
// lifecycle-state variants the bespoke ./Badge already exposed
// (draft / in_review / active / superseded / archived / disposed)
// so the import-swap stays mechanical for every existing call
// site. New code should prefer the canonical four; the lifecycle
// values stay around for the document state badges referenced
// from ~30 places.

const badgeVariants = cva(
  'inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2',
  {
    variants: {
      variant: {
        default: 'bg-primary text-primary-foreground shadow-neu-sm hover:bg-primary/80',
        secondary: 'bg-secondary text-secondary-foreground shadow-neu-sm hover:bg-secondary/80',
        destructive: 'bg-destructive text-destructive-foreground shadow-neu-sm hover:bg-destructive/80',
        outline: 'text-foreground border border-input',
        // Lifecycle / state variants — colour-on-tinted-bg to read
        // legibly in light + dark without per-variant dark: classes.
        draft: 'bg-muted text-muted-foreground shadow-neu-sm',
        in_review: 'bg-warning/15 text-warning-strong shadow-neu-sm',
        active: 'bg-success/15 text-success shadow-neu-sm',
        superseded: 'bg-info/15 text-info shadow-neu-sm',
        archived: 'bg-muted text-muted-foreground shadow-neu-sm',
        disposed: 'bg-destructive/15 text-destructive shadow-neu-sm',
        info: 'bg-info/15 text-info shadow-neu-sm',
        success: 'bg-success/15 text-success shadow-neu-sm',
        warning: 'bg-warning/15 text-warning-strong shadow-neu-sm',
      },
    },
    defaultVariants: { variant: 'default' },
  },
)

type BadgeVariantStrict = NonNullable<VariantProps<typeof badgeVariants>['variant']>

export interface BadgeProps extends HTMLAttributes<HTMLDivElement> {
  // Accepts the strict variant union OR any string — dynamic call
  // sites (`<Badge variant={doc.lifecycle_state}>`) pass arbitrary
  // strings; unknown values fall back to default via the CVA cast.
  variant?: BadgeVariantStrict | (string & {})
}

function Badge({ className, variant, ...props }: BadgeProps) {
  return (
    <div
      className={cn(badgeVariants({ variant: variant as BadgeVariantStrict }), className)}
      {...props}
    />
  )
}

export { Badge, badgeVariants }
