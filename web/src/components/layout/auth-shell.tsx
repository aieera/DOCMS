import { Database } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { useRef, type ReactNode } from 'react'

import { cn } from '@/lib/cn'
import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { usePointerParallax } from '@/hooks/usePointerParallax'
import { AuthHero } from './AuthHero'
import { PointerFx } from './PointerFx'

interface AuthShellProps {
  children: ReactNode
  // Compact heading text shown on the right pane above the form.
  title?: ReactNode
  description?: ReactNode
  // Optional action shown bottom-right (e.g. "Already have an account? Sign in")
  footer?: ReactNode
  /**
   * Identifies the current step of a multi-step flow (login's credentials
   * -> method picker -> code). Changing it re-mounts the card so the new
   * step animates in instead of swapping instantly. Presentational only:
   * it never drives which step renders.
   */
  stepKey?: string
}

/**
 * Two-pane shell used by every unauthenticated route.
 *
 * Left pane is a lit stage: a slow-drifting light source moves the soft
 * shadows across the whole pane, the hero seals a document stack on a long
 * loop, and the stage tilts a few degrees toward the pointer. Right pane
 * holds the form on a raised neumorphic card. Below `lg` the stage
 * collapses and the card takes the viewport with the brand mark above it.
 *
 * All of it stops dead under `prefers-reduced-motion`, which is why the
 * root carries `data-motion` — the CSS switches every loop off from there
 * rather than each animated element re-deciding.
 */
export function AuthShell({ children, title, description, footer, stepKey }: AuthShellProps) {
  const reduced = usePrefersReducedMotion()
  const motion = reduced ? 'static' : 'animated'
  // PointerFx publishes --fx-x/--fx-y here; the brand pane's light reads
  // them in CSS, so the pointer never re-renders React.
  const surfaceRef = useRef<HTMLDivElement>(null)

  return (
    <div
      ref={surfaceRef}
      data-motion={motion}
      className="min-h-screen bg-background text-foreground"
    >
      <PointerFx surfaceRef={surfaceRef} />
      <div className="grid min-h-screen lg:grid-cols-2">
        <BrandStage motion={motion} />

        <div className="flex flex-col">
          <header className="flex items-center justify-between p-6 lg:hidden">
            <BrandMark />
          </header>

          <main className="flex flex-1 items-center justify-center px-6 pb-12 pt-2 lg:px-12 lg:py-12">
            <div
              // Re-keyed per step so each one animates in on its own.
              key={stepKey}
              data-testid="auth-card"
              data-step={stepKey}
              className={cn(
                'w-full max-w-sm space-y-6 rounded-lg bg-card p-7 shadow-neu sm:p-8',
                motion === 'animated' && 'auth-card-in',
              )}
            >
              {(title || description) && (
                <div className="space-y-1.5">
                  {title && <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>}
                  {description && <p className="text-sm text-muted-foreground">{description}</p>}
                </div>
              )}
              {children}
              {footer && <div className="pt-2 text-center text-sm text-muted-foreground">{footer}</div>}
            </div>
          </main>
        </div>
      </div>
    </div>
  )
}

function BrandMark({ size = 'sm' }: { size?: 'sm' | 'md' }) {
  const box = size === 'md' ? 'h-9 w-9' : 'h-7 w-7'
  return (
    <Link to="/login" className="flex items-center gap-2 rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
      <span className={cn('flex items-center justify-center rounded-md bg-card text-foreground shadow-neu-sm', box)}>
        <Database className={size === 'md' ? 'h-5 w-5' : 'h-4 w-4'} />
      </span>
      <span className={cn('font-semibold tracking-tight', size === 'md' ? 'text-base' : 'text-sm')}>SeDoc</span>
    </Link>
  )
}

function BrandStage({ motion }: { motion: 'animated' | 'static' }) {
  const parallax = usePointerParallax(5)

  return (
    <aside
      className="relative hidden flex-col justify-between overflow-hidden border-e border-border bg-sidebar p-12 text-sidebar-foreground lg:flex"
      {...parallax.handlers}
    >
      {/* The light source, in two layers so the pointer-follow and the idle
          drift do not fight over one transform: the outer element tracks
          the pointer via the published custom properties, the inner keeps
          its slow loop. The transition smooths the follow without a
          per-frame JS loop of its own. */}
      <div
        className="pointer-events-none absolute -inset-1/4"
        aria-hidden
        style={{
          transform:
            'translate3d(calc((var(--fx-x, 0.5) - 0.5) * 70px), calc((var(--fx-y, 0.5) - 0.5) * 70px), 0)',
          transition: 'transform 260ms cubic-bezier(0.4, 0, 0.2, 1)',
        }}
      >
        <div
          className={cn('absolute inset-0', motion === 'animated' && 'auth-light-drift')}
          style={{
            background:
              'radial-gradient(38% 34% at 28% 22%, hsl(var(--nm-light) / 0.85) 0%, transparent 70%),'
              + ' radial-gradient(34% 30% at 76% 78%, hsl(var(--primary) / 0.20) 0%, transparent 70%)',
          }}
        />
      </div>

      <div className="relative">
        <BrandMark size="md" />
      </div>

      <div
        data-testid="auth-stage"
        className="relative flex flex-col items-center gap-10 [perspective:1200px]"
        style={{
          transform: parallax.transform,
          transformStyle: 'preserve-3d',
          transition: parallax.enabled ? 'transform 220ms cubic-bezier(0.4, 0, 0.2, 1)' : undefined,
        }}
      >
        <AuthHero motion={motion} />
        <h2 className="max-w-md text-balance text-center text-3xl font-semibold leading-tight tracking-tight">
          Document management your security team will actually trust.
        </h2>
      </div>

      <p className="relative text-xs text-muted-foreground">
        © {new Date().getFullYear()} SeDoc · {' '}
        <a href="#" className="hover:text-foreground">Status</a> · {' '}
        <a href="#" className="hover:text-foreground">Privacy</a> · {' '}
        <a href="#" className="hover:text-foreground">Docs</a>
      </p>
    </aside>
  )
}
