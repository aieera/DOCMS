// Platform admin API client — internal-auth observability.
//
// The two endpoints here live on the workflow service today
// (/api/v1/platform/*). Axios's `api` base already points at /api/v1
// so paths here are written relative to that.

import { api } from './client'

// ---- Prometheus query passthrough -----------------------------------------

// PromResult mirrors the Prometheus HTTP API response. We keep it loose
// because the admin panel renders several result types (vector, scalar)
// and narrowing at parse time would lose fidelity on edge cases.
export interface PromVectorSample {
  metric: Record<string, string>
  value: [number, string] // [timestamp_seconds, value]
}

export interface PromQueryData {
  resultType: 'vector' | 'matrix' | 'scalar' | 'string'
  result: PromVectorSample[]
}

export interface PromQueryResponse {
  status: 'success' | 'error'
  data?: PromQueryData
  errorType?: string
  error?: string
}

interface MetricsQueryEnvelope {
  upstream: PromQueryResponse
}

export async function queryPrometheus(query: string, time?: string) {
  const { data } = await api.post<MetricsQueryEnvelope>('/platform/metrics/query', {
    query,
    time,
  })
  return data.upstream
}

// ---- Trusted-proxy dry-run tester -----------------------------------------

export interface TrustedProxyHop {
  addr: string
  trusted: boolean
  parseable: boolean
}

export interface TrustedProxyTestResult {
  resolved_ip: string
  peer: TrustedProxyHop
  hops: TrustedProxyHop[]
  trusted_cidrs: string[]
}

export async function testTrustedProxy(input: {
  x_forwarded_for: string
  remote_addr: string
}) {
  const { data } = await api.post<TrustedProxyTestResult>(
    '/platform/trusted-proxy/test',
    input,
  )
  return data
}

// ---- Temporal schedules listing ------------------------------------------

export interface ScheduleView {
  id: string
  paused: boolean
  next_run?: string
  last_run?: string
  num_actions: number
  num_missed?: number
  running_count: number
}

interface SchedulesResponse {
  schedules: ScheduleView[]
}

export async function listSchedules(): Promise<ScheduleView[]> {
  const { data } = await api.get<SchedulesResponse>('/platform/schedules')
  return data.schedules ?? []
}
