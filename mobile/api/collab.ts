// Comments (threaded, per-document) + annotations (per-version) — the
// same REST surface the web viewer uses (web/src/api/annotations.ts /
// CommentsPanel). The collaboration WebSocket is live-update sugar the
// mobile app skips; REST is complete on its own.
import { api } from './client'

// ---- comments --------------------------------------------------------

// Wire shape from comment_handler.go: a FLAT list — replies reference
// their parent via parent_comment_id; resolution is is_resolved; the
// author is author_id (no display-name join on this endpoint).
export interface Comment {
  id: string
  document_id: string
  version_id?: string
  parent_comment_id?: string
  body: string
  author_id: string
  is_resolved: boolean
  created_at: string
}

/** A top-level comment with its replies grouped client-side. */
export interface CommentThread extends Comment {
  replies: Comment[]
}

export async function listComments(documentID: string, includeResolved = false): Promise<CommentThread[]> {
  const { data } = await api.get(`/documents/${documentID}/comments`, {
    params: includeResolved ? { include_resolved: true } : {},
  })
  const flat: Comment[] = Array.isArray(data) ? data : data?.comments ?? data?.items ?? []
  const tops: CommentThread[] = []
  const byID = new Map<string, CommentThread>()
  for (const c of flat) {
    if (!c.parent_comment_id) {
      const t = { ...c, replies: [] as Comment[] }
      tops.push(t)
      byID.set(c.id, t)
    }
  }
  for (const c of flat) {
    if (c.parent_comment_id) {
      const parent = byID.get(c.parent_comment_id)
      if (parent) parent.replies.push(c)
      else tops.push({ ...c, replies: [] }) // orphan (parent filtered out) — show flat
    }
  }
  return tops
}

export async function createComment(documentID: string, body: string): Promise<Comment> {
  const { data } = await api.post(`/documents/${documentID}/comments`, { body })
  return data
}

export async function replyToComment(commentID: string, body: string): Promise<Comment> {
  const { data } = await api.post(`/comments/${commentID}/replies`, { body })
  return data
}

export async function resolveComment(commentID: string): Promise<void> {
  await api.post(`/comments/${commentID}/resolve`)
}

// ---- annotations -----------------------------------------------------

export interface Annotation {
  id: string
  page: number
  type: string
  data: Record<string, unknown>
  created_by?: string
  created_by_name?: string
  created_at?: string
}

export async function listAnnotations(documentID: string, versionID: string): Promise<Annotation[]> {
  const { data } = await api.get(`/documents/${documentID}/versions/${versionID}/annotations`)
  return Array.isArray(data) ? data : data?.annotations ?? []
}

/**
 * A "note" annotation pinned to a page — the mobile-authorable subset of
 * the web viewer's markup vocabulary (rect/draw markup needs the canvas
 * tooling the web has). The web viewer renders these alongside its own.
 */
export async function createNoteAnnotation(
  documentID: string,
  versionID: string,
  page: number,
  body: string,
): Promise<Annotation> {
  const { data } = await api.post(`/documents/${documentID}/versions/${versionID}/annotations`, {
    page,
    type: 'pdf_markup',
    data: { kind: 'note', page, body, rects: [] },
  })
  return data
}

export async function deleteAnnotation(annotationID: string): Promise<void> {
  await api.delete(`/annotations/${annotationID}`)
}

// ---- versions --------------------------------------------------------

export interface VersionRow {
  id: string
  version_number?: number
  created_at?: string
}

export async function listVersions(documentID: string): Promise<VersionRow[]> {
  const { data } = await api.get(`/documents/${documentID}/versions`)
  return Array.isArray(data) ? data : data?.versions ?? data?.items ?? []
}
