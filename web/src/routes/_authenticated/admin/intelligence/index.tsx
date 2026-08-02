import { createFileRoute, redirect } from '@tanstack/react-router'

// /admin/intelligence was a 13-card hub from before the admin
// consolidation: 10 of its cards pointed at URLs that immediately
// redirect into the merged tab pages, and nothing in the app linked to
// it (the admin hub and sidebar both go straight to the consolidated
// destinations). It survives only as a breadcrumb segment and old
// bookmarks — both now land on the canonical hub instead of a page of
// double-hop redirects.
export const Route = createFileRoute('/_authenticated/admin/intelligence/')({
  beforeLoad: () => {
    throw redirect({ to: '/admin', replace: true })
  },
})
