import { api } from './client'

export interface PropagationStats {
  p50_seconds: number
  p95_seconds: number
  p99_seconds: number
  total_success: number
  total_failure: number
  pending_count: number
}

export async function getPermissionPropagationStats() {
  const { data } = await api.get<PropagationStats>(
    '/admin/permission-propagation-stats',
  )
  return data
}
