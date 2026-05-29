import { createFileRoute, redirect } from '@tanstack/react-router'

// /admin/pii is a guessable shortcut for the PII/PHI scanning
// surface, which actually lives at /admin/pii-scanning. The two were
// listed inconsistently in the sidebar tile vs the URL — users typing
// the URL got a 404. Soft redirect keeps the canonical path while
// honoring the muscle-memory shorthand.
export const Route = createFileRoute('/_authenticated/admin/pii')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/pii-scanning' })
  },
})
