import { createFileRoute, redirect } from '@tanstack/react-router'

// /admin/intelligence/anomaly-reports is a guessable shortcut for
// the anomaly-detection results surface, which lives at
// /admin/intelligence/anomalies. The card label says "Anomaly
// reports" so users typing what they see in the UI ended up at 404.
// Soft redirect bridges the gap until the canonical file is renamed.
export const Route = createFileRoute('/_authenticated/admin/intelligence/anomaly-reports')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/intelligence/anomalies' })
  },
})
