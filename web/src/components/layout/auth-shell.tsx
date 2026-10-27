import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { Card } from '@/components/ui/card'
import { BrandMark } from './brand-mark'

interface AuthShellProps {
  children: ReactNode
  // Heading shown at the top of the card, above the form.
  title?: ReactNode
  description?: ReactNode
  // Optional line at the bottom of the card (e.g. "Already have an account? Sign in").
  footer?: ReactNode
  /**
   * Identifies the current step of a multi-step flow (login's credentials
   * -> method picker -> code). Changing it re-mounts the card, so each step
   * starts fresh: its `autoFocus` field takes focus and no state from the
   * previous step's subtree carries over. Presentational only: it never
   * drives which step renders.
   */
  stepKey?: string
}

/**
 * Shell used by every unauthenticated route (sign-in, register, accept
 * invite, forgot password): one centred column on the page background,
 * the brand mark above a bordered card that holds the form. Same tokens,
 * tile and type as the signed-in app, and nothing animates.
 */
export function AuthShell({ children, title, description, footer, stepKey }: AuthShellProps) {
  return (
    <div className="flex min-h-dvh flex-col items-center justify-center gap-6 bg-background p-4 text-foreground shell:p-6">
      <header>
        <Link
          to="/login"
          className="flex items-center gap-2 rounded-md focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring focus-visible:shadow-focus-halo"
        >
          <BrandMark />
          <span className="text-sm font-semibold">SeDoc</span>
        </Link>
      </header>

      <main className="w-full max-w-sm">
        <Card
          key={stepKey}
          data-testid="auth-card"
          data-step={stepKey}
          className="space-y-6 p-6"
        >
          {(title || description) && (
            <div className="space-y-1.5">
              {title && <h1 className="text-xl font-semibold tracking-tight">{title}</h1>}
              {description && <p className="text-sm text-muted-foreground">{description}</p>}
            </div>
          )}
          {children}
          {footer && <div className="text-center text-sm text-muted-foreground">{footer}</div>}
        </Card>
      </main>
    </div>
  )
}
