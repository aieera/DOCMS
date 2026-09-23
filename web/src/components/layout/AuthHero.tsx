import { cn } from '@/lib/cn'

/**
 * The sign-in stage's hero: three pages slide into a stack, a padlock
 * settles onto it and its shackle clicks shut, the sealed stack breathes,
 * then the cycle fades and repeats on a long, deliberately slow loop.
 *
 * Inline SVG rather than an asset so it inherits the theme tokens and
 * costs no request. Every moving part animates `transform`/`opacity`
 * only — never a filter or a shadow, which would repaint the pane each
 * frame. The soft extrusion is baked into two static gradients, so the
 * neumorphic look costs nothing per frame.
 *
 * Decorative: `aria-hidden` with no <title>. The tagline beside it
 * carries the meaning, so announcing the drawing would be noise.
 */
export function AuthHero({
  className,
  motion = 'animated',
}: {
  className?: string
  /** `static` renders the finished, sealed state with no animation. */
  motion?: 'animated' | 'static'
}) {
  const animated = motion === 'animated'
  return (
    <svg
      viewBox="0 0 320 300"
      className={cn('h-auto w-full max-w-[320px]', className)}
      aria-hidden="true"
      focusable="false"
      data-motion={animated ? 'animated' : 'static'}
    >
      <defs>
        <linearGradient id="authPaper" x1="0" y1="0" x2="0.4" y2="1">
          <stop offset="0%" stopColor="hsl(var(--nm-light))" />
          <stop offset="100%" stopColor="hsl(var(--card))" />
        </linearGradient>
        <linearGradient id="authMetal" x1="0" y1="0" x2="0.3" y2="1">
          <stop offset="0%" stopColor="hsl(var(--primary) / 0.95)" />
          <stop offset="100%" stopColor="hsl(var(--primary) / 0.65)" />
        </linearGradient>
      </defs>

      <g className={animated ? 'auth-hero-stage' : undefined}>
        {/* Contact shadow. Sits just under the lock's base — pushed further
            down it stops reading as contact and floats as its own object. */}
        <ellipse cx="160" cy="230" rx="76" ry="8" fill="hsl(var(--nm-dark) / 0.45)" />

        {/* Pages, back to front. Equal durations with small delays keep
            them permanently staggered without drifting out of sync. */}
        {[
          { x: 74, y: 52, delay: '0s' },
          { x: 86, y: 70, delay: '0.18s' },
          { x: 98, y: 88, delay: '0.36s' },
        ].map((p, i) => (
          <g
            key={p.delay}
            data-part="page"
            className={animated ? 'auth-hero-page' : undefined}
            style={animated ? { animationDelay: p.delay } : undefined}
          >
            <rect
              x={p.x} y={p.y} width="132" height="118" rx="14"
              fill="url(#authPaper)"
              stroke="hsl(var(--border))"
              strokeWidth="1.5"
            />
            {/* Text ruling — shortest line on the front page. */}
            <rect x={p.x + 18} y={p.y + 26} width={80 - i * 8} height="7" rx="3.5" fill="hsl(var(--muted-foreground) / 0.45)" />
            <rect x={p.x + 18} y={p.y + 44} width={62 - i * 6} height="7" rx="3.5" fill="hsl(var(--muted-foreground) / 0.3)" />
          </g>
        ))}

        {/* Padlock: body drops in, shackle closes after it lands. */}
        <g data-part="lock" className={animated ? 'auth-hero-lock' : undefined}>
          <g data-part="shackle" className={animated ? 'auth-hero-shackle' : undefined}>
            <path
              d="M142 168 v-16 a18 18 0 0 1 36 0 v16"
              fill="none"
              stroke="hsl(var(--foreground) / 0.55)"
              strokeWidth="10"
              strokeLinecap="round"
            />
          </g>
          <rect x="128" y="166" width="64" height="52" rx="14" fill="url(#authMetal)" />
          <circle cx="160" cy="188" r="6" fill="hsl(var(--primary-foreground))" />
          <rect x="157" y="192" width="6" height="13" rx="3" fill="hsl(var(--primary-foreground))" />
        </g>
      </g>
    </svg>
  )
}
