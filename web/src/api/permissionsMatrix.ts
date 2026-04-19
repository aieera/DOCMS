import { api } from './client'

export interface PermissionCell {
  role: string
  resource_type: string
  max_capability: string // admin | delete | edit | share | view | ''
  source: string
}

export interface PermissionMatrix {
  roles: string[]
  resource_types: string[]
  capabilities: string[]
  cells: PermissionCell[]
  notes: string[]
}

export async function getPermissionMatrix(): Promise<PermissionMatrix> {
  const { data } = await api.get<PermissionMatrix>('/permissions/matrix')
  return data
}
