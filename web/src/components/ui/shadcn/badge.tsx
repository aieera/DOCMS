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
  'inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2',
  {
    variants: {
      variant: {
        default: 'border-transparent bg-primary text-primary-foreground hover:bg-primary/80',
        secondary: 'border-transparent bg-secondary text-secondary-foreground hover:bg-secondary/80',
        destructive: 'border-transparent bg-destructive text-destructive-foreground hover:bg-destructive/80',
        outline: 'text-foreground',
        // Lifecycle / state variants — colour-on-tinted-bg to read
        // legibly in light + dark without per-variant dark: classes.
        draft: 'border-transparent bg-muted text-muted-foreground',
        in_review: 'border-transparent bg-warning/15 text-warning',
        active: 'border-transparent bg-success/15 text-success',
        superseded: 'border-transparent bg-info/15 text-info',
        archived: 'border-transparent bg-muted text-muted-foreground',
        disposed: 'border-transparent bg-destructive/15 text-destructive',
        info: 'border-transparent bg-info/15 text-info',
        success: 'border-transparent bg-success/15 text-success',
        warning: 'border-transparent bg-warning/15 text-warning',
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
