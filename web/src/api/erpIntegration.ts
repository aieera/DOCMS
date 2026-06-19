import { api } from './client'

// ── ERP integration (native Integrations → ERP explorer) ────────────────────
// These call /api/v1/integrations/erp/*, which the auth service proxies to the
// ERP integration "Files BFF" (/files/*), stamping the ERP identity
// server-side (the browser never talks to the BFF or sees the SeDoc service
// key it holds). Shapes mirror the BFF's JSON — see integration/internal/bff.

export interface ErpCustomer {
  customer_ref: string
  name: string
}

export interface ErpFolder {
  id: string
  name: string
  parentFolderId?: string
  documentCount: number
  childFolderCount: number
}

export interface ErpCustomerTree {
  customer_ref: string
  name: string
  main_folder_id: string
  folders: ErpFolder[]
  next_page_token?: string
}

export interface ErpDocumentRow {
  id: string
  title: string
  folderId: string
  documentClass: string
  lifecycleState: string
  currentVersionId: string
  mimeType: string
  totalSizeBytes: number
  externalId: string
  updatedAt: string
}

export interface ErpDocumentList {
  documents: ErpDocumentRow[]
  pagination?: { nextPageToken?: string }
}

export interface ErpVersion {
  id: string
  versionNumber: number
  sizeBytes: number
  mimeType: string
  createdByName: string
  createdAt: string
  label: string
}

export interface ErpDocumentDetail {
  document: ErpDocumentRow
  versions: ErpVersion[]
}

// listErpCustomers — provisioned customers for the picker. Admin-gated by the
// BFF; the "customer" concept lives in the ERP, so this is the only enumeration.
export async function listErpCustomers(): Promise<ErpCustomer[]> {
  const { data } = await api.get<{ items?: ErpCustomer[] }>('/integrations/erp/customers')
  return data?.items ?? []
}

export async function getErpCustomerTree(ref: string): Promise<ErpCustomerTree> {
  const { data } = await api.get<ErpCustomerTree>(
    `/integrations/erp/customers/${encodeURIComponent(ref)}/tree`,
  )
  return data
}

export async function getErpFolderDocuments(
  ref: string,
  folderId: string,
): Promise<ErpDocumentRow[]> {
  const { data } = await api.get<ErpDocumentList>(
    `/integrations/erp/customers/${encodeURIComponent(ref)}/folders/${folderId}/documents`,
  )
  return data?.documents ?? []
}

export async function getErpDocument(id: string): Promise<ErpDocumentDetail> {
  const { data } = await api.get<ErpDocumentDetail>(`/integrations/erp/documents/${id}`)
  return data
}

// erpDocumentDownloadURL — same-origin URL for a plain <a download> navigation.
// It carries the SeDoc session cookie (and, in dev, the Vite proxy's gateway
// signature), so the auth service authenticates it before the proxy forwards
// the streamed bytes from the BFF.
export function erpDocumentDownloadURL(id: string): string {
  return `/api/v1/integrations/erp/documents/${id}/download`
}
