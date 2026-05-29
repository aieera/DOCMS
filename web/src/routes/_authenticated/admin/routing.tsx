import { createFileRoute, redirect } from '@tanstack/react-router'

// /admin/routing is a guessable shortcut for the document routing
// rules surface, which actually lives at /admin/intelligence/routing-rules.
// Soft redirect so the intuitive URL doesn't 404.
export const Route = createFileRoute('/_authenticated/admin/routing')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/intelligence/routing-rules' })
  },
})
