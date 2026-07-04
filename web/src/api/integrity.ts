import { api } from './client'

// Audit integrity (Merkle/hash-chain proof) + document WORM object-lock.

export interface MerkleProof {
  tenant_id: string
  resource_type?: string
  resource_id?: string
  count: number
  root_hash: string
  first_event_id?: string
  last_event_id?: string
  first_at?: string
  last_at?: string
  valid: boolean
  broken_at?: string
  broken_hash?: string
  expected_hash?: string
  leaf_hashes?: string[]
}

// Range-scoped Merkle/hash-chain proof over one document's audit trail.
export async function getDocumentMerkleProof(documentId: string): Promise<MerkleProof> {
  const { data } = await api.get<MerkleProof>('/audit/merkle-proof', {
    params: { resource_type: 'document', resource_id: documentId },
  })
  return data
}

export interface WormStatus {
  worm_retain_until: string | null
  locked: boolean
}

export async function getWormStatus(documentId: string): Promise<WormStatus> {
  const { data } = await api.get<WormStatus>(`/documents/${documentId}/worm`)
  return data
}

export async function wormLock(
  documentId: string,
  retainUntil: string,
  mode: 'GOVERNANCE' | 'COMPLIANCE' = 'COMPLIANCE',
): Promise<{ status: string; worm_retain_until: string }> {
  const { data } = await api.post<{ status: string; worm_retain_until: string }>(
    `/documents/${documentId}/worm-lock`,
    { retain_until: retainUntil, mode },
  )
  return data
}
