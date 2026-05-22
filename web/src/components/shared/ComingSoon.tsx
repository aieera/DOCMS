import type { ReactNode } from 'react'

// ComingSoon — the explicit "not built yet" placeholder for routes
// whose backend surface isn't ready. Wave 5 pattern 2: unfinished UI
// must say so out loud rather than render a fake empty state, a
// no-op submit button, or a "Document preview loads here" stub.
//
// API mirrors EmptyState so adopting components have a near-zero
// learning curve. The eyebrow defaults to "Coming soon" but can be
// overridden (e.g. "In progress", "Behind a flag", "Backend pending")
// for different shades of unfinished.
//
// Visual: a soft outlined card centred on the page. Distinct from
// EmptyState by the eyebrow chip + dashed border, so users (and QA)
// don't mistake an unfinished feature for "nothing to show".
interface ComingSoonProps {
  /** Icon shown above the title. Pass a lucide-react node sized to your taste. */
  icon?: ReactNode
  /** Required short headline. Plain string or rich node. */
  title: ReactNode
  /** Body copy under the title. Often names what's missing on the backend. */
  description?: ReactNode
  /** Small badge at the top. Defaults to "Coming soon". */
  eyebrow?: ReactNode
  /** Optional bullet list of sub-features that will land with this surface. */
  bullets?: ReactNode[]
  /**
   * Optional rich action slot — e.g. a link to a tracking issue or
   * a "back to dashboard" button. No default action; the component
   * is intentionally action-less unless the caller adds one, to
   * signal "this isn't ready to interact with".
   */
  action?: ReactNode
}

export function ComingSoon({
  icon,
  title,
  description,
  eyebrow = 'Coming soon',
  bullets,
  action,
}: ComingSoonProps) {
  return (
    <div className="mx-auto max-w-md rounded-xl border border-dashed border-border bg-card/40 px-6 py-12 text-center">
      <div className="mb-3 flex justify-center">
        <span
          className="inline-flex items-center rounded-full bg-primary/10 px-2.5 py-0.5 text-xs font-medium uppercase tracking-wide text-primary"
          data-testid="coming-soon-eyebrow"
        >
          {eyebrow}
        </span>
      </div>
      {icon ? (
        <div className="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-full bg-muted text-muted-foreground">
          {icon}
        </div>
      ) : null}
      <h3 className="text-base font-semibold text-foreground">{title}</h3>
      {description ? (
        <p className="mx-auto mt-1 max-w-sm text-sm text-muted-foreground">{description}</p>
      ) : null}
      {bullets && bullets.length > 0 ? (
        <ul className="mx-auto mt-4 max-w-sm space-y-1 text-start text-sm text-muted-foreground">
          {bullets.map((b, i) => (
            <li key={i} className="flex items-start gap-2">
              <span aria-hidden className="mt-1.5 inline-block h-1 w-1 shrink-0 rounded-full bg-muted-foreground/70" />
              <span>{b}</span>
            </li>
          ))}
        </ul>
      ) : null}
      {action ? <div className="mt-5">{action}</div> : null}
    </div>
  )
}
