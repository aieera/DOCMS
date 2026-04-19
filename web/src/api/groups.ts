import { api } from './client'

export interface Group {
  id: string
  name: string
  description?: string
  created_by?: string
  created_at: string
  updated_at: string
  member_count: number
}

export interface GroupMember {
  user_id: string
  email: string
  display_name?: string
  added_at: string
}

export interface GroupDetail extends Group {
  members: GroupMember[]
}

export async function listGroups(): Promise<Group[]> {
  const { data } = await api.get<Group[]>('/admin/groups')
  return data ?? []
}

export async function getGroup(id: string): Promise<GroupDetail> {
  const { data } = await api.get<GroupDetail>(`/admin/groups/${id}`)
  // Older backend builds serialize an empty slice as null; normalize so
  // call sites can always call .length / .map without a null guard.
  return { ...data, members: data.members ?? [] }
}

export async function createGroup(input: { name: string; description?: string }): Promise<Group> {
  const { data } = await api.post<Group>('/admin/groups', input)
  return data
}

export async function updateGroup(id: string, input: { name?: string; description?: string }): Promise<void> {
  await api.patch(`/admin/groups/${id}`, input)
}

export async function deleteGroup(id: string): Promise<void> {
  await api.delete(`/admin/groups/${id}`)
}

export async function addGroupMember(id: string, userId: string): Promise<void> {
  await api.post(`/admin/groups/${id}/members`, { user_id: userId })
}

export async function removeGroupMember(id: string, userId: string): Promise<void> {
  await api.delete(`/admin/groups/${id}/members/${userId}`)
}
