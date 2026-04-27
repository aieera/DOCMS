// Lightweight plan lookup for UI hints (upload limits, feature gates).
// Backend exposes the tier on /api/v1/admin/settings; we expose it here
// as a plain plan id + a derived byte ceiling.
//
// Falls back to 'standard' / 5 GiB when the settings endpoint is not
// reachable (e.g. the current user is not admin and settings isn't
// exposed to them). This is the safe default — it matches the backend's
// own fallback in services/storage/internal/service/plan.go.

import { useQuery } from '@tanstack/react-query'
import { api } from '@/api/client'

export type TenantPlan = 'standard' | 'enterprise' | 'dedicated'

// Match services/storage/internal/service/plan.go planSizeLimits.
const PLAN_LIMITS: Record<TenantPlan, number> = {
  standard: 5 * 1024 ** 3,
  enterprise: 50 * 1024 ** 3,
  dedicated: 100 * 1024 ** 3,
}

const PLAN_LABELS: Record<TenantPlan, string> = {
  standard: 'Standard',
  enterprise: 'Enterprise',
  dedicated: 'Dedicated',
}

export interface TenantPlanInfo {
  plan: TenantPlan
  label: string
  maxUploadBytes: number
  maxUploadHuman: string
  tooltip: string
}

export function useTenantPlan(): TenantPlanInfo {
  const { data } = useQuery({
    queryKey: ['tenant', 'plan'],
    queryFn: async () => {
      try {
        const { data } = await api.get<{ plan?: TenantPlan }>('/tenant/plan')
        return data.plan ?? 'standard'
      } catch {
        return 'standard' as TenantPlan
      }
    },
    staleTime: 5 * 60_000,
  })
  const plan = (data ?? 'standard') as TenantPlan
  const maxUploadBytes = PLAN_LIMITS[plan]
  const maxUploadHuman = `${Math.round(maxUploadBytes / 1024 ** 3)} GB`
  return {
    plan,
    label: PLAN_LABELS[plan],
    maxUploadBytes,
    maxUploadHuman,
    tooltip: `Max upload size: ${maxUploadHuman} (${PLAN_LABELS[plan]} tier)`,
  }
}
