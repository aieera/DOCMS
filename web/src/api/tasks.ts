// ADR 0068 — lightweight tasks API client.
// Distinct from /api/v1/workflows/tasks which surfaces approval-step
// state (workflow_tasks); these are the user-created + workflow-
// generated to-do items.
import { api } from './client'

export type TaskStatus   = 'open' | 'in_progress' | 'done' | 'cancelled'
export type TaskPriority = 'low' | 'normal' | 'high' | 'urgent'
export type TaskSource   = 'user' | 'workflow'

export interface Task {
  id: string
  title: string
  description: string
  status: TaskStatus
  priority: TaskPriority
  due_at?: string | null
  assignee_id?: string | null
  linked_document_id?: string | null
  linked_workflow_instance_id?: string | null
  source: TaskSource
  created_by: string
  completed_by?: string | null
  completed_at?: string | null
  created_at: string
  updated_at: string
}

export interface CreateTaskInput {
  title: string
  description?: string
  priority?: TaskPriority
  due_at?: string                         // RFC3339
  assignee_id?: string
  linked_document_id?: string
  linked_workflow_instance_id?: string
}

export interface UpdateTaskInput {
  title?: string
  description?: string
  priority?: TaskPriority
  due_at?: string
  clear_due_at?: boolean
}

export async function listMyTasks(includeCompleted = false): Promise<Task[]> {
  const params = includeCompleted ? { include_completed: 'true' } : {}
  const { data } = await api.get<Task[]>('/tasks/mine', { params })
  return data ?? []
}

export async function listTasks(query: { document_id?: string; status?: TaskStatus; include_completed?: boolean } = {}): Promise<Task[]> {
  const params: Record<string, string> = {}
  if (query.document_id) params.document_id = query.document_id
  if (query.status) params.status = query.status
  if (query.include_completed) params.include_completed = 'true'
  const { data } = await api.get<Task[]>('/tasks', { params })
  return data ?? []
}

export async function createTask(input: CreateTaskInput): Promise<Task> {
  const { data } = await api.post<Task>('/tasks', input)
  return data
}

export async function getTask(id: string): Promise<Task> {
  const { data } = await api.get<Task>(`/tasks/${id}`)
  return data
}

export async function updateTask(id: string, input: UpdateTaskInput): Promise<Task> {
  const { data } = await api.patch<Task>(`/tasks/${id}`, input)
  return data
}

export async function assignTask(id: string, assignee_id: string): Promise<Task> {
  const { data } = await api.post<Task>(`/tasks/${id}/assign`, { assignee_id })
  return data
}

export async function unassignTask(id: string): Promise<Task> {
  const { data } = await api.post<Task>(`/tasks/${id}/unassign`)
  return data
}

export async function completeTask(id: string): Promise<Task> {
  const { data } = await api.post<Task>(`/tasks/${id}/complete`)
  return data
}

export async function reopenTask(id: string): Promise<Task> {
  const { data } = await api.post<Task>(`/tasks/${id}/reopen`)
  return data
}

export async function cancelTask(id: string): Promise<Task> {
  const { data } = await api.post<Task>(`/tasks/${id}/cancel`)
  return data
}

export async function deleteTask(id: string): Promise<void> {
  await api.delete(`/tasks/${id}`)
}
