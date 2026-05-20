// Cross-format compare — ADR 0101 §18 F8.
import { api } from './client'

export type DiffOp = 'equal' | 'insert' | 'delete'

export interface DiffOperation {
  op: DiffOp
  text: string
}

export interface CompareSideMeta {
  id: string
  title: string
  version_id: string
  text_chars: number
}

export interface CompareResponse {
  doc_a: CompareSideMeta
  doc_b: CompareSideMeta
  granularity: 'paragraph' | 'word' | 'semantic'
  diff: { operations: DiffOperation[] }
  summary: { added_chars: number; removed_chars: number; changed_blocks: number }
  truncated: boolean
}

export interface CompareRequest {
  doc_a_id: string
  version_a_id?: string
  doc_b_id: string
  version_b_id?: string
  granularity?: 'paragraph' | 'word' | 'semantic'
}

export async function compareDocuments(req: CompareRequest) {
  const { data } = await api.post<CompareResponse>('/compare', req)
  return data
}
