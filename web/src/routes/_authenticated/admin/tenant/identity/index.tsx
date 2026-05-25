import { createFileRoute, redirect } from '@tanstack/react-router'

// /admin/tenant/identity is an intermediate breadcrumb path. Today
// the only child is .../ldap, so redirect there directly. When SAML
// / OIDC move under /admin/tenant/identity/* (currently they live at
// /admin/sso), turn this into a real hub.
export const Route = createFileRoute('/_authenticated/admin/tenant/identity/')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/tenant/identity/ldap' })
  },
})
