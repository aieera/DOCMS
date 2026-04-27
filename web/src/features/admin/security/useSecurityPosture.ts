// Shared react-query hook so the banner + the full page read the same
// cached posture object. The banner is mounted on every admin route;
// without the shared key we'd double-fetch whenever the user loads
// the page.

import { useQuery } from '@tanstack/react-query'

import { getSecurityPosture, type SecurityPosture } from '@/api/platform'

export const SECURITY_POSTURE_QUERY_KEY = ['platform-security-posture'] as const

export function useSecurityPosture() {
  return useQuery<SecurityPosture>({
    queryKey: SECURITY_POSTURE_QUERY_KEY,
    queryFn: getSecurityPosture,
    // Posture doesn't move minute-to-minute; 60 s refresh keeps the
    // banner current without hammering the aggregator.
    refetchInterval: 60_000,
    staleTime: 30_000,
    // A 403 / 503 shouldn't cascade a toast to every admin page.
    // Callers (the banner) treat isError + undefined data as
    // "unknown" and render accordingly.
    retry: 1,
  })
}
