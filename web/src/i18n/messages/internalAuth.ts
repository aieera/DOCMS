// Message constants for the internal-auth admin panel.
//
// Kept as a flat object of en-US strings rather than plugged into an
// IntlProvider because react-intl is not yet wired up at the app
// level. When it is, this file converts cleanly to `defineMessages()`
// — every string already has a stable key suitable for a message id.
//
// Consumer pattern:
//   import { internalAuthMessages as M } from '@/i18n/messages/internalAuth'
//   toast.success(M.healthRefreshed)
//
// Reviewing: when adding a user-visible string to the panel, add it
// here and reference by key. Do not inline strings in components.

export const internalAuthMessages = {
  pageTitle: 'Internal Auth Plane',
  pageDescription:
    'Live health of the internal-auth (mTLS + HMAC) surface across every Go service.',

  healthRefreshed: 'Internal auth health refreshed',
  healthError: 'Failed to load internal auth metrics — check platform logs.',

  metricsBackendUnconfigured:
    'Metrics backend not configured. Set VAULTDMS_PROMETHEUS_URL on the workflow service and restart.',

  servicesCardTitle: 'Services',
  modeLabel: 'Mode',
  modeUnknown: 'unknown',
  successCountLabel: 'Success (24h)',
  rejectedCountLabel: 'Rejected (24h)',
  certExpiryLabel: 'Cert expiry',
  certExpiryUnknown: 'no client cert',
  certDaysSuffix: 'd',

  trustedProxyTitle: 'Trusted Proxy CIDRs',
  trustedProxyDescription:
    'Read-only. Update via the VAULTDMS_TRUSTED_PROXY_CIDRS ConfigMap and restart affected pods.',
  trustedProxyEmpty:
    'No CIDRs loaded. In VAULTDMS_ENV=production the process would refuse to start.',

  dryRunTitle: 'Dry-run tester',
  dryRunDescription:
    'Paste an X-Forwarded-For chain and a RemoteAddr; the server returns the resolved client IP using the same logic the middleware applies.',
  dryRunXff: 'X-Forwarded-For',
  dryRunRemoteAddr: 'RemoteAddr',
  dryRunRun: 'Resolve',
  dryRunResolvedIP: 'Resolved client IP',
  dryRunPeerLabel: 'Peer',
  dryRunHopsLabel: 'Chain (right → left)',
  dryRunHopTrusted: 'trusted',
  dryRunHopUntrusted: 'external',
  dryRunHopUnparseable: 'unparseable',
} as const

export type InternalAuthMessageKey = keyof typeof internalAuthMessages
