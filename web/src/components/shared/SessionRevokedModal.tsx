// Full-screen modal shown when the backend revokes a session for
// binding-mismatch. The UX intent is "stop the user in their tracks
// and make them re-authenticate" — no dismiss, only a login CTA.
// Rendered at the app root so it overlays every route.

import { ShieldAlert } from 'lucide-react'
import { useSessionStore } from '@/store/sessionStore'
import { useAuthStore } from '@/store/authStore'

export function SessionRevokedModal() {
  const revoked = useSessionStore((s) => s.revoked)
  if (!revoked) return null

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="session-revoked-title"
      data-testid="session-revoked-modal"
      className="fixed inset-0 z-[100] flex items-center justify-center bg-slate-950/70 backdrop-blur-sm"
    >
      <div className="mx-4 max-w-md rounded-xl border border-red-200 bg-white p-8 shadow-2xl dark:border-red-900 dark:bg-slate-900">
        <div className="flex items-start gap-3">
          <ShieldAlert className="h-6 w-6 shrink-0 text-red-600" aria-hidden="true" />
          <div>
            <h2 id="session-revoked-title" className="text-lg font-semibold">
              Your session was revoked for security reasons
            </h2>
            <p className="mt-2 text-sm text-[var(--color-text-secondary)]">
              We detected a significant change in how you're connecting
              (different network or device than when you signed in). To keep
              your account safe, your session has ended.
            </p>
            <p className="mt-3 text-xs text-[var(--color-text-secondary)]">
              This may happen after switching networks, traveling, or if
              someone else attempted to use your session.
            </p>
            <button
              data-testid="session-revoked-login"
              onClick={() => {
                useAuthStore.getState().logout()
                window.location.href = '/login'
              }}
              className="mt-5 w-full rounded-md bg-[var(--color-primary)] px-4 py-2 text-sm font-medium text-white hover:opacity-90"
            >
              Sign in again
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
