import { createFileRoute, redirect } from '@tanstack/react-router'

// /admin/tenant doesn't have content of its own — it's an intermediate
// path that appears in breadcrumbs because /admin/tenant/license and
// /admin/tenant/ai live underneath. Without this redirect, a click on
// the "tenant" crumb 404s.
//
// Target: /admin/tenant/license. The License surface is the most
// "tenant-administration"-feeling of the children; the AI provider
// is one config knob, License is the broader tenant entitlement view.
// If/when /admin/tenant grows a hub view (settings overview, members
// count, plan summary), replace this redirect with a real component.
export const Route = createFileRoute('/_authenticated/admin/tenant/')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/tenant/license' })
  },
})
