// Global admin banner. Mounted in AppLayout so it appears on every
// admin route. Non-admin users don't see it (the posture endpoint
// returns 403; the hook returns isError and we render null).
//
// Render rules:
//   - fetch errors / not-admin → null (silent; the page still works).
//   - any_failing === true     → red bar with the /admin/platform/security link.
//   - any_failing === false    → null (no banner when everything is green).

import { Link } from '@tanstack/react-router'
import { AlertTriangle } from 'lucide-react'

import { useSecurityPosture } from './useSecurityPosture'

export function SecurityBanner() {
  const { data, isError } = useSecurityPosture()

  // A failing posture fetch must not itself look like a failing gate.
  // Operators should see the banner only when a gate genuinely failed.
  if (isError || !data) return null
  if (!data.any_failing) return null

  // Pull the failing gate names for the banner copy so the operator
  // can act without navigating. Order matches the backend's canonical
  // scanTypes array.
  const failing = data.scans
    .filter((s: { status: string }) => s.status === 'fail')
    .map((s: { scan_type: string }) => friendlyName(s.scan_type))

  return (
    <div
      role="alert"
      data-testid="security-banner"
      className="border-b border-red-300 bg-red-600 text-white dark:border-red-900"
    >
      <div className="flex items-center gap-3 px-6 py-2 text-sm">
        <AlertTriangle className="h-4 w-4 flex-shrink-0" aria-hidden="true" />
        <span className="flex-1">
          <strong>Security CI gate failing.</strong>{' '}
          {failing.length > 0 ? `Failing: ${failing.join(', ')}. ` : ''}
          See{' '}
          <Link
            to="/admin/platform/security"
            className="underline underline-offset-2 hover:no-underline"
            data-testid="security-banner-link"
          >
            /admin/platform/security
          </Link>
          .
        </span>
      </div>
    </div>
  )
}

function friendlyName(scanType: string): string {
  switch (scanType) {
    case 'sast':
      return 'SAST'
    case 'dep_scan':
      return 'Dep scan'
    case 'dast':
      return 'DAST'
    case 'secret_scan':
      return 'Secret scan'
    default:
      return scanType
  }
}
