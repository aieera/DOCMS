// Task API client — talks to services/task (2026-07-28 task-service
// design). Tasks carry MULTIPLE assignees and MULTIPLE linked documents,
// plus comments and an activity trail.
//
// Distinct from /api/v1/workflows/tasks, which surfaces approval-step
// state (workflow_tasks) and is what the Approvals tab reads.
import type { QueryClient } from '@tanstack/react-query'

import { api } from './client'

export type TaskStatus = 'open' | 'in_progress' | 'done' | 'cancelled'
export type TaskPriority = 'low' | 'normal' | 'high' | 'urgent'
export type TaskSource = 'user' | 'workflow'
export type TaskSort = 'due_at' | 'priority' | 'created_at'
export type TaskFilter = 'mine' | 'created' | 'all'

export interface TaskAssignee {
  user_id: string
  added_by: string
  added_at: string
}

export interface TaskDocument {
  document_id: string
  workspace_id: string
  /** Snapshot of the document title taken when it was linked. */
  title: string
  linked_by: string
  linked_at: string
}

export interface Task {
  id: string
  title: string
  description: string
  status: TaskStatus
  priority: TaskPriority
  source: TaskSource
  due_at?: string | null
  created_by: string
  completed_by?: string | null
  completed_at?: string | null
  created_at: string
  updated_at: string
  assignees: TaskAssignee[]
  documents: TaskDocument[]
}

export interface TaskComment {
  id: string
  task_id: string
  author_id: string
  body: string
  mentions: string[]
  created_at: string
  updated_at: string
}

export interface TaskActivityEntry {
  id: number
  task_id: string
  actor_id: string
  action: string
  detail: Record<string, unknown>
  created_at: string
}

/** Paginated envelope returned by GET /tasks. */
export interface TaskPage {
  items: Task[]
  total: number
  limit: number
  offset: number
}

export interface ListTasksParams {
  filter?: TaskFilter
  status?: TaskStatus
  priority?: TaskPriority
  document_id?: string
  q?: string
  include_completed?: boolean
  limit?: number
  offset?: number
  sort?: TaskSort
}

export interface CreateTaskInput {
  title: string
  description?: string
  priority?: TaskPriority
  due_at?: string // RFC3339
  assignee_ids?: string[]
  document_ids?: string[]
}

export interface UpdateTaskInput {
  title?: string
  description?: string
  priority?: TaskPriority
  due_at?: string
  clear_due_at?: boolean
}

// ---- query keys ------------------------------------------------------
//
// One `['tasks', …]` family so a single invalidateTasks() refreshes every
// consumer: both inbox tabs, the topbar badge, the dashboard card, the
// per-document panel, and any open detail drawer. The pre-rewrite client
// spread these across ['my-tasks'] / ['created-tasks'] / ['tasks'], which
// is why creating a task from a document left the badge stale.
export const taskKeys = {
  all: ['tasks'] as const,
  mine: () => ['tasks', 'mine'] as const,
  created: () => ['tasks', 'created'] as const,
  list: (params: ListTasksParams) => ['tasks', 'list', params] as const,
  detail: (id: string) => ['tasks', 'detail', id] as const,
  comments: (id: string) => ['tasks', 'comments', id] as const,
  activity: (id: string) => ['tasks', 'activity', id] as const,
}

/** Refresh every task view after a mutation. */
export function invalidateTasks(queryClient: QueryClient) {
  return queryClient.invalidateQueries({ queryKey: taskKeys.all })
}

// ---- reads -----------------------------------------------------------

function listParams(params: ListTasksParams): Record<string, string> {
  const out: Record<string, string> = {}
  if (params.filter) out.filter = params.filter
  if (params.status) out.status = params.status
  if (params.priority) out.priority = params.priority
  if (params.document_id) out.document_id = params.document_id
  if (params.q) out.q = params.q
  if (params.include_completed) out.include_completed = 'true'
  if (params.limit != null) out.limit = String(params.limit)
  if (params.offset != null) out.offset = String(params.offset)
  if (params.sort) out.sort = params.sort
  return out
}

export async function listTasks(params: ListTasksParams = {}): Promise<TaskPage> {
  const { data } = await api.get<TaskPage>('/tasks', { params: listParams(params) })
  return data ?? { items: [], total: 0, limit: 0, offset: 0 }
}

// /tasks/mine and /tasks/created return BARE ARRAYS (not the envelope) —
// a deliberate back-compat shim for the mobile app and the topbar badge.
export async function listMyTasks(includeCompleted = false): Promise<Task[]> {
  const params = includeCompleted ? { include_completed: 'true' } : {}
  const { data } = await api.get<Task[]>('/tasks/mine', { params })
  return data ?? []
}

export async function listMyCreatedTasks(includeCompleted = false): Promise<Task[]> {
  const params = includeCompleted ? { include_completed: 'true' } : {}
  const { data } = await api.get<Task[]>('/tasks/created', { params })
  return data ?? []
}

export async function getTask(id: string): Promise<Task> {
  const { data } = await api.get<Task>(`/tasks/${id}`)
  return data
}

export async function listComments(id: string): Promise<TaskComment[]> {
  const { data } = await api.get<TaskComment[]>(`/tasks/${id}/comments`)
  return data ?? []
}

export async function listActivity(id: string): Promise<TaskActivityEntry[]> {
  const { data } = await api.get<TaskActivityEntry[]>(`/tasks/${id}/activity`)
  return data ?? []
}

// ---- writes ----------------------------------------------------------

export async function createTask(input: CreateTaskInput): Promise<Task> {
  const { data } = await api.post<Task>('/tasks', input)
  return data
}

export async function updateTask(id: string, input: UpdateTaskInput): Promise<Task> {
  const { data } = await api.patch<Task>(`/tasks/${id}`, input)
  return data
}

export async function deleteTask(id: string): Promise<void> {
  await api.delete(`/tasks/${id}`)
}

export async function startTask(id: string): Promise<Task> {
  const { data } = await api.post<Task>(`/tasks/${id}/start`)
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

export async function addAssignee(id: string, userId: string): Promise<Task> {
  const { data } = await api.post<Task>(`/tasks/${id}/assignees`, { user_id: userId })
  return data
}

export async function removeAssignee(id: string, userId: string): Promise<Task> {
  const { data } = await api.delete<Task>(`/tasks/${id}/assignees/${userId}`)
  return data
}

export async function linkDocument(id: string, documentId: string): Promise<Task> {
  const { data } = await api.post<Task>(`/tasks/${id}/documents`, { document_id: documentId })
  return data
}

export async function unlinkDocument(id: string, documentId: string): Promise<Task> {
  const { data } = await api.delete<Task>(`/tasks/${id}/documents/${documentId}`)
  return data
}

export async function addComment(id: string, body: string): Promise<TaskComment> {
  const { data } = await api.post<TaskComment>(`/tasks/${id}/comments`, { body })
  return data
}

export async function updateComment(
  id: string,
  commentId: string,
  body: string,
): Promise<TaskComment> {
  const { data } = await api.patch<TaskComment>(`/tasks/${id}/comments/${commentId}`, { body })
  return data
}

export async function deleteComment(id: string, commentId: string): Promise<void> {
  await api.delete(`/tasks/${id}/comments/${commentId}`)
}
