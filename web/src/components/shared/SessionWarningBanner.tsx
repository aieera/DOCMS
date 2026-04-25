// Persistent yellow banner shown when the backend returns
// X-Session-Warning: binding-mismatch (warn strictness). Dismissible —
// the user's next refresh will re-arm it if the condition persists.

import { AlertTriangle, X } from 'lucide-react'
import { useSessionStore } from '@/store/sessionStore'

export function SessionWarningBanner() {
  const show = useSessionStore((s) => s.bindingWarning)
  const dismiss = useSessionStore((s) => s.dismissWarning)
  if (!show) return null
  return (
    <div
      role="status"
      data-testid="session-warning-banner"
      className="border-b border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200"
    >
      <div className="flex items-center gap-3 px-6 py-2 text-sm">
        <AlertTriangle className="h-4 w-4 shrink-0" aria-hidden="true" />
        <span className="flex-1">
          <strong>Unusual sign-in activity.</strong> We noticed your connection
          looks different from when you signed in. If this wasn't you,{' '}
          <a href="/settings/sessions" className="underline underline-offset-2 hover:no-underline">
            review active sessions
          </a>
          .
        </span>
        <button
          onClick={dismiss}
          aria-label="Dismiss"
          data-testid="session-warning-dismiss"
          className="shrink-0 rounded p-0.5 hover:bg-amber-200/70 dark:hover:bg-amber-900/50"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>
    </div>
  )
}
