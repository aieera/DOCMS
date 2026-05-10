import { useQuery } from '@tanstack/react-query'

import { runPersistedQuery } from '@/lib/graphql'
import { DocumentDetailQuery, ActivityForDocumentQuery } from '@/lib/graphql-operations'

// Typed mirrors of the GraphQL response. Hand-written rather than
// generated because:
//   1. We ship two operations; full graphql-codegen pipeline is
//      overkill for that scale.
//   2. Keeping the shape next to the hook makes the contract local
//      to the call site.
// When the operation set grows past ~5 we should swap to codegen.

export interface GqlDocumentDetail {
  id: string
  tenantId: string
  workspaceId: string
  folderId?: string
  title: string
  description?: string
  lifecycleState: string
  regionPin: string
  documentClass?: string
  classificationConfidence?: number
  tags: string[]
  mimeType?: string
  totalSizeBytes?: number
  sha256Hash?: string
  createdBy?: string
  createdAt?: string
  updatedAt?: string
  currentVersion: GqlVersion | null
  versions: { nodes: GqlVersion[]; nextCursor?: string }
  comments: { nodes: GqlComment[]; nextCursor?: string }
  annotations: { nodes: GqlAnnotation[]; nextCursor?: string }
  workflowInstances: GqlWorkflowInstance[]
  permissions: GqlPermissions
}

export interface GqlVersion {
  id: string
  versionNumber: number
  sizeBytes?: number
  mimeType?: string
  sha256Hash?: string
  createdAt?: string
  createdByName?: string
  changeSummary?: string
}

export interface GqlComment {
  id: string
  body: string
  authorName?: string
  authorId?: string
  createdAt: string
  resolved: boolean
  replies: GqlComment[]
}

export interface GqlAnnotation {
  id: string
  kind: string
  page?: number
  createdAt: string
}

export interface GqlWorkflowInstance {
  id: string
  status: string
  definitionName?: string
  startedAt: string
  tasks: Array<{
    id: string
    title: string
    status: string
    assigneeName?: string
    dueAt?: string
  }>
}

export interface GqlPermissions {
  canView: boolean
  canEdit: boolean
  canDelete: boolean
  canShare: boolean
  canAdmin: boolean
}

// useDocumentDetailGQL fires the single ADR 0074 DocumentDetail
// query — replaces 6+ REST round trips (doc + permissions + versions
// + comments + annotations + workflow instances) with one.
//
// Cache key is namespaced under 'gql' so it doesn't collide with
// the REST `['document', id]` cache the rest of the app uses for
// mutations + the legacy doc detail flow.
export function useDocumentDetailGQL(documentId: string | undefined) {
  return useQuery({
    queryKey: ['gql', 'document-detail', documentId],
    enabled: !!documentId,
    queryFn: async () => {
      const res = await runPersistedQuery<{ id: string }, { document: GqlDocumentDetail | null }>(
        DocumentDetailQuery,
        { id: documentId! },
      )
      if (res.errors?.length) {
        throw new Error(res.errors.map((e) => e.message).join('; '))
      }
      return res.data?.document ?? null
    },
  })
}

export interface GqlActivityEvent {
  id: string
  kind: string
  actorName?: string
  summary: string
  occurredAt: string
  payload?: string
}

export interface GqlActivityPage {
  nodes: GqlActivityEvent[]
  nextCursor?: string
}

// useActivityForDocument fans the audit + workflow + comment +
// signature streams into one chronological feed via the
// activityForDocument resolver. Keyset-paginated: pass the previous
// page's nextCursor as `cursor` for the next call.
export function useActivityForDocument(documentId: string | undefined, cursor?: string) {
  return useQuery({
    queryKey: ['gql', 'activity-for-document', documentId, cursor ?? null],
    enabled: !!documentId,
    queryFn: async () => {
      const res = await runPersistedQuery<
        { id: string; cursor?: string },
        { activityForDocument: GqlActivityPage }
      >(ActivityForDocumentQuery, { id: documentId!, cursor })
      if (res.errors?.length) {
        throw new Error(res.errors.map((e) => e.message).join('; '))
      }
      return res.data?.activityForDocument ?? { nodes: [] }
    },
  })
}
