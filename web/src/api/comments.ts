// ADR 0066 — comments + reactions client.
import { api } from './client'

export interface Comment {
  tenant_id: string
  id: string
  document_id: string
  version_id?: string | null
  parent_comment_id?: string | null
  author_id: string
  body: string
  is_resolved: boolean
  resolved_by?: string | null
  resolved_at?: string | null
  created_at: string
  updated_at: string
  deleted_at?: string | null
}

export interface ReactionAggregate {
  emoji: string
  count: number
  users: string[]
}

export async function listComments(documentId: string, includeResolved = false): Promise<Comment[]> {
  const params = includeResolved ? { include_resolved: 'true' } : {}
  const { data } = await api.get<Comment[]>(`/documents/${documentId}/comments`, { params })
  return data ?? []
}

export async function createComment(documentId: string, body: string, versionId?: string): Promise<Comment> {
  const { data } = await api.post<Comment>(`/documents/${documentId}/comments`, {
    body, version_id: versionId,
  })
  return data
}

export async function replyToComment(parentId: string, body: string): Promise<Comment> {
  const { data } = await api.post<Comment>(`/comments/${parentId}/replies`, { body })
  return data
}

export async function updateComment(id: string, body: string): Promise<Comment> {
  const { data } = await api.patch<Comment>(`/comments/${id}`, { body })
  return data
}

export async function deleteComment(id: string): Promise<void> {
  await api.delete(`/comments/${id}`)
}

export async function resolveComment(id: string): Promise<void> {
  await api.post(`/comments/${id}/resolve`)
}

export async function unresolveComment(id: string): Promise<void> {
  await api.post(`/comments/${id}/unresolve`)
}

export async function addReaction(commentId: string, emoji: string): Promise<void> {
  await api.post(`/comments/${commentId}/reactions`, { emoji })
}

export async function removeReaction(commentId: string, emoji: string): Promise<void> {
  await api.delete(`/comments/${commentId}/reactions`, { data: { emoji } })
}

export async function listReactions(commentId: string): Promise<ReactionAggregate[]> {
  const { data } = await api.get<ReactionAggregate[]>(`/comments/${commentId}/reactions`)
  return data ?? []
}

// ---- @mention helpers ----------------------------------------------------

// Wire format: @[Display Name](user-uuid). The backend's mentionRE
// matches exactly this shape.
export function mentionToken(displayName: string, userId: string): string {
  return `@[${displayName}](${userId})`
}

// Render a body with mentions formatted nicely. Returns an array of
// { text, user_id? } so callers can decide how to render mentions
// (link to the user, highlight, etc.).
export interface BodySegment {
  text: string
  userId?: string
}

const MENTION_RE = /@\[([^\]]+)\]\(([0-9a-fA-F-]{36})\)/g

export function parseBodyForRender(body: string): BodySegment[] {
  const out: BodySegment[] = []
  let last = 0
  for (const m of body.matchAll(MENTION_RE)) {
    const start = m.index ?? 0
    if (start > last) out.push({ text: body.slice(last, start) })
    out.push({ text: '@' + m[1], userId: m[2] })
    last = start + m[0].length
  }
  if (last < body.length) out.push({ text: body.slice(last) })
  return out
}
