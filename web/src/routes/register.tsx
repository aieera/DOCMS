import { createFileRoute, redirect } from '@tanstack/react-router'

import { tenantHint } from '@/lib/tenantHint'

// DEF-09 — there is no self-service sign-up. Accounts come only from
// invitations, SSO/SCIM provisioning or admins (the auth service answers
// 403 on POST /auth/register unless a deployment opts in). Old bookmarks
// and links to /register land on sign-in, keeping a ?tenant= hint.
export const Route = createFileRoute('/register')({
  validateSearch: (search: Record<string, unknown>): { tenant?: string } => ({
    tenant: tenantHint(search.tenant),
  }),
  beforeLoad: ({ search }) => {
    throw redirect({
      to: '/login',
      search: search.tenant ? { tenant: search.tenant } : {},
      replace: true,
    })
  },
})
