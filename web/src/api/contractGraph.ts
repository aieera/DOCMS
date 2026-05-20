// Contract intelligence graph — ADR 0099 §18 F4.
import { api } from './client'

export type EdgeType = 'amends' | 'supersedes' | 'references'

export interface GraphNode {
  id: string
  title: string
  mime_type: string
  created_at: string
  is_root?: boolean
}

export interface GraphEdge {
  id: string
  src: string
  dst: string
  type: EdgeType
  confidence: number
  metadata: Record<string, unknown>
  created_at: string
}

export interface ContractGraph {
  root_id: string
  nodes: GraphNode[]
  edges: GraphEdge[]
  truncated?: boolean
}

export async function getContractGraph(documentId: string, depth = 2) {
  const { data } = await api.get<ContractGraph>(
    `/contracts/${documentId}/graph?depth=${depth}`,
  )
  return data
}

export async function createContractEdge(input: {
  documentId: string
  targetDocumentId: string
  edgeType: EdgeType
  metadata?: Record<string, unknown>
}) {
  const { data } = await api.post<{ id: string }>(
    `/contracts/${input.documentId}/edges`,
    {
      target_document_id: input.targetDocumentId,
      edge_type: input.edgeType,
      metadata: input.metadata ?? {},
    },
  )
  return data
}

export async function deleteContractEdge(documentId: string, edgeId: string) {
  await api.delete(`/contracts/${documentId}/edges/${edgeId}`)
}
