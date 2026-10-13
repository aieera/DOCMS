import type { ReactNode } from 'react'
import { cn } from '@/lib/cn'

interface PageHeaderProps {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  className?: string
  // When true, drops the bottom margin so a sticky page toolbar
  // can dock directly underneath without a gap.
  noMargin?: boolean
  // 'section' renders an h2 at section scale — for components that
  // used to be standalone pages but now live inside a tabbed page
  // that already carries the h1 (admin consolidation): two full-size
  // page titles stacked on one screen read as a bug.
  variant?: 'page' | 'section'
}

// Standard page heading row used at the top of every authenticated
// route. Title + optional description on the left, optional action
// buttons on the right. Stack on small viewports so the actions
// don't squeeze the title.
export function PageHeader({ title, description, actions, className, noMargin, variant = 'page' }: PageHeaderProps) {
  const Heading = variant === 'section' ? 'h2' : 'h1'
  return (
    <div
      className={cn(
        'flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between',
        noMargin ? '' : variant === 'section' ? 'mb-4' : 'mb-6',
        className,
      )}
    >
      <div className="min-w-0">
        <Heading
          // dir="auto" per run of text, not per page. With the UI in
          // Arabic the page direction is RTL, so an English sentence
          // rendered inside it has its trailing punctuation moved to the
          // front — ".Manage tenant-wide policy" instead of "Manage
          // tenant-wide policy." Letting the browser pick the direction
          // from the first strong character fixes that for English copy,
          // untranslated strings and Latin document titles alike, and
          // changes nothing for Arabic content.
          dir="auto"
          className={cn(
            'font-semibold tracking-tight text-foreground',
            variant === 'section' ? 'text-lg' : 'text-2xl',
          )}
        >
          {title}
        </Heading>
        {description && (
          // <div> not <p>: the description slot accepts arbitrary
          // ReactNode and several call sites pass <Skeleton/> (a
          // <div>) or composed flex rows. <p> caused validateDOMNesting
          // warnings and broke React rendering on those routes.
          <div dir="auto" className="mt-1 text-sm text-muted-foreground">{description}</div>
        )}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}
