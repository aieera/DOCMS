// ADR 0086 — unified notification preferences API client.
import { api } from './client'

export type Channel = 'in_app' | 'email' | 'push' | 'slack' | 'teams' | 'sms'

export interface PrefCell {
  tenant_id?: string
  user_id?: string
  channel: Channel
  event_type: string
  is_enabled: boolean
  digest_enabled: boolean
}

export interface Snooze {
  id: string
  event_type: string
  until_at: string
  reason?: string
  created_at: string
}

export interface DND {
  dnd_start: string // "HH:MM"
  dnd_end: string
  timezone: string
  updated_at?: string
}

// CHANNELS / EVENT_TYPES drive the matrix grid in the settings UI.
// Kept here so the page renders even before the user has saved any
// cells (the backend returns []; the UI fills in defaults).
export const CHANNELS: Channel[] = ['in_app', 'email', 'push', 'slack', 'teams', 'sms']
export const EVENT_TYPES: { id: string; label: string }[] = [
  { id: 'document.shared',         label: 'Document shared with me' },
  { id: 'comment.mention',         label: 'Comment mentions me' },
  { id: 'comment.reply',           label: 'Reply to my comment' },
  { id: 'workflow.step_assigned',  label: 'Workflow step assigned' },
  // Task ids MUST match the `type` the task service puts on its
  // dms.notify.task.*.v1 payloads ("task." + suffix). `task.due` used to
  // be listed here, but nothing ever emits that type — the emitter has
  // always sent task.due_soon — so the toggle wrote preference rows that
  // could never match, and since Decide() defaults to enabled when no row
  // matches, due-soon mail could not be switched off.
  { id: 'task.assigned',           label: 'Task assigned to me' },
  { id: 'task.due_soon',           label: 'Task due soon' },
  { id: 'task.overdue',            label: 'Task overdue' },
  { id: 'task.mention',            label: 'Mentioned in a task comment' },
  { id: 'task.completed',          label: 'Task completed' },
  { id: 'document.version_uploaded', label: 'New version uploaded' },
  { id: 'signature.requested',     label: 'Signature requested' },
]

export async function getMatrix(): Promise<PrefCell[]> {
  const { data } = await api.get<{ cells: PrefCell[] }>('/notifications/preferences/matrix')
  return data?.cells ?? []
}

export async function putMatrix(cells: PrefCell[]): Promise<void> {
  await api.put('/notifications/preferences/matrix', { cells })
}

export async function patchCell(cell: PrefCell): Promise<void> {
  await api.patch('/notifications/preferences/cell', cell)
}

export async function listSnoozes(): Promise<Snooze[]> {
  const { data } = await api.get<{ snoozes: Snooze[] }>('/notifications/snoozes')
  return data?.snoozes ?? []
}

export async function createSnooze(input: { event_type: string; duration_minutes: number; reason?: string }): Promise<Snooze> {
  const { data } = await api.post<Snooze>('/notifications/snooze', input)
  return data
}

export async function deleteSnooze(id: string): Promise<void> {
  await api.delete(`/notifications/snooze/${id}`)
}

export async function getDND(): Promise<DND | null> {
  const { data } = await api.get<{ dnd: DND | null }>('/notifications/dnd')
  return data?.dnd ?? null
}

export async function putDND(input: { start: string; end: string; timezone: string }): Promise<DND> {
  const { data } = await api.put<DND>('/notifications/dnd', input)
  return data
}

export async function deleteDND(): Promise<void> {
  await api.delete('/notifications/dnd')
}
